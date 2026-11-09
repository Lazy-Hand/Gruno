package ui

// Smoke tests of the workbench via ui.NewTester: collection opening,
// tree selection, tab behavior and the request editor's sections.

import (
	"os"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"gruno/bru"
)

func openedApp(t *testing.T) *app {
	t.Helper()
	c, err := CreateCollection(filepath.Join(t.TempDir(), "api"), "api")
	if err != nil {
		t.Fatal(err)
	}
	a := newApp()
	a.coll = c
	return a
}

func TestWelcomeView(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 900, 600)
	if !tt.HasText("Bruno") || !tt.HasText("打开集合…") {
		t.Fatalf("welcome missing: %q", tt.Texts())
	}
}

func TestTreeSelectsAndOpens(t *testing.T) {
	a := openedApp(t)
	if _, err := a.coll.AddRequest("", "Get Users"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.coll.AddRequest("", "Create User"); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Get Users") || !tt.HasText("Create User") {
		t.Fatalf("tree missing requests: %q", tt.Texts())
	}
	if err := tt.Click("Get Users"); err != nil {
		t.Fatal(err)
	}
	if a.active == "" {
		t.Fatal("click should open a tab")
	}
	// The request line shows method select and send button.
	if !tt.HasText("发送") {
		t.Fatalf("send button missing: %q", tt.Texts())
	}
}

func TestSectionSwitching(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	a.openTab(br)
	tt := ui.NewTester(a.view, 1200, 800)
	for _, tab := range []string{"Params", "Body", "Headers", "Auth", "Vars", "Script", "Tests", "Assert", "Docs"} {
		if err := tt.Click(tab); err != nil {
			t.Fatalf("tab %s: %v", tab, err)
		}
	}
}

func TestVariablesCompleteAndShowAsChips(t *testing.T) {
	a := openedApp(t)
	env, err := a.coll.AddEnvironment("Local")
	if err != nil {
		t.Fatal(err)
	}
	env.Vars = bru.Dict{{Key: "baseURL", Value: "http://x", Enabled: true}}
	if err := a.coll.SetActiveEnv("Local"); err != nil {
		t.Fatal(err)
	}
	a.envSel = "Local"
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = "https://api"
	br.Req.VarsPreRequest = bru.Dict{{Key: "rid", Value: "1", Enabled: true}}
	a.openTab(br)

	// Every scope a send resolves against is offered.
	names := map[string]string{}
	for _, v := range a.varEntries(br) {
		names[v.Name] = v.Scope
	}
	if names["baseURL"] != "环境" || names["rid"] != "请求" {
		t.Fatalf("entries %v", names)
	}

	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("URL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyRight)
	tt.Type("/{{ba")
	tt.Frame()
	if !tt.HasText("baseURL") {
		t.Fatalf("no suggestion: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if want := "https://api/{{baseURL}}"; br.Req.URL != want {
		t.Fatalf("url %q, want %q", br.Req.URL, want)
	}
	if a.tabState(br).sending {
		t.Fatal("Enter completed the variable and also sent")
	}

	// Elsewhere, the URL shows the variable as a chip.
	if err := tt.Click("Params"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText(br.Req.URL) || !tt.HasText("baseURL") || !tt.HasText("https://api/") {
		t.Fatalf("no chip: %q", tt.Texts())
	}

	// A Params value cell completes the same way.
	br.Req.ParamsQuery = bru.Dict{{Key: "q", Value: "x", Enabled: true}}
	tt.Frame()
	if err := tt.Click("变量值"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyRight)
	tt.Type("{{ri")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if want := "x{{rid}}"; br.Req.ParamsQuery[0].Value != want {
		t.Fatalf("param value %q, want %q", br.Req.ParamsQuery[0].Value, want)
	}
	// The completion reaches the URL and the file, as typing does.
	if want := "https://api/{{baseURL}}?q=x{{rid}}"; br.Req.URL != want {
		t.Fatalf("url not synced from params: %q, want %q", br.Req.URL, want)
	}
	data, err := os.ReadFile(filepath.Join(a.coll.Root, br.Path))
	if err != nil || !strings.Contains(string(data), "q: x{{rid}}") || !strings.Contains(string(data), "url: https://api/{{baseURL}}?q=x{{rid}}") {
		t.Fatalf("not saved: %v\n%s", err, data)
	}
	// The field kept the focus to type on.
	if !tt.Focused("变量值") {
		t.Fatal("completion lost the focus")
	}
	tt.Type("-1")
	tt.Frame()
	if want := "x{{rid}}-1"; br.Req.ParamsQuery[0].Value != want {
		t.Fatalf("typing after completion: %q", br.Req.ParamsQuery[0].Value)
	}
}

func TestSendRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = srv.URL + "/ping"
	if err := a.coll.SaveRequest(br); err != nil {
		t.Fatal(err)
	}
	a.openTab(br)
	ts := a.tabState(br)
	a.send(br, ts)
	waitSent(t, ts)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.err != nil {
		t.Fatal(ts.err)
	}
	if ts.head == nil || ts.head.StatusCode != 200 {
		t.Fatalf("head: %+v", ts.head)
	}
	if ts.result == nil || !strings.Contains(ts.result.BodyText, "ok") {
		t.Fatalf("result: %+v", ts.result)
	}
}

func TestEnvPickerListsEnvironments(t *testing.T) {
	a := openedApp(t)
	if _, err := a.coll.AddEnvironment("Local"); err != nil {
		t.Fatal(err)
	}
	a.coll.SetActiveEnv("Local")
	a.envSel = "Local"
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Local") {
		t.Fatalf("env not shown: %q", tt.Texts())
	}
}

func TestLayoutToggle(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "Req")
	a.openTab(br)

	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("api") {
		t.Fatalf("collection header missing name: %q", tt.Texts())
	}

	// Layout starts horizontal (side-by-side)
	if !a.layoutHorizontal {
		t.Fatal("expected layoutHorizontal to default to true")
	}

	// Click layout toggle via accessibility label
	if err := tt.Click("切换面板布局"); err != nil {
		t.Fatal(err)
	}
	if a.layoutHorizontal {
		t.Fatal("expected layoutHorizontal to be toggled to false")
	}
}

func TestBeautifyJSON(t *testing.T) {
	raw := `{"name":"bruno","version":1}`
	formatted := beautifyJSON(raw)
	expected := "{\n  \"name\": \"bruno\",\n  \"version\": 1\n}"
	if formatted != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, formatted)
	}
}

func TestRecentCollectionsWelcome(t *testing.T) {
	a := newApp()
	a.recentDirs = []string{"/tmp/my-bruno-api"}
	tt := ui.NewTester(a.view, 900, 600)
	if !tt.HasText("最近打开的集合") || !tt.HasText("my-bruno-api") {
		t.Fatalf("recent collection missing from welcome: %q", tt.Texts())
	}
}

func TestPathParamsSyncAndBreadcrumb(t *testing.T) {
	a := openedApp(t)
	f, err := a.coll.AddFolder("", "users")
	if err != nil {
		t.Fatal(err)
	}
	br, err := a.coll.AddRequest(f.Path, "Get User Detail")
	if err != nil {
		t.Fatal(err)
	}
	br.Req.URL = "https://api.example.com/users/:id/posts/:postId?active=1"
	extractQueryToParams(br.Req)
	if len(br.Req.ParamsPath) != 2 || br.Req.ParamsPath[0].Key != "id" || br.Req.ParamsPath[1].Key != "postId" {
		t.Fatalf("expected path params [id, postId], got: %+v", br.Req.ParamsPath)
	}
	a.openTab(br)
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("users") || !tt.HasText("Get User Detail") {
		t.Fatalf("breadcrumb missing folder or request name: %q", tt.Texts())
	}
	if !tt.HasText("Path Variables (:name)") {
		t.Fatalf("path variables table missing: %q", tt.Texts())
	}
}

func TestGraphQLAndDocsPreview(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "GraphQL Query")
	br.Req.Body = "graphql"
	br.Req.BodyGraphQL = "query { users { id } }"
	br.Req.BodyGraphQLVars = `{"limit": 10}`
	br.Req.Docs = "# User API\nReturns user list."
	a.openTab(br)

	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Body"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("QUERY") || !tt.HasText("VARIABLES (JSON)") {
		t.Fatalf("graphql dual editors missing: %q", tt.Texts())
	}

	if err := tt.Click("Docs"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("预览 Markdown"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("User API") {
		t.Fatalf("markdown preview missing heading: %q", tt.Texts())
	}
}

func TestCollectionSettingsAndThemeToggle(t *testing.T) {
	a := openedApp(t)
	a.initCollectionSettingsEditors()
	a.collSettingsOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("集合设置 — api") {
		t.Fatalf("collection settings modal not shown: %q", tt.Texts())
	}
	if err := tt.Click("保存并关闭"); err != nil {
		t.Fatal(err)
	}
	if a.collSettingsOpen {
		t.Fatal("collection settings modal should close")
	}

	// Status bar theme toggle
	if err := tt.Click("切换主题: 跟随系统"); err != nil {
		t.Fatal(err)
	}
	if a.themeMode != "light" {
		t.Fatalf("expected themeMode=light, got %s", a.themeMode)
	}
}

func TestBrunoAppIconGeneration(t *testing.T) {
	out := filepath.Join(t.TempDir(), "icon.png")
	if err := writeBrunoAppIconPNG(out); err != nil {
		t.Fatal(err)
	}
}

func TestResponsePrettyRawAndTimeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "JSON Test")
	br.Req.URL = srv.URL + "/json"
	a.openTab(br)
	ts := a.tabState(br)
	a.send(br, ts)
	waitSent(t, ts)

	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Pretty") || !tt.HasText("Raw") || !tt.HasText("Timeline") {
		t.Fatalf("response controls missing: %q", tt.Texts())
	}
	// Default Pretty formats JSON with indentation
	if !strings.Contains(ts.respBuf.String(), "\n  \"hello\": \"world\"") {
		t.Fatalf("expected pretty JSON in respBuf, got: %q", ts.respBuf.String())
	}
	if err := tt.Click("Timeline"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("* 接收完成: 17 B") && !strings.Contains(strings.Join(tt.Texts(), " "), "接收完成") {
		t.Fatalf("timeline details missing: %q", tt.Texts())
	}
}

func waitSent(t *testing.T, ts *tabState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ts.mu.Lock()
		done := !ts.sending
		ts.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("send did not finish")
}

func TestScriptsTestsAndAssertsRun(t *testing.T) {
	var gotAuth, gotTrace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTrace = r.URL.Query().Get("trace")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"t-123","items":[1,2,3]}`))
	}))
	defer srv.Close()

	a := openedApp(t)
	env, err := a.coll.AddEnvironment("dev")
	if err != nil {
		t.Fatal(err)
	}
	env.Vars = bru.Dict{{Key: "host", Value: srv.URL, Enabled: true}}
	a.coll.SetActiveEnv("dev")

	br, _ := a.coll.AddRequest("", "Login")
	br.Req.URL = "{{host}}/login"
	br.Req.ScriptPreRequest = `req.setHeader("Authorization", "Bearer " + (bru.getVar("token") || "none"));
req.setUrl(req.getUrl() + "?trace={{traceId}}");
bru.setVar("traceId", "abc");`
	br.Req.ScriptPostResponse = `bru.setVar("token", res.body.token); bru.setEnvVar("lastStatus", String(res.status)); console.log("got", res.body.items.length)`
	br.Req.Tests = `test("ok", () => expect(res.status).to.equal(200));
test("items", () => expect(res.body.items).to.have.lengthOf(4));`
	br.Req.Assert = bru.Dict{{Key: "res.body.token", Value: "startsWith t-", Enabled: true}}
	if err := a.coll.SaveRequest(br); err != nil {
		t.Fatal(err)
	}
	a.openTab(br)
	ts := a.tabState(br)

	a.send(br, ts)
	waitSent(t, ts)
	a.applyScriptVars(ts)
	if ts.err != nil || ts.result == nil || ts.result.ErrorCode != "" {
		t.Fatalf("send: err=%v result=%+v", ts.err, ts.result)
	}
	if gotAuth != "Bearer none" || gotTrace != "abc" {
		t.Fatalf("server saw auth=%q trace=%q", gotAuth, gotTrace)
	}
	r := ts.script
	if len(r.Tests) != 2 || !r.Tests[0].Passed || r.Tests[1].Passed || len(r.Asserts) != 1 || !r.Asserts[0].Passed {
		t.Fatalf("results %+v", r)
	}
	if tab := testsTab(ui.LightTheme(), r); tab.Badge != "2/3" || tab.BadgeColor != ui.LightTheme().Danger {
		t.Fatalf("tests tab %+v", tab)
	}
	if v, _ := env.Vars.Get("lastStatus"); v != "200" {
		t.Fatalf("env var not applied: %+v", env.Vars)
	}

	// Runtime variables carry into the next send.
	a.send(br, ts)
	waitSent(t, ts)
	a.applyScriptVars(ts)
	if gotAuth != "Bearer t-123" {
		t.Fatalf("second send auth %q", gotAuth)
	}

	ts.respSection = 3
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Tests", "2/3", "items", "got 3"} {
		if !tt.HasText(want) {
			t.Fatalf("tests pane missing %q: %q", want, tt.Texts())
		}
	}
}

func TestPreRequestScriptErrorStopsSend(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = srv.URL
	br.Req.ScriptPreRequest = "throw new Error('boom')"
	a.openTab(br)
	ts := a.tabState(br)
	a.send(br, ts)
	waitSent(t, ts)
	if hit {
		t.Fatal("request was sent despite script error")
	}
	if ts.result == nil || ts.result.ErrorCode != "script" || !strings.Contains(ts.result.ErrorMessage, "boom") {
		t.Fatalf("result %+v", ts.result)
	}
}
