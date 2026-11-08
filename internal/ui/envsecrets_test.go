package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"gruno/bru"
	"gruno/internal/secrets"
)

func testSecrets(t *testing.T) *secrets.Store {
	path := filepath.Join(t.TempDir(), "env-secrets.json")
	return secrets.New(path, func() ([]byte, error) { return bytes.Repeat([]byte{1}, 32), nil })
}

func TestEnvSecretsSaveSendAndScriptPersist(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"next":"rotated-2"}`))
	}))
	defer srv.Close()

	store := testSecrets(t)
	a := openedApp(t)
	a.secrets = store
	env, err := a.coll.AddEnvironment("dev")
	if err != nil {
		t.Fatal(err)
	}
	a.coll.SetActiveEnv("dev")

	a.beginEnvEdit(env)
	a.envRows = append(a.envRows,
		envRow{Key: "host", Value: srv.URL, Enabled: true},
		envRow{Key: "token", Value: "s3cret-1", Enabled: true, Secret: true},
		envRow{Key: "unused", Value: "x", Enabled: false, Secret: true},
	)
	if err := a.saveEnvEdit(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(a.coll.Root, "environments", "dev.bru"))
	if strings.Contains(string(data), "s3cret") || !strings.Contains(string(data), "vars:secret [\n  token\n  ~unused\n]") {
		t.Fatalf("env file:\n%s", data)
	}

	// A fresh app on the same store reads the values back.
	b := newApp()
	b.secrets = store
	b.openCollection(a.coll.Root)
	b.coll.SetActiveEnv("dev")
	benv := b.coll.activeEnvironment()
	if got := b.envSecretValues(benv); got["token"] != "s3cret-1" || got["unused"] != "x" {
		t.Fatalf("values %v", got)
	}
	b.beginEnvEdit(benv)
	if r := b.envRows[1]; r.Key != "token" || r.Value != "s3cret-1" || !r.Secret || !r.Enabled {
		t.Fatalf("rows %+v", b.envRows)
	}
	if r := b.envRows[2]; r.Enabled {
		t.Fatalf("disabled secret %+v", r)
	}

	br, _ := b.coll.AddRequest("", "R")
	br.Req.URL = "{{host}}/x"
	br.Req.Headers = bru.Dict{{Key: "Authorization", Value: "Bearer {{token}}", Enabled: true}}
	br.Req.ScriptPostResponse = `bru.setEnvVar("token", res.body.next, {persist: true})`
	b.openTab(br)
	ts := b.tabState(br)
	b.send(br, ts)
	waitSent(t, ts)
	b.applyScriptVars(ts)
	if gotAuth != "Bearer s3cret-1" {
		t.Fatalf("auth %q (result %+v)", gotAuth, ts.result)
	}
	if v, ok := benv.Vars.Get("token"); ok {
		t.Fatalf("secret leaked into plain vars: %q", v)
	}
	got, err := store.Get(b.coll.Root, "dev")
	if err != nil || got["token"] != "rotated-2" {
		t.Fatalf("persisted %v %v", got, err)
	}
	data, _ = os.ReadFile(filepath.Join(b.coll.Root, "environments", "dev.bru"))
	if strings.Contains(string(data), "rotated") {
		t.Fatalf("value written to env file:\n%s", data)
	}

	// Turning a secret back into a plain variable moves its value to the file.
	b.beginEnvEdit(benv)
	b.envRows[1].Secret = false
	if err := b.saveEnvEdit(); err != nil {
		t.Fatal(err)
	}
	if v, _ := benv.Vars.Get("token"); v != "rotated-2" {
		t.Fatalf("plain var %q", v)
	}
	if got, _ := store.Get(b.coll.Root, "dev"); got["token"] != "" {
		t.Fatalf("store still has token: %v", got)
	}
}

func TestEnvEditorShowsSecretToggle(t *testing.T) {
	a := openedApp(t)
	a.secrets = testSecrets(t)
	env, _ := a.coll.AddEnvironment("dev")
	a.beginEnvEdit(env)
	a.envRows = append(a.envRows, envRow{Key: "token", Value: "v", Enabled: true, Secret: true})
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Secret") || !tt.HasText("环境配置: dev") {
		t.Fatalf("editor: %q", tt.Texts())
	}
}
