package ui

// store.go reads and writes a Bruno collection directory: bruno.json,
// collection.bru, folder.bru per directory, environments/*.bru and one
// .bru file per request. Writes go to temp files renamed over the
// originals so a crash never leaves a half-written request.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gruno/bru"
	"gruno/internal/script"
	"gruno/internal/send"
)

// Collection is a Bruno collection opened on disk.
type Collection struct {
	Root         string             // absolute path of the collection dir
	Name         string             // from bruno.json
	Dot          *bru.CollectionBru // collection.bru, nil when absent
	Folders      []*Folder
	Requests     []*BruRequest
	Environments []*bru.Environment
	ActiveEnv    string // last-used environment name, persisted in bruno.json

	// Proxy and ClientCerts come from bruno.json, certificate paths as
	// stored; ClientCertsOn is its clientCertificates.enabled.
	Proxy         send.ProxySettings
	ClientCerts   []send.ClientCert
	ClientCertsOn bool

	DotCRLF bool // collection.bru uses CRLF line endings

	// parseErrs maps a file's collection-relative path to its parse
	// error. Such a file is never written back: the in-memory default
	// would replace the user's content.
	parseErrs map[string]string

	envMu    sync.Mutex
	dotEnv   map[string]string
	dotEnvAt time.Time
}

// Folder is a directory inside the collection; Path is relative to the
// root, "" for none (folders always have one).
type Folder struct {
	Path string // relative dir path, "/" separators
	Name string // display name; folder.bru meta.name wins over dirname
	Seq  int
	// Vars/Headers/Scripts come from the folder's folder.bru.
	Bru     *bru.CollectionBru
	BruCRLF bool // folder.bru uses CRLF line endings
}

// BruRequest is a parsed request .bru file plus its location.
type BruRequest struct {
	Path     string // path of the .bru file relative to the collection root
	Folder   string // containing directory relative to root, "" = root
	FileName string
	Req      *bru.Request
	CRLF     bool // the file uses CRLF line endings
}

// brunoMeta is bruno.json: Bruno writes {"version","name","type","ignore"}.
type brunoMeta struct {
	Version   string   `json:"version"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Ignore    []string `json:"ignore"`
	ActiveEnv string   `json:"activeEnvironment,omitempty"`
}

// OpenCollection loads a collection directory. Root itself is validated
// by the presence of bruno.json or at least one .bru file.
func OpenCollection(root string) (*Collection, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s 不是目录", root)
	}

	c := &Collection{Root: root, Dot: &bru.CollectionBru{Auth: "none"}, parseErrs: map[string]string{}}

	// bruno.json.
	var meta brunoMeta
	if data, err := os.ReadFile(filepath.Join(root, "bruno.json")); err == nil {
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("bruno.json: %w", err)
		}
		if o, err := parseOrderedJSON(data); err == nil {
			c.Proxy = parseProxy(o)
			c.ClientCerts, c.ClientCertsOn = parseClientCerts(o)
		}
	}
	c.Name = meta.Name
	c.ActiveEnv = meta.ActiveEnv
	if c.Name == "" {
		c.Name = filepath.Base(root)
	}

	// collection.bru.
	if data, err := os.ReadFile(filepath.Join(root, "collection.bru")); err == nil {
		c.DotCRLF = hasCRLF(data)
		if cb, err := bru.ParseCollection(string(data)); err == nil {
			c.Dot = cb
		} else {
			c.parseErrs["collection.bru"] = err.Error()
		}
	}

	// Walk: dirs become folders, .bru files requests, environments kept
	// apart. Anything inside environments/ is not a request.
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(path)
		if d.IsDir() {
			switch base {
			case "environments", ".git", "node_modules", ".bru":
				return filepath.SkipDir
			}
			if rel == "." {
				return nil
			}
			if strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			f := &Folder{Path: rel, Name: base}
			if data, err := os.ReadFile(filepath.Join(path, "folder.bru")); err == nil {
				f.BruCRLF = hasCRLF(data)
				if cb, err := bru.ParseCollection(string(data)); err == nil {
					f.Bru = cb
					if cb.Meta.Name != "" {
						f.Name = cb.Meta.Name
					}
					f.Seq = cb.Meta.Seq
				} else {
					c.parseErrs[rel+"/folder.bru"] = err.Error()
				}
			}
			c.Folders = append(c.Folders, f)
			return nil
		}
		if !strings.HasSuffix(base, ".bru") || strings.HasPrefix(base, ".") {
			return nil
		}
		if rel == "collection.bru" || base == "folder.bru" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		req, err := bru.ParseRequest(string(data))
		if err != nil {
			// Skip unreadable files rather than fail the whole collection;
			// Bruno is forgiving the same way. Remember why: the file is
			// never overwritten from an empty in-memory request.
			c.parseErrs[rel] = err.Error()
			return nil
		}
		c.Requests = append(c.Requests, &BruRequest{
			Path:     rel,
			FileName: base,
			Req:      req,
			CRLF:     hasCRLF(data),
		})
		return nil
	}); err != nil {
		return nil, err
	}

	// Fix Folder field of requests: filepath.Dir of root-level files is ".".
	for _, r := range c.Requests {
		dir := filepath.ToSlash(filepath.Dir(r.Path))
		if dir == "." {
			dir = ""
		}
		r.Folder = dir
	}

	// environments.
	envDir := filepath.Join(root, "environments")
	if entries, err := os.ReadDir(envDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".bru") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(envDir, e.Name()))
			if err != nil {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".bru")
			if env, err := bru.ParseEnvironment(string(data), name); err == nil {
				env.CRLF = hasCRLF(data)
				c.Environments = append(c.Environments, env)
			} else {
				c.parseErrs["environments/"+e.Name()] = err.Error()
			}
		}
		sort.Slice(c.Environments, func(i, j int) bool { return c.Environments[i].Name < c.Environments[j].Name })
	}

	c.sortAll()
	return c, nil
}

// sortAll orders folders and requests by seq then name.
func (c *Collection) sortAll() {
	sort.SliceStable(c.Folders, func(i, j int) bool {
		if c.Folders[i].Seq != c.Folders[j].Seq {
			return c.Folders[i].Seq < c.Folders[j].Seq
		}
		return c.Folders[i].Name < c.Folders[j].Name
	})
	sort.SliceStable(c.Requests, func(i, j int) bool {
		a, b := c.Requests[i], c.Requests[j]
		if a.Folder != b.Folder {
			return a.Folder < b.Folder
		}
		if a.Req.Meta.Seq != b.Req.Meta.Seq {
			return a.Req.Meta.Seq < b.Req.Meta.Seq
		}
		return a.Req.Meta.Name < b.Req.Meta.Name
	})
}

// CreateCollection makes a new collection directory with bruno.json.
func CreateCollection(root, name string) (*Collection, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	meta := brunoMeta{Version: "1", Name: name, Type: "collection", Ignore: []string{"node_modules", ".git"}}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(root, "bruno.json"), data); err != nil {
		return nil, err
	}
	return OpenCollection(root)
}

// hasCRLF reports whether data uses CRLF line endings.
func hasCRLF(data []byte) bool { return strings.Contains(string(data), "\r\n") }

// withLineEnding converts LF text to CRLF when the original file used it.
func withLineEnding(text string, crlf bool) []byte {
	if crlf {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	}
	return []byte(text)
}

// guardSave refuses to write a file whose parse failed on load.
func (c *Collection) guardSave(rel string) error {
	if msg, ok := c.parseErrs[rel]; ok {
		return fmt.Errorf("%s 解析失败，为避免覆盖原文件已拒绝写入: %s", rel, msg)
	}
	return nil
}

// writeFileAtomic writes data to path via a sibling temp + rename.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bru-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var unsafeName = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]`)

// bruFileName turns a request name into a filename. Bruno keeps the
// human name in meta.name and uses a filesystem-safe version as the
// file name.
func bruFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "untitled"
	}
	name = unsafeName.ReplaceAllString(name, "-")
	return name + ".bru"
}

// AddRequest creates a .bru file in folder ("" = root) and registers it.
func (c *Collection) AddRequest(folder, name string) (*BruRequest, error) {
	r := &bru.Request{
		Meta:   bru.Meta{Name: name, Type: "http", Seq: c.nextSeq(folder)},
		Method: "get",
		Body:   "none",
		Auth:   "none",
	}
	return c.writeNewRequest(folder, r, "")
}

// writeNewRequest picks a free filename and writes r under folder.
func (c *Collection) writeNewRequest(folder string, r *bru.Request, fileName string) (*BruRequest, error) {
	if fileName == "" {
		fileName = bruFileName(r.Meta.Name)
	}
	dir := filepath.Join(c.Root, filepath.FromSlash(folder))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fileName)
	// Find a free name: name.bru, name-1.bru, ...
	for n := 1; ; n++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		stem := strings.TrimSuffix(fileName, ".bru")
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.bru", stem, n))
	}
	if err := writeFileAtomic(path, []byte(bru.SerializeRequest(r))); err != nil {
		return nil, err
	}
	rel := filepath.ToSlash(mustRel(c.Root, path))
	br := &BruRequest{Path: rel, Folder: folder, FileName: filepath.Base(path), Req: r}
	c.Requests = append(c.Requests, br)
	return br, nil
}

// nextSeq returns one past the highest seq among siblings of folder.
func (c *Collection) nextSeq(folder string) int {
	maxSeq := 0
	for _, r := range c.Requests {
		if r.Folder == folder && r.Req.Meta.Seq > maxSeq {
			maxSeq = r.Req.Meta.Seq
		}
	}
	return maxSeq + 1
}

// SaveRequest writes r.Req back to its file.
func (c *Collection) SaveRequest(r *BruRequest) error {
	if err := c.guardSave(r.Path); err != nil {
		return err
	}
	data := withLineEnding(bru.SerializeRequest(r.Req), r.CRLF)
	return writeFileAtomic(filepath.Join(c.Root, filepath.FromSlash(r.Path)), data)
}

// SaveCollectionBru writes collection.bru at the root of the collection.
func (c *Collection) SaveCollectionBru() error {
	if c.Dot == nil {
		return nil
	}
	if err := c.guardSave("collection.bru"); err != nil {
		return err
	}
	data := withLineEnding(bru.SerializeCollection(c.Dot), c.DotCRLF)
	return writeFileAtomic(filepath.Join(c.Root, "collection.bru"), data)
}

// SaveFolderBru writes folder.bru for f.
func (c *Collection) SaveFolderBru(f *Folder) error {
	if f == nil || f.Bru == nil {
		return nil
	}
	rel := f.Path + "/folder.bru"
	if err := c.guardSave(rel); err != nil {
		return err
	}
	data := withLineEnding(bru.SerializeCollection(f.Bru), f.BruCRLF)
	return writeFileAtomic(filepath.Join(c.Root, filepath.FromSlash(f.Path), "folder.bru"), data)
}

// RenameFolder updates the display name of f in folder.bru.
func (c *Collection) RenameFolder(f *Folder, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	f.Name = name
	if f.Bru == nil {
		f.Bru = &bru.CollectionBru{Auth: "none"}
	}
	f.Bru.Meta.Name = name
	return c.SaveFolderBru(f)
}

// RenameCollection updates the collection's display name in bruno.json.
func (c *Collection) RenameCollection(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	c.Name = name
	return c.updateBrunoJSON(func(o *orderedJSON) error { return o.set("name", name) })
}

// DeleteEnvironment removes the environment file and unregisters it.
func (c *Collection) DeleteEnvironment(e *bru.Environment) error {
	if e == nil {
		return nil
	}
	path := filepath.Join(c.Root, "environments", bruFileName(e.Name))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i, x := range c.Environments {
		if x == e || x.Name == e.Name {
			c.Environments = append(c.Environments[:i], c.Environments[i+1:]...)
			break
		}
	}
	if c.ActiveEnv == e.Name {
		c.SetActiveEnv("")
	}
	return nil
}

// RenameRequest changes the display name (meta.name). The file name is
// kept; Bruno separates the two.
func (c *Collection) RenameRequest(r *BruRequest, name string) error {
	r.Req.Meta.Name = name
	return c.SaveRequest(r)
}

// DeleteRequest removes the file and unregisters it.
func (c *Collection) DeleteRequest(r *BruRequest) error {
	if err := os.Remove(filepath.Join(c.Root, filepath.FromSlash(r.Path))); err != nil {
		return err
	}
	for i, x := range c.Requests {
		if x == r {
			c.Requests = append(c.Requests[:i], c.Requests[i+1:]...)
			break
		}
	}
	return nil
}

// CloneRequest duplicates r into the same folder.
func (c *Collection) CloneRequest(r *BruRequest) (*BruRequest, error) {
	dup := r.Req.Clone()
	dup.Meta.Name = r.Req.Meta.Name + " copy"
	dup.Meta.Seq = c.nextSeq(r.Folder)
	return c.writeNewRequest(r.Folder, dup, "")
}

// MoveRequest puts r into folder, rewriting its path.
func (c *Collection) MoveRequest(r *BruRequest, folder string) error {
	newDir := filepath.Join(c.Root, filepath.FromSlash(folder))
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return err
	}
	newPath := filepath.Join(newDir, r.FileName)
	oldPath := filepath.Join(c.Root, filepath.FromSlash(r.Path))
	if newPath == oldPath {
		return nil
	}
	for n := 1; ; n++ {
		if _, err := os.Stat(newPath); errors.Is(err, os.ErrNotExist) {
			break
		}
		stem := strings.TrimSuffix(r.FileName, ".bru")
		newPath = filepath.Join(newDir, fmt.Sprintf("%s-%d.bru", stem, n))
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	r.Path = filepath.ToSlash(mustRel(c.Root, newPath))
	r.Folder = folder
	r.FileName = filepath.Base(newPath)
	return nil
}

// AddFolder creates a directory with folder.bru naming it.
func (c *Collection) AddFolder(parent, name string) (*Folder, error) {
	dirName := strings.TrimSuffix(bruFileName(name), ".bru")
	rel := name
	if parent != "" {
		rel = parent + "/" + dirName
	} else {
		rel = dirName
	}
	if err := os.MkdirAll(filepath.Join(c.Root, filepath.FromSlash(rel)), 0o755); err != nil {
		return nil, err
	}
	f := &Folder{Path: rel, Name: name, Bru: &bru.CollectionBru{Meta: bru.Meta{Name: name}}}
	data := []byte(bru.SerializeCollection(f.Bru))
	if err := writeFileAtomic(filepath.Join(c.Root, filepath.FromSlash(rel), "folder.bru"), data); err != nil {
		return nil, err
	}
	c.Folders = append(c.Folders, f)
	return f, nil
}

// DeleteFolder removes the directory recursively and unregisters its
// subtree.
func (c *Collection) DeleteFolder(f *Folder) error {
	if err := os.RemoveAll(filepath.Join(c.Root, filepath.FromSlash(f.Path))); err != nil {
		return err
	}
	prefix := f.Path + "/"
	var keep []*BruRequest
	for _, r := range c.Requests {
		if !strings.HasPrefix(r.Path, prefix) && r.Folder != f.Path {
			keep = append(keep, r)
		}
	}
	c.Requests = keep
	var keepF []*Folder
	for _, x := range c.Folders {
		if x != f && !strings.HasPrefix(x.Path, prefix) {
			keepF = append(keepF, x)
		}
	}
	c.Folders = keepF
	return nil
}

// SaveEnvironment writes the environment file.
func (c *Collection) SaveEnvironment(e *bru.Environment) error {
	dir := filepath.Join(c.Root, "environments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, bruFileName(e.Name))
	if err := c.guardSave("environments/" + bruFileName(e.Name)); err != nil {
		return err
	}
	return writeFileAtomic(path, withLineEnding(bru.SerializeEnvironment(e), e.CRLF))
}

// AddEnvironment creates a new empty environment.
func (c *Collection) AddEnvironment(name string) (*bru.Environment, error) {
	e := &bru.Environment{Name: name}
	if err := c.SaveEnvironment(e); err != nil {
		return nil, err
	}
	c.Environments = append(c.Environments, e)
	return e, nil
}

// SetActiveEnv persists the last-chosen environment in bruno.json.
func (c *Collection) SetActiveEnv(name string) error {
	c.ActiveEnv = name
	return c.updateBrunoJSON(func(o *orderedJSON) error {
		if name == "" {
			o.del("activeEnvironment")
			return nil
		}
		return o.set("activeEnvironment", name)
	})
}

// folderByPath returns the folder at rel or nil.
func (c *Collection) folderByPath(rel string) *Folder {
	for _, f := range c.Folders {
		if f.Path == rel {
			return f
		}
	}
	return nil
}

// varScopes returns variable dictionaries in Bruno's precedence order
// for a request: collection vars, folder vars outermost→innermost,
// environment vars, pre-request vars. Later wins.
func (c *Collection) varScopes(r *BruRequest) []bru.Dict {
	s := c.scopeSet(r)
	dicts := make([]bru.Dict, 0, 2+len(s.folders))
	dicts = append(dicts, s.collection)
	dicts = append(dicts, s.folders...)
	if s.env != nil {
		dicts = append(dicts, s.env)
	}
	if r != nil {
		dicts = append(dicts, s.request)
	}
	return dicts
}

// collectionHeaders returns collection-level headers for a request:
// collection.bru plus folder.bru files outermost→innermost. Later
// scopes override earlier ones per header name at send time.
func (c *Collection) collectionHeaders(r *BruRequest) []bru.Dict {
	var dicts []bru.Dict
	if c.Dot != nil {
		dicts = append(dicts, c.Dot.Headers)
	}
	if r != nil && r.Folder != "" {
		parts := strings.Split(r.Folder, "/")
		for i := range parts {
			f := c.folderByPath(strings.Join(parts[:i+1], "/"))
			if f != nil && f.Bru != nil {
				dicts = append(dicts, f.Bru.Headers)
			}
		}
	}
	return dicts
}

// loadDotEnv returns the collection's .env over the OS environment,
// re-reading only when the file's timestamp moved.
func (c *Collection) loadDotEnv() map[string]string {
	path := filepath.Join(c.Root, ".env")
	var mod time.Time
	if info, err := os.Stat(path); err == nil {
		mod = info.ModTime()
	}
	c.envMu.Lock()
	defer c.envMu.Unlock()
	if c.dotEnv != nil && c.dotEnvAt.Equal(mod) {
		return c.dotEnv
	}
	c.dotEnv = script.LoadDotEnv(path)
	c.dotEnvAt = mod
	return c.dotEnv
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
