package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func pair(k, v string) Pair {
	return Pair{Key: k, Value: v, Enabled: true}
}

func inputFor(data RequestData, collVars, envVars []Pair) SendInput {
	return SendInput{Request: data, Scopes: [][]Pair{collVars, envVars}}
}

func TestPrepareRequestVariables(t *testing.T) {
	data := RequestData{
		Method: "get",
		URL:    "{{base}}/users/{{id}}",
		Query:  []Pair{pair("q", "{{token}}"), {Key: "off", Value: "x", Enabled: false}},
		Headers: []Pair{
			pair("X-Env", "{{env}}"),
			{Key: "X-Off", Value: "no", Enabled: false},
		},
	}
	coll := []Pair{pair("base", "http://coll"), pair("token", "colltok"), pair("id", "1"), pair("env", "collenv")}
	env := []Pair{pair("base", "http://env"), pair("token", "envtok"), pair("env", "envenv")}

	req, err := prepareRequest(context.Background(), inputFor(data, coll, env))
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" {
		t.Fatalf("method not uppercased: %s", req.Method)
	}
	// env overrides collection
	if req.URL.Scheme+"://"+req.URL.Host != "http://env" {
		t.Fatalf("env override failed: %s", req.URL)
	}
	if req.URL.Path != "/users/1" {
		t.Fatalf("path: %s", req.URL.Path)
	}
	q := req.URL.Query()
	if q.Get("q") != "envtok" {
		t.Fatalf("query var: %v", q)
	}
	if _, ok := q["off"]; ok {
		t.Fatal("disabled query row sent")
	}
	if req.Header.Get("X-Env") != "envenv" {
		t.Fatalf("header var: %s", req.Header.Get("X-Env"))
	}
	if req.Header.Get("X-Off") != "" {
		t.Fatal("disabled header sent")
	}
}

func TestPrepareRequestVariableErrors(t *testing.T) {
	base := RequestData{Method: "GET", URL: "http://x/{{missing}}"}
	if _, err := prepareRequest(context.Background(), inputFor(base, nil, nil)); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing var should name it: %v", err)
	}

	dyn := RequestData{Method: "GET", URL: "http://x/{{ $guid }}"}
	if _, err := prepareRequest(context.Background(), inputFor(dyn, nil, nil)); err == nil || !strings.Contains(err.Error(), "动态变量") {
		t.Fatalf("dynamic var should fail: %v", err)
	}

	// Cycle: a -> b -> a
	cyc := RequestData{Method: "GET", URL: "http://x/{{a}}"}
	vars := []Pair{pair("a", "{{b}}"), pair("b", "{{a}}")}
	if _, err := prepareRequest(context.Background(), inputFor(cyc, vars, nil)); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatalf("cycle should fail: %v", err)
	}

	// Chained resolution works.
	chain := RequestData{Method: "GET", URL: "http://x/{{a}}"}
	vars = []Pair{pair("a", "{{b}}-tail"), pair("b", "mid")}
	req, err := prepareRequest(context.Background(), inputFor(chain, vars, nil))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/mid-tail" {
		t.Fatalf("chain: %s", req.URL.Path)
	}
}

func TestPrepareRequestURLAndQuery(t *testing.T) {
	data := RequestData{
		Method: "GET",
		URL:    "http://example.com/path",
		Query: []Pair{
			pair("a", "1"), pair("a", "2"), // duplicate keys preserved
			{Key: "off", Value: "x", Enabled: false},
			pair("empty", ""),
			pair("中文", "a+b&c"),
		},
	}
	req, err := prepareRequest(context.Background(), inputFor(data, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	raw := req.URL.RawQuery
	if !strings.Contains(raw, "a=1") || !strings.Contains(raw, "a=2") {
		t.Fatalf("duplicate query lost: %s", raw)
	}
	if strings.Contains(raw, "off=") {
		t.Fatal("disabled query sent")
	}
	if !strings.Contains(raw, "%E4%B8%AD%E6%96%87=a%2Bb%26c") {
		t.Fatalf("encoding wrong: %s", raw)
	}
	// Encoded once: literal + must not become %252B.
	if strings.Contains(raw, "%252B") {
		t.Fatalf("double-encoded: %s", raw)
	}
}

func TestPrepareRequestPathParams(t *testing.T) {
	data := RequestData{
		Method: "GET",
		URL:    "http://example.com/users/:id/posts/:pid",
		PathParams: []Pair{
			pair("id", "a/b"), // slash must stay encoded
			pair("pid", "9"),
		},
	}
	req, err := prepareRequest(context.Background(), inputFor(data, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.EscapedPath() != "/users/a%2Fb/posts/9" {
		t.Fatalf("escaped path: %s (path=%s raw=%s)", req.URL.EscapedPath(), req.URL.Path, req.URL.RawPath)
	}

	// Missing value rejects.
	data.PathParams = []Pair{pair("id", "1")}
	if _, err := prepareRequest(context.Background(), inputFor(data, nil, nil)); err == nil || !strings.Contains(err.Error(), "pid") {
		t.Fatalf("missing path param should fail: %v", err)
	}

	// Port colon is not a path param.
	data2 := RequestData{Method: "GET", URL: "http://example.com:8080/x", PathParams: []Pair{pair("8080", "evil")}}
	req2, err := prepareRequest(context.Background(), inputFor(data2, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if req2.URL.Host != "example.com:8080" || req2.URL.Path != "/x" {
		t.Fatalf("port mishandled: %s", req2.URL)
	}
}

func TestPrepareRequestBodyAndAuth(t *testing.T) {
	// urlencoded body encoding.
	data := RequestData{
		Method: "POST",
		URL:    "http://x/submit",
		Body: BodySpec{Mode: BodyURLEncoded, Form: []Pair{
			pair("name", "张三"), pair("plus", "a+b"), pair("empty", ""),
		}},
	}
	req, err := prepareRequest(context.Background(), inputFor(data, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	got := string(body)
	if !strings.Contains(got, "name=%E5%BC%A0%E4%B8%89") || !strings.Contains(got, "plus=a%2Bb") || !strings.Contains(got, "empty=") {
		t.Fatalf("form encoding: %s", got)
	}
	if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("content type: %s", req.Header.Get("Content-Type"))
	}

	// Explicit Content-Type wins; JSON default.
	data.Body = BodySpec{Mode: BodyJSON, Text: `{"a":1}`}
	data.Headers = []Pair{pair("Content-Type", "application/vnd.api+json")}
	req, _ = prepareRequest(context.Background(), inputFor(data, nil, nil))
	if req.Header.Get("Content-Type") != "application/vnd.api+json" {
		t.Fatalf("explicit CT lost: %s", req.Header.Get("Content-Type"))
	}
	data.Headers = nil
	req, _ = prepareRequest(context.Background(), inputFor(data, nil, nil))
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("json CT: %s", req.Header.Get("Content-Type"))
	}

	// Basic auth reaches the wire.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "s3cret" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	data2 := RequestData{Method: "GET", URL: srv.URL, Auth: AuthSpec{Mode: AuthBasic, Username: "alice", Password: "s3cret"}}
	resp, err := newHTTPService().client.Do(mustReq(t, data2))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("basic auth failed: %d", resp.StatusCode)
	}

	// Bearer + API key header.
	data3 := RequestData{Method: "GET", URL: srv.URL + "/k", Auth: AuthSpec{Mode: AuthBearer, Token: "tok"}}
	req3, _ := prepareRequest(context.Background(), inputFor(data3, nil, nil))
	if req3.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("bearer: %s", req3.Header.Get("Authorization"))
	}
	data4 := RequestData{Method: "GET", URL: srv.URL, Auth: AuthSpec{Mode: AuthAPIKey, Key: "X-API-Key", Value: "v1", Location: AuthLocationHeader}}
	req4, _ := prepareRequest(context.Background(), inputFor(data4, nil, nil))
	if req4.Header.Get("X-API-Key") != "v1" {
		t.Fatalf("apikey header: %s", req4.Header.Get("X-API-Key"))
	}
	data5 := RequestData{Method: "GET", URL: srv.URL, Auth: AuthSpec{Mode: AuthAPIKey, Key: "api_key", Value: "v2", Location: AuthLocationQuery}}
	req5, _ := prepareRequest(context.Background(), inputFor(data5, nil, nil))
	if req5.URL.Query().Get("api_key") != "v2" {
		t.Fatalf("apikey query: %s", req5.URL.RawQuery)
	}

	// Conflicts reject.
	conflict := RequestData{
		Method: "GET", URL: srv.URL,
		Headers: []Pair{pair("Authorization", "manual")},
		Auth:    AuthSpec{Mode: AuthBearer, Token: "x"},
	}
	if _, err := prepareRequest(context.Background(), inputFor(conflict, nil, nil)); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("auth conflict should fail: %v", err)
	}
}

func TestPrepareRequestHostAndBlocking(t *testing.T) {
	data := RequestData{
		Method: "GET", URL: "http://example.com",
		Headers: []Pair{pair("Host", "internal.local"), pair("Content-Length", "999")},
		Body:    BodySpec{Mode: BodyText, Text: "hi"},
	}
	req, err := prepareRequest(context.Background(), inputFor(data, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if req.Host != "internal.local" {
		t.Fatalf("host: %q", req.Host)
	}
	if req.ContentLength != 2 {
		t.Fatalf("content-length should be real body size: %d", req.ContentLength)
	}

	// Bad URLs reject.
	for _, u := range []string{"", "ftp://x", "http://", "not a url"} {
		d := RequestData{Method: "GET", URL: u}
		if _, err := prepareRequest(context.Background(), inputFor(d, nil, nil)); err == nil {
			t.Fatalf("url %q should fail", u)
		}
	}
}

func mustReq(t *testing.T, data RequestData) *http.Request {
	t.Helper()
	req, err := prepareRequest(context.Background(), inputFor(data, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestPrepareRequestURLEncodedInURLVar(t *testing.T) {
	// A variable value carrying a query is kept, then rows append.
	data := RequestData{
		Method: "GET",
		URL:    "{{base}}",
		Query:  []Pair{pair("b", "2")},
	}
	vars := []Pair{pair("base", "http://h/p?a=1")}
	req, err := prepareRequest(context.Background(), inputFor(data, vars, nil))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/p" {
		t.Fatalf("path: %s", req.URL.Path)
	}
	if req.URL.Query().Get("a") != "1" || req.URL.Query().Get("b") != "2" {
		t.Fatalf("query merge: %s", req.URL.RawQuery)
	}
}

func TestSplitURLQueryHelper(t *testing.T) {
	// url.Parse keeps RawQuery; ensure our append preserves it once.
	u, _ := url.Parse("http://h/p?x=1")
	appendRawQuery(u, []Pair{pair("y", "2")})
	if u.RawQuery != "x=1&y=2" {
		t.Fatalf("rawquery: %s", u.RawQuery)
	}
}
