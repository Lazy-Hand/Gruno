package send

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOAuth2ClientCredentialsCacheRefreshAndPlacement(t *testing.T) {
	var fetches, refreshes atomic.Int32
	var lastForm, lastBasic string
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		lastForm = r.PostForm.Encode()
		u, p, _ := r.BasicAuth()
		lastBasic = u + ":" + p
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("grant_type") {
		case "refresh_token":
			refreshes.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-refreshed", "expires_in": 3600})
		case "client_credentials", "password":
			n := fetches.Add(1)
			if r.PostForm.Get("client_secret") == "bad" {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client", "error_description": "nope"})
				return
			}
			// The first token is already expired, to exercise refresh.
			exp := 3600
			if n == 1 {
				exp = 0
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-{{x}}", "refresh_token": "r1", "expires_in": exp})
		}
	}))
	defer tokSrv.Close()
	var gotAuth, gotQuery string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
	}))
	defer api.Close()

	svc := NewHTTPService()
	spec := &OAuth2Spec{GrantType: "client_credentials", AccessTokenURL: "{{tok}}/token", ClientID: "id", ClientSecret: "{{sec}}",
		Scope: "read", TokenHeaderPrefix: "Bearer", AutoFetch: true, AutoRefresh: true}
	in := SendInput{
		Request: RequestData{Method: "GET", URL: api.URL + "/x", Auth: AuthSpec{Mode: AuthOAuth2, OAuth2: spec}},
		Scopes:  [][]Pair{{pair("tok", tokSrv.URL), pair("sec", "s3")}},
	}
	send := func() ResponseResult {
		t.Helper()
		res, err := svc.Send(context.Background(), in, func(ResponseHead) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := send()
	if res.ErrorCode != "" || gotAuth != "Bearer tok-{{x}}" {
		t.Fatalf("first: %+v auth=%q", res, gotAuth)
	}
	if !strings.Contains(lastForm, "client_id=id") || !strings.Contains(lastForm, "client_secret=s3") || !strings.Contains(lastForm, "scope=read") {
		t.Fatalf("form %q", lastForm)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "已获取新 token") {
		t.Fatalf("notes %v", res.Notes)
	}

	// Expired with a refresh token: refreshed, not refetched.
	res = send()
	if gotAuth != "Bearer tok-refreshed" || refreshes.Load() != 1 || fetches.Load() != 1 {
		t.Fatalf("refresh: auth=%q refreshes=%d fetches=%d notes=%v", gotAuth, refreshes.Load(), fetches.Load(), res.Notes)
	}
	// Valid now: cached.
	res = send()
	if refreshes.Load() != 1 || fetches.Load() != 1 || !strings.Contains(res.Notes[0], "缓存") {
		t.Fatalf("cache: %v", res.Notes)
	}

	// Token in the URL, password grant with Basic client credentials.
	svc.ClearOAuth2Tokens()
	spec.GrantType, spec.Username, spec.Password = "password", "bob", "pw"
	spec.CredentialsPlacement, spec.TokenPlacement = "basic_auth_header", "url"
	in.Request.Query = []Pair{pair("a", "1")}
	res = send()
	if res.ErrorCode != "" || gotQuery != "a=1&access_token=tok-%7B%7Bx%7D%7D" {
		t.Fatalf("url placement: %+v query=%q", res, gotQuery)
	}
	if lastBasic != "id:s3" || strings.Contains(lastForm, "client_secret") || !strings.Contains(lastForm, "username=bob") {
		t.Fatalf("basic=%q form=%q", lastBasic, lastForm)
	}

	// Server error surfaces as an auth failure without sending.
	svc.ClearOAuth2Tokens()
	spec.CredentialsPlacement = "body"
	in.Scopes = [][]Pair{{pair("tok", tokSrv.URL), pair("sec", "bad")}}
	gotQuery = "untouched"
	res = send()
	if res.ErrorCode != ErrAuth || !strings.Contains(res.ErrorMessage, "invalid_client") || gotQuery != "untouched" {
		t.Fatalf("error: %+v", res)
	}
	if strings.Contains(res.ErrorMessage, "bad") {
		t.Fatalf("secret leaked into error: %q", res.ErrorMessage)
	}
}

func TestOAuth2Rejections(t *testing.T) {
	svc := NewHTTPService()
	for _, tc := range []struct {
		spec OAuth2Spec
		hdr  []Pair
		want string
	}{
		{OAuth2Spec{GrantType: "authorization_code", AccessTokenURL: "http://x", AutoFetch: true}, nil, "authorization_url"},
		{OAuth2Spec{AutoFetch: true}, nil, "access_token_url"},
		{OAuth2Spec{AccessTokenURL: "http://127.0.0.1:1"}, nil, "自动获取"},
		{OAuth2Spec{AccessTokenURL: "http://x", AutoFetch: true}, []Pair{pair("Authorization", "x")}, "冲突"},
	} {
		spec := tc.spec
		res, err := svc.Send(context.Background(), SendInput{Request: RequestData{Method: "GET", URL: "http://127.0.0.1:1",
			Headers: tc.hdr, Auth: AuthSpec{Mode: AuthOAuth2, OAuth2: &spec}}}, func(ResponseHead) error { return nil })
		if err != nil || res.ErrorCode != ErrAuth || !strings.Contains(res.ErrorMessage, tc.want) {
			t.Errorf("%+v: %+v %v", tc.spec, res, err)
		}
	}
}
