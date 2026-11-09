package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"gruno/bru"
)

func TestMultipartCookiesAndCodegenInApp(t *testing.T) {
	var upload string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" {
			r.ParseMultipartForm(1 << 20)
			if fhs := r.MultipartForm.File["avatar"]; len(fhs) == 1 {
				f, _ := fhs[0].Open()
				data, _ := io.ReadAll(f)
				upload = fhs[0].Filename + ":" + string(data)
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s-1", Path: "/"})
		}
	}))
	defer srv.Close()

	a := openedApp(t)
	os.MkdirAll(filepath.Join(a.coll.Root, "files"), 0o755)
	os.WriteFile(filepath.Join(a.coll.Root, "files", "me.png"), []byte("PNG"), 0o644)
	if got := fileValue(a.coll.Root, []string{filepath.Join(a.coll.Root, "files", "me.png"), "/abs/x.pdf"}); got != "@file(files/me.png|/abs/x.pdf)" {
		t.Fatalf("fileValue %q", got)
	}

	br, _ := a.coll.AddRequest("", "Upload")
	br.Req.Method = "post"
	br.Req.URL = srv.URL + "/upload"
	br.Req.Body = "multipart-form"
	br.Req.BodyMultipart = bru.Dict{{Key: "avatar", Value: "@file(files/me.png)", Enabled: true}}
	a.coll.SaveRequest(br)
	a.openTab(br)
	ts := a.tabState(br)
	a.send(br, ts)
	waitSent(t, ts)
	if upload != "me.png:PNG" {
		t.Fatalf("upload %q result %+v", upload, ts.result)
	}

	// The cookie the server set is in the jar and listed.
	a.cookiesOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("sid") || !tt.HasText("s-1") {
		t.Fatalf("cookies modal: %q", tt.Texts())
	}
	a.cookiesOpen = false

	// Generate Code from the request's menu.
	tt = ui.NewTester(a.view, 1200, 800)
	if err := tt.RightClick("Upload"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("生成代码…"); err != nil {
		t.Fatalf("menu %v: %q", err, tt.Menu())
	}
	tt.Frame()
	code := a.codegen.ed.Buffer.String()
	if !strings.Contains(code, "curl ") || !strings.Contains(code, "-F avatar=@"+filepath.Join(a.coll.Root, "files", "me.png")) {
		t.Fatalf("curl:\n%s (err %s)", code, a.codegen.err)
	}
	a.codegen.lang = "Python (requests)"
	a.generateCode()
	if code := a.codegen.ed.Buffer.String(); !strings.Contains(code, "import requests") || !strings.Contains(code, `open("`) {
		t.Fatalf("python:\n%s", code)
	}
	if !tt.HasText("生成代码 — Upload") {
		t.Fatalf("dialog: %q", tt.Texts())
	}
}

func TestCookiePreferenceStopsJar(t *testing.T) {
	a := openedApp(t)
	a.net.NoCookies = true
	if !a.sendSettings(nil).NoCookies {
		t.Fatal("NoCookies not passed to the send")
	}
}
