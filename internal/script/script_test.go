package script

import (
	"context"
	"strings"
	"testing"
	"time"

	"gruno/internal/send"
)

func p(k, v string) send.Pair { return send.Pair{Key: k, Value: v, Enabled: true} }

func newReq() send.RequestData {
	return send.RequestData{
		Name: "Get user", Method: "GET", URL: "{{host}}/users/1",
		Query:   []send.Pair{p("a", "1")},
		Headers: []send.Pair{p("Accept", "application/json")},
		Body:    send.BodySpec{Mode: send.BodyJSON, Text: `{"x":1}`},
	}
}

func TestPreRequestMutatesRequestAndVars(t *testing.T) {
	vars := &Vars{Env: []send.Pair{p("host", "http://h")}, EnvName: "dev", Collection: []send.Pair{p("c", "cv")}}
	s := NewSession(vars)
	req := newReq()
	err := s.PreRequest(&req, []Script{
		{Label: "集合", Source: `const a = 1; bru.setVar("token", "abc"); bru.setVar("n", 2)`},
		{Label: "请求", Source: `const a = 2;
req.setHeader("Authorization", "Bearer " + bru.getVar("token"));
req.setUrl(req.getUrl() + "&b=2");
const body = req.getBody(); body.y = bru.getVar("n") + 1; req.setBody(body);
bru.setEnvVar("seen", req.getMethod());
console.log("url", req.url, {k: bru.getCollectionVar("c")});`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Headers[len(req.Headers)-1]; got.Key != "Authorization" || got.Value != "Bearer abc" {
		t.Fatalf("header %+v", got)
	}
	if req.URL != "{{host}}/users/1" || len(req.Query) != 2 || req.Query[1].Key != "b" {
		t.Fatalf("url %q query %+v", req.URL, req.Query)
	}
	if req.Body.Text != `{"x":1,"y":3}` {
		t.Fatalf("body %q", req.Body.Text)
	}
	if len(vars.EnvSets) != 1 || vars.EnvSets[0].Value != "GET" {
		t.Fatalf("env sets %+v", vars.EnvSets)
	}
	if len(s.Result.Logs) != 1 || !strings.Contains(s.Result.Logs[0].Text, `"k": "cv"`) {
		t.Fatalf("logs %+v", s.Result.Logs)
	}
	out, err := send.Interpolate("{{token}}-{{n}}-{{seen}}", vars.Scopes()...)
	if err != nil || out != "abc-2-GET" {
		t.Fatalf("interpolate %q %v", out, err)
	}
}

func TestPreRequestErrorStops(t *testing.T) {
	s := NewSession(&Vars{})
	req := newReq()
	err := s.PreRequest(&req, []Script{{Label: "请求", Source: "\n\nfoo.bar()"}})
	if err == nil || !strings.Contains(err.Error(), "第 3 行") {
		t.Fatalf("err %v", err)
	}
}

func TestTimeout(t *testing.T) {
	s := NewSession(&Vars{})
	s.Timeout = 50e6
	req := newReq()
	err := s.PreRequest(&req, []Script{{Label: "请求", Source: "while(true){}"}})
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("err %v", err)
	}
}

func TestPostResponseTestsAndAsserts(t *testing.T) {
	vars := &Vars{Env: []send.Pair{p("want", "200")}}
	s := NewSession(vars)
	res := Response{Status: 200, StatusText: "OK", Headers: []send.Pair{p("Content-Type", "application/json")},
		Body: `{"id":7,"name":"bob","tags":["a","b"]}`, DurationMs: 12}
	s.PostResponse(newReq(), res,
		[]Script{{Label: "请求", Source: `bru.setVar("uid", res.body.id)`}},
		[]send.Pair{p("uname", "res.body.name")},
		[]send.Pair{p("res.status", "eq {{want}}"), p("res.body.tags", "length 2"), p("res.body.name", `"bob"`),
			p("res.body.id", "in 1,7"), p("res.body.id", "gt 10")},
		[]Script{{Label: "请求", Source: `
test("status", function() { expect(res.status).to.equal(200); expect(res.getStatus()).to.eql(200); });
test("body", () => {
  expect(res.body).to.have.property("name", "bob");
  expect(res.body.tags).to.have.lengthOf(2).and.include("a");
  expect(res.body).to.include.keys("id");
  expect(res.body.tags).to.have.length.above(1);
  expect(res.getHeader("content-type")).to.match(/json/);
  expect(res("tags[1]")).to.be.a("string");
  expect(bru.getVar("uid")).to.equal(7);
  expect(bru.getVar("uname")).to.equal("bob");
  expect({a: {b: 1}}).to.deep.equal({a: {b: 1}});
  expect(null).to.not.exist;
});
test("fails", () => { expect(res.status, "status").to.equal(404); });
test("async", async () => { await bru.sleep(1); expect(1).to.be.ok; });
`}})
	if len(s.Result.Errors) != 0 {
		t.Fatalf("errors %+v", s.Result.Errors)
	}
	want := map[string]bool{"status": true, "body": true, "fails": false, "async": true}
	if len(s.Result.Tests) != 4 {
		t.Fatalf("tests %+v", s.Result.Tests)
	}
	for _, tr := range s.Result.Tests {
		if tr.Passed != want[tr.Name] {
			t.Errorf("test %q passed=%v err=%s", tr.Name, tr.Passed, tr.Error)
		}
	}
	if e := s.Result.Tests[2].Error; e != "status: expected 200 to equal 404" {
		t.Errorf("fail message %q", e)
	}
	passed := []bool{true, true, true, true, false}
	if len(s.Result.Asserts) != len(passed) {
		t.Fatalf("asserts %+v", s.Result.Asserts)
	}
	for i, a := range s.Result.Asserts {
		if a.Passed != passed[i] {
			t.Errorf("assert %q passed=%v err=%s", a.Name, a.Passed, a.Error)
		}
	}
	if vars.Runtime[0].Key != "uname" || vars.Runtime[1].Key != "uid" || !vars.Runtime[1].JSON {
		t.Errorf("runtime %+v", vars.Runtime)
	}
}

func TestNoScriptsIsNoop(t *testing.T) {
	s := NewSession(&Vars{})
	req := newReq()
	if err := s.PreRequest(&req, []Script{{Source: "  "}}); err != nil {
		t.Fatal(err)
	}
	s.PostResponse(req, Response{}, nil, nil, nil, nil)
	if s.Result.Ran() {
		t.Fatal("expected nothing to run")
	}
}

func TestRunnerControl(t *testing.T) {
	// Outside a run the calls only warn.
	s := NewSession(&Vars{})
	req := newReq()
	if err := s.PreRequest(&req, []Script{{Label: "请求", Source: `bru.runner.skipRequest(); bru.setNextRequest("x")`}}); err != nil {
		t.Fatal(err)
	}
	if len(s.Result.Logs) != 2 || s.Result.Logs[0].Level != "warn" {
		t.Fatalf("logs %+v", s.Result.Logs)
	}

	s = NewSession(&Vars{})
	s.Run = &RunControl{}
	if err := s.PreRequest(&req, []Script{{Label: "请求", Source: `bru.runner.skipRequest(); bru.runner.setNextRequest("Login")`}}); err != nil {
		t.Fatal(err)
	}
	if !s.Run.Skip || !s.Run.NextSet || s.Run.Next != "Login" || s.Run.NextNull || len(s.Result.Logs) != 0 {
		t.Fatalf("run %+v logs %+v", s.Run, s.Result.Logs)
	}
	s.Run = &RunControl{}
	s.PostResponse(req, Response{Status: 200}, []Script{{Label: "请求", Source: `bru.setNextRequest(null); bru.runner.stopExecution()`}}, nil, nil, nil)
	if !s.Run.NextSet || !s.Run.NextNull || !s.Run.Stop || len(s.Result.Errors) != 0 {
		t.Fatalf("run %+v errs %+v", s.Run, s.Result.Errors)
	}
}

func TestSleepStopsWhenContextIsCanceled(t *testing.T) {
	sess := NewSession(&Vars{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess.Ctx = ctx
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := sess.PreRequest(&send.RequestData{Method: "GET", URL: "http://x"},
		[]Script{{Label: "t", Source: "bru.sleep(5000);"}})
	if err != nil {
		t.Fatalf("sleep script: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("sleep ignored cancellation: %s", elapsed)
	}
}
