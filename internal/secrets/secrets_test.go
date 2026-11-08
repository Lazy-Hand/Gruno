package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixedKey(calls *int) KeySource {
	return func() ([]byte, error) {
		*calls++
		return bytes.Repeat([]byte{7}, 32), nil
	}
}

func TestRoundTripAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	calls := 0
	s := New(path, fixedKey(&calls))

	got, err := s.Get("/c1", "dev")
	if err != nil || len(got) != 0 || calls != 0 {
		t.Fatalf("empty get: %v %v calls=%d", got, err, calls)
	}
	long := strings.Repeat("x", 10000)
	if err := s.Set("/c1", "dev", map[string]string{"token": "abc", "jwt": long}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("/c2", "dev", map[string]string{"token": "other"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("abc")) || bytes.Contains(raw, []byte("/c1")) {
		t.Fatalf("plaintext leaked: %s", raw)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	s2 := New(path, fixedKey(&calls))
	got, err = s2.Get("/c1", "dev")
	if err != nil || got["token"] != "abc" || got["jwt"] != long {
		t.Fatalf("get: %v", err)
	}
	if got, _ := s2.Get("/c2", "dev"); got["token"] != "other" {
		t.Fatalf("c2: %v", got)
	}
	if got, _ := s2.Get("/c1", "prod"); len(got) != 0 {
		t.Fatalf("prod: %v", got)
	}

	if err := s2.Delete("/c1", "dev"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s2.Get("/c1", "dev"); len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestWrongKeyAndKeyError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	calls := 0
	if err := New(path, fixedKey(&calls)).Set("/c", "dev", map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	other := New(path, func() ([]byte, error) { return bytes.Repeat([]byte{8}, 32), nil })
	if _, err := other.Get("/c", "dev"); err == nil {
		t.Fatal("expected decrypt failure")
	}
	broken := New(path, func() ([]byte, error) { return nil, errors.ErrUnsupported })
	if _, err := broken.Get("/c", "dev"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
}
