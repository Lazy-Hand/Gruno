package ui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"gruno/bru"
	"gruno/internal/secrets"
	"gruno/internal/send"
)

// Deleting a table row must not shift indexes under the render loop.
func TestDeleteTableRowKeepsRendering(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.Headers = bru.Dict{
		{Key: "A", Value: "1", Enabled: true},
		{Key: "B", Value: "2", Enabled: true},
		{Key: "C", Value: "3", Enabled: true},
	}
	a.openTab(br)
	ts := a.tabState(br)
	ts.section = 2 // Headers
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("删除行"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(br.Req.Headers) != 2 {
		t.Fatalf("headers after delete: %+v", br.Req.Headers)
	}
	if br.Req.Headers[0].Key != "B" || br.Req.Headers[1].Key != "C" {
		t.Fatalf("deleted the wrong row: %+v", br.Req.Headers)
	}
}

// A failed open must not leave tabs pointing at the previous collection:
// saving then dereferenced a nil collection.
func TestFailedOpenClearsTabs(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	a.openTab(br)
	tt := ui.NewTester(a.view, 900, 600)
	tt.Frame()
	a.openCollection(filepath.Join(t.TempDir(), "missing"))
	if a.coll != nil || len(a.open) != 0 || a.active != "" {
		t.Fatalf("stale state: coll=%v open=%d active=%q", a.coll, len(a.open), a.active)
	}
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
}

// A file that failed to parse is never overwritten with defaults.
func TestParseFailureBlocksOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	c, err := CreateCollection(dir, "api")
	if err != nil {
		t.Fatal(err)
	}
	bad := "this line is not a block\n"
	if err := os.WriteFile(filepath.Join(dir, "collection.bru"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := c.AddFolder("", "f")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.Path, "folder.bru"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	envDir := filepath.Join(dir, "environments")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "Bad.bru"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	re, err := OpenCollection(dir)
	if err != nil {
		t.Fatal(err)
	}
	re.Dot.Headers = bru.Dict{{Key: "X", Value: "1", Enabled: true}}
	if err := re.SaveCollectionBru(); err == nil {
		t.Fatal("collection.bru written despite parse failure")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "collection.bru")); string(data) != bad {
		t.Fatalf("collection.bru changed: %q", data)
	}
	rf := re.folderByPath(f.Path)
	if rf == nil {
		t.Fatal("folder missing")
	}
	rf.Bru = &bru.CollectionBru{Auth: "none", Vars: bru.Dict{{Key: "a", Value: "b", Enabled: true}}}
	if err := re.SaveFolderBru(rf); err == nil {
		t.Fatal("folder.bru written despite parse failure")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, f.Path, "folder.bru")); string(data) != bad {
		t.Fatalf("folder.bru changed: %q", data)
	}
	if _, err := re.AddEnvironment("Bad"); err == nil {
		t.Fatal("environment written despite parse failure")
	}
	if data, _ := os.ReadFile(filepath.Join(envDir, "Bad.bru")); string(data) != bad {
		t.Fatalf("environment changed: %q", data)
	}
}

func TestCloneRequestIsDeep(t *testing.T) {
	c := newCollection(t)
	r, _ := c.AddRequest("", "R")
	r.Req.Headers = bru.Dict{{Key: "A", Value: "1", Enabled: true}}
	dup, err := c.CloneRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	dup.Req.Headers[0].Value = "changed"
	dup.Req.URL = "https://clone"
	if r.Req.Headers[0].Value != "1" || r.Req.URL == "https://clone" {
		t.Fatalf("clone aliases the original: %+v", r.Req)
	}
}

// Query values extracted from a URL are logical values, escaped once on
// the way out.
func TestQueryExtractionEncodesOnce(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = "https://api.example.com/search?q=a%20b&x=1"
	extractQueryToParams(br.Req)
	if got := br.Req.ParamsQuery[0].Value; got != "a b" {
		t.Fatalf("decoded value: %q", got)
	}
	code, err := send.GenerateCode(a.coll.sendInputOf(br), "cURL")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(code, "%2520") || !strings.Contains(code, "q=a+b") {
		t.Fatalf("double encoding: %s", code)
	}
}

func TestSavePreservesCRLF(t *testing.T) {
	c := newCollection(t)
	r, _ := c.AddRequest("", "R")
	r.CRLF = true
	r.Req.URL = "https://crlf.example"
	if err := c.SaveRequest(r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(r.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\r\n") {
		t.Fatalf("CRLF lost: %q", data)
	}
	// Round-trips through the parser too.
	re, err := OpenCollection(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !re.Requests[0].CRLF {
		t.Fatal("CRLF flag not detected")
	}
}

func TestFolderSettingsModal(t *testing.T) {
	a := openedApp(t)
	f, err := a.coll.AddFolder("", "users")
	if err != nil {
		t.Fatal(err)
	}
	a.openFolderSettings(f)
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("目录设置 — users") {
		t.Fatalf("folder settings missing: %q", tt.Texts())
	}
	if err := tt.Click("保存并关闭"); err != nil {
		t.Fatal(err)
	}
	if a.folderSettingsOpen {
		t.Fatal("folder settings should close")
	}
}

// Editor writes are debounced, then flushed on a frame once due.
func TestDebouncedSaveFlushes(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	a.openTab(br)
	tt := ui.NewTester(a.view, 900, 600)
	tt.Frame()
	br.Req.URL = "https://debounced.example"
	a.saveSoon(br)
	time.Sleep(saveDebounce + 150*time.Millisecond)
	tt.Frame()
	data, err := os.ReadFile(filepath.Join(a.coll.Root, filepath.FromSlash(br.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "debounced.example") {
		t.Fatalf("not flushed: %s", data)
	}
}

func TestUnsupportedBodySendFailsClearly(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = "http://127.0.0.1:1/x"
	br.Req.Body = "file"
	a.openTab(br)
	ts := a.tabState(br)
	a.send(br, ts)
	waitSent(t, ts)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.result == nil || ts.result.ErrorCode != send.ErrInvalidReq || !strings.Contains(ts.result.ErrorMessage, "暂不支持") {
		t.Fatalf("result: %+v", ts.result)
	}
}

func TestOAuth2AuthorizeSpecMapping(t *testing.T) {
	d := bru.Dict{
		{Key: "grant_type", Value: "authorization_code", Enabled: true},
		{Key: "authorization_url", Value: "https://auth.example/a", Enabled: true},
		{Key: "callback_url", Value: "http://127.0.0.1:9999/cb", Enabled: true},
		{Key: "access_token_url", Value: "https://auth.example/t", Enabled: true},
	}
	spec := oauth2AuthSpec(d)
	if spec.OAuth2 == nil || spec.OAuth2.AuthorizationURL != "https://auth.example/a" ||
		spec.OAuth2.CallbackURL != "http://127.0.0.1:9999/cb" || !spec.OAuth2.PKCE {
		t.Fatalf("authorization_code spec: %+v", spec.OAuth2)
	}
	d = append(d, bru.Entry{Key: "pkce", Value: "false", Enabled: true})
	if oauth2AuthSpec(d).OAuth2.PKCE {
		t.Fatal("pkce=false ignored")
	}
}

func TestSendSettingsReadEncodeURL(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.Settings = bru.Dict{{Key: "encodeUrl", Value: "false", Enabled: true}}
	if st := a.sendSettings(br); !st.NoEncodeURL {
		t.Fatalf("encodeUrl not applied: %+v", st)
	}
	br.Req.Settings = bru.Dict{{Key: "encodeUrl", Value: "true", Enabled: true}}
	if st := a.sendSettings(br); st.NoEncodeURL {
		t.Fatalf("encodeUrl=true inverted: %+v", st)
	}
}

func TestSecretReadFailureNotCached(t *testing.T) {
	a := openedApp(t)
	path := filepath.Join(t.TempDir(), "secrets.json")
	key := bytes.Repeat([]byte{7}, 32)
	seed := secrets.New(path, func() ([]byte, error) { return key, nil })
	if err := seed.Set(a.coll.Root, "Dev", map[string]string{"tok": "v1"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	broken := secrets.New(path, func() ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("keychain down")
		}
		return key, nil
	})
	a.secrets = broken
	env := &bru.Environment{Name: "Dev", Secrets: []string{"tok"}}
	a.coll.Environments = append(a.coll.Environments, env)

	if vals := a.envSecretValues(env); len(vals) != 0 || a.secretErr == "" {
		t.Fatalf("first read: %v %q", vals, a.secretErr)
	}
	vals := a.envSecretValues(env)
	if a.secretErr != "" {
		t.Fatalf("failure cached: %q", a.secretErr)
	}
	if vals["tok"] != "v1" {
		t.Fatalf("retry values: %+v", vals)
	}
}

func TestTemplatedURLKeepsPathParams(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = "{{baseUrl}}"
	br.Req.ParamsPath = bru.Dict{{Key: "id", Value: "1", Enabled: true}}
	syncPathParamsFromURL(br.Req)
	if len(br.Req.ParamsPath) != 1 {
		t.Fatalf("path params cleared: %+v", br.Req.ParamsPath)
	}
}

func TestProcessEnvLookup(t *testing.T) {
	a := openedApp(t)
	if _, ok := a.lookupVar(nil, "process.env.GRUNO_TEST_MISSING"); ok {
		t.Fatal("missing process.env name reported defined")
	}
	if err := os.WriteFile(filepath.Join(a.coll.Root, ".env"), []byte("GRUNO_TEST_KEY=v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, ok := a.lookupVar(nil, "process.env.GRUNO_TEST_KEY")
	if !ok || e.Value != "v" {
		t.Fatalf("process.env entry: %+v %v", e, ok)
	}
}

func TestVarEntriesCachedPerPass(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	a.varPass = map[string][]varEntry{}
	v1 := a.varEntries(br)
	v2 := a.varEntries(br)
	if reflect.ValueOf(v1).Pointer() != reflect.ValueOf(v2).Pointer() {
		t.Fatal("varEntries rebuilt within one pass")
	}
}

func TestCutUTF8(t *testing.T) {
	s := strings.Repeat("é", 100)
	out := cutUTF8(s, 101)
	if len(out) > 101 || !utf8.ValidString(out) {
		t.Fatalf("cut: %d bytes %q", len(out), out)
	}
}

func TestParseGuardSurfacesSaveError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	if _, err := CreateCollection(dir, "api"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "collection.bru"), []byte("bad line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	re, err := OpenCollection(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := newApp()
	a.coll = re
	a.saveCollection()
	if a.saveErr == "" {
		t.Fatal("save error not surfaced")
	}
}

// A long collection name ends in an ellipsis on one line.
func TestLongCollectionNameStaysOneLine(t *testing.T) {
	a := openedApp(t)
	a.coll.Name = strings.Repeat("接口文档", 20)
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	name, ok := tt.Find(a.coll.Name)
	if !ok {
		t.Fatalf("collection name not shown: %q", tt.Texts())
	}
	if name.H > 30 {
		t.Fatalf("collection name wraps: height %v", name.H)
	}
}

// Crowded tabs scroll sideways instead of stacking and stretching the
// tab bar.
func TestTabBarHeightStableWithManyTabs(t *testing.T) {
	a := openedApp(t)
	first, err := a.coll.AddRequest("", "R0")
	if err != nil {
		t.Fatal(err)
	}
	a.openTab(first)
	tt := ui.NewTester(a.view, 700, 700)
	tt.Frame()
	base, ok := tt.Find("关闭标签")
	if !ok {
		t.Fatalf("tab close button missing: %q", tt.Texts())
	}
	for i := 0; i < 20; i++ {
		r, err := a.coll.AddRequest("", fmt.Sprintf("%s %d", strings.Repeat("请求名称", 6), i))
		if err != nil {
			t.Fatal(err)
		}
		a.openTab(r)
	}
	tt.Frame()
	got, ok := tt.Find("关闭标签")
	if !ok {
		t.Fatalf("tab close button missing after opening many tabs: %q", tt.Texts())
	}
	if got.Y > 40 || got.Y-base.Y > 2 {
		t.Fatalf("tab bar grew with many tabs: y %.1f -> %.1f", base.Y, got.Y)
	}
}

// The tab strip scrolls sideways with a wheel or a vertical trackpad move,
// and shows no scroll bar to drag.
func TestTabStripScrollsWithWheel(t *testing.T) {
	a := openedApp(t)
	for i := 0; i < 12; i++ {
		r, err := a.coll.AddRequest("", fmt.Sprintf("%s %d", strings.Repeat("请求名称", 6), i))
		if err != nil {
			t.Fatal(err)
		}
		a.openTab(r)
	}
	tt := ui.NewTester(a.view, 700, 700)
	tt.Frame()
	before, ok := tt.Find("关闭标签")
	if !ok {
		t.Fatalf("tab close button missing: %q", tt.Texts())
	}
	tt.Wheel(350, 15, 0, 120)
	tt.Frame()
	after, ok := tt.Find("关闭标签")
	if !ok {
		t.Fatal("tab close button missing after scroll")
	}
	if after.X >= before.X {
		t.Fatalf("wheel did not scroll the tab strip: x %.1f -> %.1f", before.X, after.X)
	}
	tt.Wheel(350, 15, 0, -120)
	tt.Frame()
	back, _ := tt.Find("关闭标签")
	if back.X < after.X {
		t.Fatalf("wheel did not scroll back: x %.1f -> %.1f", after.X, back.X)
	}
}
