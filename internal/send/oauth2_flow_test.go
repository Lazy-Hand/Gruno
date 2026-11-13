package send

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The authorization_code flow: the browser opens the authorization
// page with a PKCE challenge, the loopback listener takes the redirect,
// and the code is exchanged for a token.
func TestOAuth2AuthorizationCodePKCE(t *testing.T) {
	var tokenForm url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		tokenForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer api.Close()

	svc := NewHTTPService()
	var authURL string
	svc.OpenURL = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		authURL = raw
		q := u.Query()
		go func() {
			redir, err := url.Parse(q.Get("redirect_uri"))
			if err != nil {
				return
			}
			rq := redir.Query()
			rq.Set("code", "abc")
			rq.Set("state", q.Get("state"))
			redir.RawQuery = rq.Encode()
			if resp, err := http.Get(redir.String()); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spec := OAuth2Spec{
		GrantType:         "authorization_code",
		AuthorizationURL:  "https://auth.example/authorize",
		AccessTokenURL:    tokenSrv.URL,
		ClientID:          "cid",
		ClientSecret:      "sec",
		PKCE:              true,
		AutoFetch:         true,
		TokenPlacement:    "header",
		TokenHeaderPrefix: "Bearer",
	}
	input := inputFor(RequestData{Method: "GET", URL: api.URL,
		Auth: AuthSpec{Mode: AuthOAuth2, OAuth2: &spec}}, nil, nil)
	res, err := svc.Send(ctx, input, func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != ErrNone {
		t.Fatalf("result: %+v", res)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("attached auth: %q", gotAuth)
	}
	au, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth url: %q", authURL)
	}
	q := au.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("client_id") != "cid" || q.Get("state") == "" {
		t.Fatalf("authorization query: %v", q)
	}
	if tokenForm.Get("grant_type") != "authorization_code" || tokenForm.Get("code") != "abc" ||
		tokenForm.Get("redirect_uri") != q.Get("redirect_uri") || len(tokenForm.Get("code_verifier")) < 40 {
		t.Fatalf("token form: %v", tokenForm)
	}
}

func TestOAuth2AuthorizationCodeRejectsForeignCallback(t *testing.T) {
	svc := NewHTTPService()
	spec := OAuth2Spec{
		GrantType: "authorization_code", AuthorizationURL: "https://auth.example/authorize",
		AccessTokenURL: "http://x", CallbackURL: "https://evil.example/cb", AutoFetch: true,
	}
	res, _ := svc.Send(context.Background(), inputFor(RequestData{Method: "GET", URL: "http://x",
		Auth: AuthSpec{Mode: AuthOAuth2, OAuth2: &spec}}, nil, nil), func(ResponseHead) error { return nil })
	if res.ErrorCode != ErrAuth || !strings.Contains(res.ErrorMessage, "回环") {
		t.Fatalf("result: %+v", res)
	}
}
