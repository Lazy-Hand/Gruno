// Package secrets keeps the values of environment secrets (the names in
// an environment's vars:secret block) outside the collection, as Bruno
// does: the .bru file names the secret, the value stays on this machine.
//
// Values live in one JSON file, each environment's map sealed with
// AES-256-GCM under a key kept in the system credential store. The
// credential store alone would do, but it caps one item at a few KB,
// which a couple of JWTs exceed.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// KeySource returns the 32-byte encryption key, creating it on first
// use. The app backs it with the credential store; tests with a fixed
// key.
type KeySource func() ([]byte, error)

// Store reads and writes secret values. It is safe for concurrent use.
type Store struct {
	path string
	key  KeySource

	mu     sync.Mutex
	cached []byte
	// doc is the last file read or written, so a burst of secret reads
	// does not re-read and re-parse the JSON each time.
	doc *file
}

// New returns a store over the file at path.
func New(path string, key KeySource) *Store {
	return &Store{path: path, key: key}
}

// file is the on-disk form: sealed entries by envID.
type file struct {
	Version int               `json:"version"`
	Entries map[string]string `json:"entries"` // envID → base64(nonce || ciphertext)
}

// envID names a collection's environment in the file without spelling
// out the collection path.
func envID(collectionRoot, env string) string {
	h := sha256.Sum256([]byte(filepath.Clean(collectionRoot) + "\x00" + env))
	return hex.EncodeToString(h[:16])
}

func (s *Store) load() (file, error) {
	if s.doc != nil {
		return *s.doc, nil
	}
	f := file{Version: 1, Entries: map[string]string{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("secrets: %s: %w", s.path, err)
	}
	if f.Entries == nil {
		f.Entries = map[string]string{}
	}
	s.doc = &f
	return f, nil
}

func (s *Store) save(f file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		s.doc = nil
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.doc = nil
		return err
	}
	s.doc = &f
	return nil
}

func (s *Store) aead() (cipher.AEAD, error) {
	if s.cached == nil {
		k, err := s.key()
		if err != nil {
			return nil, fmt.Errorf("读取 secret 加密密钥失败: %w", err)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("secret 加密密钥长度 %d，应为 32", len(k))
		}
		s.cached = k
	}
	block, err := aes.NewCipher(s.cached)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Get returns the secret values of one environment, empty when none
// are stored. It touches the key, and so the credential store, only
// when there is something to decrypt.
func (s *Store) Get(collectionRoot, env string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	id := envID(collectionRoot, env)
	sealed, ok := f.Entries[id]
	if !ok {
		return map[string]string{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("secret 数据损坏: %w", err)
	}
	g, err := s.aead()
	if err != nil {
		return nil, err
	}
	if len(raw) < g.NonceSize() {
		return nil, errors.New("secret 数据损坏")
	}
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(id))
	if err != nil {
		return nil, errors.New("无法解密 secret（加密密钥可能已更换）")
	}
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Set replaces the secret values of one environment; an empty map
// removes its entry.
func (s *Store) Set(collectionRoot, env string, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	id := envID(collectionRoot, env)
	if len(values) == 0 {
		if _, ok := f.Entries[id]; !ok {
			return nil
		}
		delete(f.Entries, id)
		return s.save(f)
	}
	g, err := s.aead()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := g.Seal(nonce, nonce, plain, []byte(id))
	f.Entries[id] = base64.StdEncoding.EncodeToString(sealed)
	return s.save(f)
}

// Delete removes one environment's secrets.
func (s *Store) Delete(collectionRoot, env string) error {
	return s.Set(collectionRoot, env, nil)
}

// NewKey returns a fresh random 32-byte key.
func NewKey() ([]byte, error) {
	k := make([]byte, 32)
	_, err := rand.Read(k)
	return k, err
}
