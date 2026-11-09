package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func waitRun(t *testing.T, rs *runnerState) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, running, _, _, _ := rs.snapshot(); !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
}

func TestRunnerFlowControl(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/login" {
			w.Write([]byte(`{"token":"t1"}`))
			return
		}
		w.Write([]byte(r.Header.Get("Authorization")))
	}))
	defer srv.Close()

	a := openedApp(t)
	add := func(folder, name, path, pre, post, tests string) {
		t.Helper()
		br, err := a.coll.AddRequest(folder, name)
		if err != nil {
			t.Fatal(err)
		}
		br.Req.URL = srv.URL + path
		br.Req.ScriptPreRequest, br.Req.ScriptPostResponse, br.Req.Tests = pre, post, tests
		if err := a.coll.SaveRequest(br); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.coll.AddFolder("", "auth"); err != nil {
		t.Fatal(err)
	}
	// Folders run first, as the sidebar lists them.
	add("auth", "Login", "/login", "", `bru.setVar("token", res.body.token); bru.setVar("n", 1)`, "")
	add("", "Me", "/me", `req.setHeader("Authorization", "Bearer " + bru.getVar("token"))`,
		`bru.setVar("n", bru.getVar("n") + 1); if (bru.getVar("n") < 3) bru.setNextRequest("Me")`,
		`test("auth", function () { expect(res.body).to.equal("Bearer t1") })`)
	add("", "Skipped", "/skip", `bru.runner.skipRequest()`, "", "")
	add("", "Fails", "/fail", "", `bru.runner.stopExecution()`, `test("nope", function () { expect(1).to.equal(2) })`)
	add("", "Never", "/never", "", "", "")

	a.openRunner("")
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("集合运行器") || !tt.HasText("开始运行") {
		t.Fatalf("runner view: %q", tt.Texts())
	}
	if err := tt.Click("开始运行"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, a.runner)
	results, _, _, _, note := a.runner.snapshot()
	var got []string
	for _, r := range results {
		got = append(got, r.Name+":"+outcomeLabel(r.Outcome))
	}
	want := "Login:通过 Me:通过 Me:通过 Skipped:跳过 Fails:失败"
	if strings.Join(got, " ") != want {
		t.Fatalf("results %q, want %q", strings.Join(got, " "), want)
	}
	if !strings.Contains(note, "stopExecution") {
		t.Fatalf("note %q", note)
	}
	if hits.Load() != 4 {
		t.Fatalf("hits %d", hits.Load())
	}
	// Runtime variables reach the app once the run is done.
	tt.Move(10, 10)
	n := ""
	for _, v := range a.runtimeVars {
		if v.Key == "n" {
			n = v.Value
		}
	}
	if n != "3" {
		t.Fatalf("runtime n = %q (%+v)", n, a.runtimeVars)
	}
	if !tt.HasText("重新运行") || !tt.HasText("1/1") {
		t.Fatalf("summary: %q", tt.Texts())
	}
	// Selecting a result shows its tests.
	a.runner.sel = 4
	tt.Move(12, 12)
	if !tt.HasText("nope") || !tt.HasText("响应") {
		t.Fatalf("detail: %q", tt.Texts())
	}
}

func TestRunnerFolderScopeAndStop(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	defer close(release)

	a := openedApp(t)
	if _, err := a.coll.AddFolder("", "f"); err != nil {
		t.Fatal(err)
	}
	for _, spec := range [][2]string{{"f", "/slow"}, {"f", "/b"}, {"", "/root"}} {
		br, _ := a.coll.AddRequest(spec[0], strings.TrimPrefix(spec[1], "/"))
		br.Req.URL = srv.URL + spec[1]
		a.coll.SaveRequest(br)
	}
	if got := len(a.runOrder("f")); got != 2 {
		t.Fatalf("folder order %d", got)
	}
	a.openRunner("f")
	a.startRun()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if res, _, _, _, _ := a.runner.snapshot(); len(res) == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.stopRun()
	waitRun(t, a.runner)
	results, _, _, _, note := a.runner.snapshot()
	if len(results) != 1 || results[0].Outcome != runError || note != "运行已停止" {
		t.Fatalf("results %+v note %q", results, note)
	}
	a.closeRunner()
	if a.runner != nil || a.active == runnerTab {
		t.Fatal("runner should close")
	}
}
