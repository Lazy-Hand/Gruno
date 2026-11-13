package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"gruno/bru"
)

func TestOAuth2InheritedFromCollection(t *testing.T) {
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("client_secret") != "sec" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"T1","expires_in":600}`))
	}))
	defer tokSrv.Close()
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer api.Close()

	a := openedApp(t)
	a.coll.Dot.Auth = "oauth2"
	a.coll.Dot.AuthOAuth2 = bru.Dict{
		{Key: "grant_type", Value: "client_credentials", Enabled: true},
		{Key: "access_token_url", Value: tokSrv.URL, Enabled: true},
		{Key: "client_id", Value: "id", Enabled: true},
		{Key: "client_secret", Value: "{{secret}}", Enabled: true},
	}
	a.coll.Dot.Vars = bru.Dict{{Key: "secret", Value: "sec", Enabled: true}}
	if err := a.coll.SaveCollectionBru(); err != nil {
		t.Fatal(err)
	}
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = api.URL
	br.Req.Auth = "inherit"
	a.openTab(br)
	ts := a.tabState(br)
	a.send(br, ts)
	waitSent(t, ts)
	if gotAuth != "Bearer T1" || ts.result == nil || ts.result.ErrorCode != "" {
		t.Fatalf("auth %q result %+v", gotAuth, ts.result)
	}

	ts.respSection = 2
	tt := ui.NewTester(a.view, 1200, 800)
	if !hasTextContaining(tt, "OAuth2: 已获取新 token") {
		t.Fatalf("timeline: %q", tt.Texts())
	}

	br.Req.Auth = "oauth2"
	br.Req.AuthOAuth2 = a.coll.Dot.AuthOAuth2
	ts.section = 3
	tt = ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"access_token_url", "client_secret", "清除缓存的 token"} {
		if !tt.HasText(want) {
			t.Fatalf("editor missing %q: %q", want, tt.Texts())
		}
	}
}

func hasTextContaining(tt *ui.Tester, sub string) bool {
	for _, s := range tt.Texts() {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
