package bru

import (
	"reflect"
	"strings"
	"testing"
)

const requestSample = `meta {
  name: Get User By Id
  type: http
  seq: 2
}

get {
  url: {{host}}/users/{{userId}}?full=true
  body: none
  auth: bearer
}

headers {
  Accept: application/json
  ~X-Debug: 1
}

params:query {
  page: 1
  ~limit: 50
}

params:path {
  userId: 42
}

body:json {
  {
    "a": 1,
    "nested": {
      "b": [2, 3]
    }
  }
}

auth:bearer {
  token: {{token}}
}

vars:pre-request {
  stamp: 123
}

script:pre-request {
  const n = 1;
  if (n > 0) {
    bru.setVar("x", n);
  }
}

tests {
  test("ok", () => {
    expect(res.status).to.equal(200);
  });
}

docs {
  Gets one user.

  Second paragraph.
}

assert {
  res.status: eq 200
}
`

func TestParseRequest(t *testing.T) {
	r, err := ParseRequest(requestSample)
	if err != nil {
		t.Fatal(err)
	}
	if r.Meta.Name != "Get User By Id" || r.Meta.Seq != 2 || r.Meta.Type != "http" {
		t.Fatalf("meta: %+v", r.Meta)
	}
	if r.Method != "get" || r.URL != "{{host}}/users/{{userId}}?full=true" {
		t.Fatalf("method block: %+v", r)
	}
	if r.Body != "none" || r.Auth != "bearer" {
		t.Fatalf("selectors body=%q auth=%q", r.Body, r.Auth)
	}
	if v, _ := r.Headers.Get("Accept"); v != "application/json" {
		t.Fatalf("headers: %v", r.Headers)
	}
	if len(r.Headers) != 2 || r.Headers[1].Enabled {
		t.Fatalf("disabled header lost: %v", r.Headers)
	}
	if v, _ := r.ParamsQuery.Get("page"); v != "1" {
		t.Fatalf("query: %v", r.ParamsQuery)
	}
	// JSON body survives with embedded braces and indent.
	if !strings.Contains(r.BodyJSON, `"nested": {`) || !strings.Contains(r.BodyJSON, `"b": [2, 3]`) {
		t.Fatalf("body:json: %q", r.BodyJSON)
	}
	// Two-space indent stripped, deeper indent kept relative.
	if !strings.HasPrefix(r.BodyJSON, "{") {
		t.Fatalf("body indent: %q", r.BodyJSON)
	}
	if !strings.Contains(r.ScriptPreRequest, "bru.setVar") {
		t.Fatalf("script: %q", r.ScriptPreRequest)
	}
	if !strings.Contains(r.Tests, "expect(res.status)") {
		t.Fatalf("tests: %q", r.Tests)
	}
	if !strings.Contains(r.Docs, "Second paragraph.") {
		t.Fatalf("docs: %q", r.Docs)
	}
	if v, _ := r.Assert.Get("res.status"); v != "eq 200" {
		t.Fatalf("assert: %v", r.Assert)
	}
}

func TestRoundTrip(t *testing.T) {
	r, err := ParseRequest(requestSample)
	if err != nil {
		t.Fatal(err)
	}
	out := SerializeRequest(r)
	r2, err := ParseRequest(out)
	if err != nil {
		t.Fatalf("reparse %q: %v", out, err)
	}
	if !reflect.DeepEqual(r, r2) {
		t.Fatalf("round trip differs:\n%v\nvs\n%v\nserialized:\n%s", r, r2, out)
	}
}

const envSample = `vars {
  baseUrl: https://api.acme.com
  port: 8080
  ~unused: x
}

vars:secret [
  apiKey
  dbPass
]
`

func TestParseEnvironment(t *testing.T) {
	e, err := ParseEnvironment(envSample, "Production")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "Production" {
		t.Fatal(e.Name)
	}
	if v, _ := e.Vars.Get("baseUrl"); v != "https://api.acme.com" {
		t.Fatalf("vars: %v", e.Vars)
	}
	if !reflect.DeepEqual(e.Secrets, []string{"apiKey", "dbPass"}) {
		t.Fatalf("secrets: %v", e.Secrets)
	}
	out := SerializeEnvironment(e)
	e2, err := ParseEnvironment(out, "Production")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e, e2) {
		t.Fatalf("env round trip differs: %v vs %v", e, e2)
	}
}

func TestParseCollection(t *testing.T) {
	src := `headers {
  X-From-Collection: 1
}

vars {
  host: https://h
}

auth {
  mode: bearer
}

auth:bearer {
  token: abc
}
`
	cb, err := ParseCollection(src)
	if err != nil {
		t.Fatal(err)
	}
	if cb.Auth != "bearer" {
		t.Fatalf("auth mode: %q", cb.Auth)
	}
	if v, _ := cb.Vars.Get("host"); v != "https://h" {
		t.Fatalf("vars: %v", cb.Vars)
	}
	if v, _ := cb.AuthBearer.Get("token"); v != "abc" {
		t.Fatalf("auth: %v", cb.AuthBearer)
	}
}

func TestUnclosedBlock(t *testing.T) {
	if _, err := ParseRequest("meta {\n  name: x\n"); err == nil {
		t.Fatal("expected error")
	}
}

func TestVarsMapPrecedence(t *testing.T) {
	m := VarsMap(
		Dict{{Key: "a", Value: "1", Enabled: true}, {Key: "b", Value: "x", Enabled: false}},
		Dict{{Key: "a", Value: "2", Enabled: true}},
	)
	if m["a"] != "2" {
		t.Fatalf("later var should win: %v", m)
	}
	if _, ok := m["b"]; ok {
		t.Fatalf("disabled var should not resolve: %v", m)
	}
}
