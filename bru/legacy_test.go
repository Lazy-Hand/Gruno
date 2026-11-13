package bru

import (
	"strings"
	"testing"
)

// Older collections name the query and request-variable blocks "params"
// and "vars"; both must survive a round trip as the current names.
func TestLegacyParamsAndVarsBlocks(t *testing.T) {
	src := `meta {
  name: Old
  seq: 1
}

get {
  url: https://api/old
  body: none
  auth: none
}

params {
  q: x
}

headers {
  Accept: application/json
}

vars {
  token: t1
}
`
	r, err := ParseRequest(src)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r.ParamsQuery.Get("q"); v != "x" {
		t.Fatalf("params: %+v", r.ParamsQuery)
	}
	if v, _ := r.VarsPreRequest.Get("token"); v != "t1" {
		t.Fatalf("vars: %+v", r.VarsPreRequest)
	}
	out := SerializeRequest(r)
	if !strings.Contains(out, "params:query") || !strings.Contains(out, "vars:pre-request") ||
		strings.Contains(out, "\nparams {") || strings.Contains(out, "\nvars {") {
		t.Fatalf("serialized: %s", out)
	}
	r2, err := ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r2.ParamsQuery.Get("q"); v != "x" {
		t.Fatalf("reparsed params: %+v", r2.ParamsQuery)
	}
}

// A blank line inside a text block is written without trailing spaces
// and parses back as the same text.
func TestTextBlockBlankLines(t *testing.T) {
	body := "{\n\n  \"a\": 1\n}"
	r := &Request{Meta: Meta{Name: "T"}, Method: "post", Body: "json", BodyJSON: body}
	out := SerializeRequest(r)
	if strings.Contains(out, "  \n") {
		t.Fatalf("trailing spaces in text block: %q", out)
	}
	r2, err := ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if r2.BodyJSON != body {
		t.Fatalf("body round trip: %q != %q", r2.BodyJSON, body)
	}
}
