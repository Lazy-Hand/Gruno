package send

import (
	"context"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultipartBodyWithFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"b":1}`), 0o644)
	var got map[string]string
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		got = map[string]string{"name": r.FormValue("name")}
		for _, fh := range r.MultipartForm.File["docs"] {
			f, _ := fh.Open()
			data, _ := io.ReadAll(f)
			got[fh.Filename] = string(data) + "|" + fh.Header.Get("Content-Type")
		}
	}))
	defer srv.Close()

	in := SendInput{Request: RequestData{Method: "POST", URL: srv.URL,
		Headers: []Pair{pair("Content-Type", "multipart/form-data")}, // no boundary: replaced
		Body: BodySpec{Mode: BodyMultipart, FileBase: dir, Form: []Pair{
			pair("name", "{{who}}"),
			pair("docs", "@file(a.txt|"+filepath.Join(dir, "b.json")+")"),
			{Key: "skip", Value: "x", Enabled: false},
		}}},
		Scopes: [][]Pair{{pair("who", "ada")}},
	}
	res, err := NewHTTPService().Send(context.Background(), in, func(ResponseHead) error { return nil })
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(ct, "boundary=") || got["name"] != "ada" ||
		got["a.txt"] != "alpha|text/plain; charset=utf-8" || got["b.json"] != `{"b":1}|application/json` {
		t.Fatalf("ct=%q got=%v", ct, got)
	}

	in.Request.Body.Form = []Pair{pair("f", "@file(missing.bin)")}
	res, _ = NewHTTPService().Send(context.Background(), in, func(ResponseHead) error { return nil })
	if res.ErrorCode != ErrInvalidReq || !strings.Contains(res.ErrorMessage, "读取文件失败") {
		t.Fatalf("missing file: %+v", res)
	}
}

func TestCookieJarKeepsAndSends(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Cookie")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "pref", Value: "dark", Path: "/"})
		}
	}))
	defer srv.Close()
	svc := NewHTTPService()
	sendTo := func(path string, st Settings) {
		t.Helper()
		if _, err := svc.Send(context.Background(), SendInput{Request: RequestData{Method: "GET", URL: srv.URL + path}, Settings: st},
			func(ResponseHead) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	sendTo("/login", Settings{})
	sendTo("/me", Settings{})
	if !strings.Contains(seen, "sid=abc") || !strings.Contains(seen, "pref=dark") {
		t.Fatalf("cookies not sent: %q", seen)
	}
	sendTo("/me", Settings{NoCookies: true})
	if seen != "" {
		t.Fatalf("NoCookies still sent %q", seen)
	}
	list := svc.Cookies.List()
	if len(list) != 2 || list[0].Name != "pref" || list[1].Value != "abc" {
		t.Fatalf("list %+v", list)
	}
	svc.Cookies.Delete(list[1].Host, "sid")
	sendTo("/me", Settings{})
	if strings.Contains(seen, "sid=") || !strings.Contains(seen, "pref=dark") {
		t.Fatalf("after delete: %q", seen)
	}
	svc.Cookies.Clear()
	sendTo("/me", Settings{})
	if seen != "" || len(svc.Cookies.List()) != 0 {
		t.Fatalf("after clear: %q", seen)
	}
}

func TestGenerateCode(t *testing.T) {
	in := SendInput{
		Request: RequestData{Method: "POST", URL: "{{host}}/users", Query: []Pair{pair("v", "1")},
			Headers: []Pair{pair("X-Note", "it's {{who}}")},
			Body:    BodySpec{Mode: BodyJSON, Text: `{"name":"{{who}}"}`},
			Auth:    AuthSpec{Mode: AuthBearer, Token: "{{tok}}"}},
		Scopes: [][]Pair{{pair("host", "https://api.example.com"), pair("who", "ada"), pair("tok", "T")}},
	}
	curl, err := GenerateCode(in, "cURL")
	if err != nil {
		t.Fatal(err)
	}
	want := `curl 'https://api.example.com/users?v=1' \
  -H 'Authorization: Bearer T' \
  -H 'Content-Type: application/json' \
  -H 'X-Note: it'\''s ada' \
  --data-raw '{"name":"ada"}'`
	if curl != want {
		t.Fatalf("curl:\n%s\nwant:\n%s", curl, want)
	}
	for _, lang := range CodeLangs {
		code, err := GenerateCode(in, lang)
		if err != nil || !strings.Contains(code, "api.example.com/users") {
			t.Fatalf("%s: %v\n%s", lang, err, code)
		}
		if lang == "Go (net/http)" {
			if _, err := parser.ParseFile(token.NewFileSet(), "main.go", code, 0); err != nil {
				t.Fatalf("go snippet does not parse: %v\n%s", err, code)
			}
		}
	}

	in.Request.Method = "PUT"
	in.Request.Body = BodySpec{Mode: BodyMultipart, FileBase: "/data", Form: []Pair{pair("name", "{{who}}"), pair("f", "@file(a.png)")}}
	curl, _ = GenerateCode(in, "cURL")
	if !strings.Contains(curl, "-X PUT") || !strings.Contains(curl, "-F name=ada") || !strings.Contains(curl, "-F f=@/data/a.png") ||
		strings.Contains(curl, "Content-Type") {
		t.Fatalf("multipart curl:\n%s", curl)
	}
	goCode, _ := GenerateCode(in, "Go (net/http)")
	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", goCode, 0); err != nil {
		t.Fatalf("multipart go snippet: %v\n%s", err, goCode)
	}
}
