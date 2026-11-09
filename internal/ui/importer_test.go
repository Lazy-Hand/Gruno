package ui

import (
	"os"
	"path/filepath"
	"testing"

	"gruno/internal/importer"
)

func TestWriteImportRoundTrip(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "importer", "testdata", "shop.postman.json"))
	if err != nil {
		t.Fatal(err)
	}
	imp, err := importer.ParsePostman(data)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "shop")
	c, err := CreateCollection(root, imp.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeImport(c, imp); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk: what the app sees after the import.
	re, err := OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	if re.Name != "Shop API" {
		t.Fatalf("name %q", re.Name)
	}
	if re.Dot == nil || re.Dot.Auth != "bearer" {
		t.Fatalf("collection auth %+v", re.Dot)
	}
	if v, _ := re.Dot.Vars.Get("baseUrl"); v != "https://api.test" {
		t.Fatalf("collection vars %+v", re.Dot.Vars)
	}
	f := re.folderByPath("Users")
	if f == nil || f.Bru == nil || f.Bru.Auth != "basic" {
		t.Fatalf("folder %+v", f)
	}
	var create *BruRequest
	for _, r := range re.Requests {
		if r.Req.Meta.Name == "Create user" {
			create = r
		}
	}
	if create == nil {
		t.Fatalf("Create user missing; requests %d", len(re.Requests))
	}
	if create.Folder != "Users" || create.Req.Method != "post" || create.Req.BodyJSON != `{"name":"Ada"}` {
		t.Fatalf("create %+v", create.Req)
	}
	if len(re.Requests) != 4 {
		t.Fatalf("requests %d", len(re.Requests))
	}
	a := newApp()
	a.coll = re
	if n := len(a.runOrder("")); n != 4 {
		t.Fatalf("run order %d", n)
	}
}
