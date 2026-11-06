package ui

import (
	"gruno/bru"
	"gruno/internal/send"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCollection(t *testing.T) *Collection {
	t.Helper()
	c, err := CreateCollection(filepath.Join(t.TempDir(), "api"), "api")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateAndOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	c, err := CreateCollection(dir, "api")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "api" {
		t.Fatal(c.Name)
	}
	if _, err := os.Stat(filepath.Join(dir, "bruno.json")); err != nil {
		t.Fatal("bruno.json missing")
	}
	// Re-open reads the same collection.
	c2, err := OpenCollection(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Name != "api" {
		t.Fatal(c2.Name)
	}
}

func TestAddSaveRoundTrip(t *testing.T) {
	c := newCollection(t)
	r, err := c.AddRequest("", "Get Users")
	if err != nil {
		t.Fatal(err)
	}
	r.Req.Method = "post"
	r.Req.URL = "{{host}}/users"
	r.Req.Headers = append(r.Req.Headers, bru.Entry{Key: "Accept", Value: "application/json", Enabled: true})
	if err := c.SaveRequest(r); err != nil {
		t.Fatal(err)
	}
	c2, err := OpenCollection(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Requests) != 1 {
		t.Fatalf("requests: %d", len(c2.Requests))
	}
	got := c2.Requests[0].Req
	if got.Meta.Name != "Get Users" || got.Method != "post" || got.URL != "{{host}}/users" {
		t.Fatalf("round trip: %+v", got)
	}
	if v, _ := got.Headers.Get("Accept"); v != "application/json" {
		t.Fatalf("headers: %v", got.Headers)
	}
}

func TestNestedFolderAndVars(t *testing.T) {
	c := newCollection(t)
	f1, err := c.AddFolder("", "users")
	if err != nil {
		t.Fatal(err)
	}
	f2, err := c.AddFolder(f1.Path, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if f2.Path != "users/admin" {
		t.Fatalf("nested path: %q", f2.Path)
	}
	// folder.bru vars on each level.
	f1.Bru.Vars = bru.Dict{{Key: "scope", Value: "users", Enabled: true}}
	os.WriteFile(filepath.Join(c.Root, f1.Path, "folder.bru"), []byte(bru.SerializeCollection(f1.Bru)), 0o644)
	f2.Bru.Vars = bru.Dict{{Key: "scope", Value: "admin", Enabled: true}}
	os.WriteFile(filepath.Join(c.Root, f2.Path, "folder.bru"), []byte(bru.SerializeCollection(f2.Bru)), 0o644)

	r, err := c.AddRequest(f2.Path, "List")
	if err != nil {
		t.Fatal(err)
	}
	r.Req.VarsPreRequest = bru.Dict{{Key: "req", Value: "1", Enabled: true}}

	c2, err := OpenCollection(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	req := c2.Requests[0]
	scopes := c2.varScopes(req)
	// collection vars (empty), users vars, admin vars, env (none), req vars.
	if len(scopes) != 4 {
		t.Fatalf("scopes: %d", len(scopes))
	}
	if v, _ := scopes[1].Get("scope"); v != "users" {
		t.Fatalf("outer folder vars: %v", scopes[1])
	}
	if v, _ := scopes[2].Get("scope"); v != "admin" {
		t.Fatalf("inner folder vars: %v", scopes[2])
	}
}

func TestDeleteAndRename(t *testing.T) {
	c := newCollection(t)
	r, _ := c.AddRequest("", "A")
	path := filepath.Join(c.Root, r.Path)
	if err := c.RenameRequest(r, "B"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "name: B") {
		t.Fatal("rename not persisted")
	}
	if err := c.DeleteRequest(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
	if len(c.Requests) != 0 {
		t.Fatal("still registered")
	}
}

func TestMoveRequest(t *testing.T) {
	c := newCollection(t)
	f, _ := c.AddFolder("", "dest")
	r, _ := c.AddRequest("", "X")
	if err := c.MoveRequest(r, f.Path); err != nil {
		t.Fatal(err)
	}
	if r.Folder != f.Path || !strings.HasPrefix(r.Path, f.Path+"/") {
		t.Fatalf("moved: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(c.Root, r.Path)); err != nil {
		t.Fatal("file missing at new path")
	}
}

func TestEnvironmentLifecycle(t *testing.T) {
	c := newCollection(t)
	env, err := c.AddEnvironment("Local")
	if err != nil {
		t.Fatal(err)
	}
	env.Vars = bru.Dict{{Key: "host", Value: "http://localhost", Enabled: true}}
	if err := c.SaveEnvironment(env); err != nil {
		t.Fatal(err)
	}
	if err := c.SetActiveEnv("Local"); err != nil {
		t.Fatal(err)
	}
	c2, err := OpenCollection(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ActiveEnv != "Local" || len(c2.Environments) != 1 {
		t.Fatalf("env: %+v", c2)
	}
	if v, _ := c2.Environments[0].Vars.Get("host"); v != "http://localhost" {
		t.Fatalf("env vars: %v", c2.Environments[0].Vars)
	}
	// Env vars land in the resolution scopes.
	r, _ := c2.AddRequest("", "X")
	scopes := c2.varScopes(r)
	found := false
	for _, d := range scopes {
		if v, ok := d.Get("host"); ok && v == "http://localhost" {
			found = true
		}
	}
	if !found {
		t.Fatal("env var missing from scopes")
	}
}

func TestSendInputAdaptation(t *testing.T) {
	c := newCollection(t)
	c.Dot.Vars = bru.Dict{{Key: "host", Value: "https://h", Enabled: true}}
	c.Dot.Headers = bru.Dict{{Key: "X-From-Collection", Value: "1", Enabled: true}}
	r, _ := c.AddRequest("", "R")
	r.Req.Method = "post"
	r.Req.URL = "{{host}}/x"
	r.Req.Auth = "bearer"
	r.Req.AuthBearer = bru.Dict{{Key: "token", Value: "tok", Enabled: true}}
	r.Req.Body = "json"
	r.Req.BodyJSON = `{"a":1}`
	r.Req.Headers = bru.Dict{{Key: "X-From-Collection", Value: "req", Enabled: true}}

	in := c.sendInputOf(r)
	if in.Request.Method != "POST" || in.Request.URL != "{{host}}/x" {
		t.Fatalf("request: %+v", in.Request)
	}
	// The request's own X-From-Collection overrides the collection one:
	// only one entry should carry the name.
	count := 0
	for _, h := range in.Request.Headers {
		if h.Key == "X-From-Collection" {
			count++
			if h.Value != "req" {
				t.Fatalf("override lost: %v", h)
			}
		}
	}
	if count != 1 {
		t.Fatalf("header duplication: %v", in.Request.Headers)
	}
	if in.Request.Auth.Mode != send.AuthBearer || in.Request.Auth.Token != "tok" {
		t.Fatalf("auth: %+v", in.Request.Auth)
	}
	if in.Request.Body.Mode != send.BodyJSON || in.Request.Body.Text != `{"a":1}` {
		t.Fatalf("body: %+v", in.Request.Body)
	}
}

func TestInheritAuth(t *testing.T) {
	c := newCollection(t)
	c.Dot.Auth = "basic"
	c.Dot.AuthBasic = bru.Dict{{Key: "username", Value: "u", Enabled: true}, {Key: "password", Value: "p", Enabled: true}}
	r, _ := c.AddRequest("", "R")
	r.Req.Auth = "inherit"
	in := c.sendInputOf(r)
	if in.Request.Auth.Mode != send.AuthBasic || in.Request.Auth.Username != "u" {
		t.Fatalf("inherited auth: %+v", in.Request.Auth)
	}
}
