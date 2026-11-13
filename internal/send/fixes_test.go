package send

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// A Digest challenge must hash resolved credentials, not the {{vars}}.
func TestDigestUsesResolvedCredentials(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="n", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	input := inputFor(RequestData{
		Method: "GET",
		URL:    srv.URL,
		Auth:   AuthSpec{Mode: AuthDigest, Username: "{{u}}", Password: "{{p}}"},
	}, []Pair{pair("u", "alice"), pair("p", "secret")}, nil)

	res, err := NewHTTPService().Send(context.Background(), input, func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != ErrNone || !strings.Contains(res.BodyText, "ok") {
		t.Fatalf("result: %+v", res)
	}
	if !strings.Contains(gotAuth, `username="alice"`) || strings.Contains(gotAuth, "{{") {
		t.Fatalf("digest header: %q", gotAuth)
	}
}

func TestNoEncodeURLSetting(t *testing.T) {
	req := RequestData{Method: "GET", URL: "http://h/p", Query: []Pair{pair("q", "a/b")}}
	in := inputFor(req, nil, nil)
	in.Settings.NoEncodeURL = true
	r, err := prepareRequest(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL.RawQuery != "q=a/b" {
		t.Fatalf("unencoded query: %q", r.URL.RawQuery)
	}

	in.Settings.NoEncodeURL = false
	r, err = prepareRequest(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL.RawQuery != "q=a%2Fb" {
		t.Fatalf("encoded query: %q", r.URL.RawQuery)
	}
}

func TestCookieDeleteAcrossPaths(t *testing.T) {
	jar := NewCookieJar()
	api := &url.URL{Scheme: "https", Host: "h.example", Path: "/api"}
	jar.SetCookies(api, []*http.Cookie{
		{Name: "sid", Value: "1", Path: "/api"},
		{Name: "other", Value: "2", Path: "/api"},
	})
	if got := jar.Cookies(&url.URL{Scheme: "https", Host: "h.example", Path: "/api/x"}); len(got) != 2 {
		t.Fatalf("cookies before delete: %+v", got)
	}
	jar.Delete("h.example", "sid")
	got := jar.Cookies(&url.URL{Scheme: "https", Host: "h.example", Path: "/api/x"})
	if len(got) != 1 || got[0].Name != "other" {
		t.Fatalf("cookie survived delete: %+v", got)
	}
	for _, c := range jar.List() {
		if c.Name == "sid" {
			t.Fatal("deleted cookie still listed")
		}
	}
}

func TestUnsupportedBodyIsReported(t *testing.T) {
	in := inputFor(RequestData{
		Method: "GET",
		URL:    "http://h/p",
		Body:   BodySpec{Mode: BodyUnsupported, Text: "file"},
	}, nil, nil)
	if _, err := prepareRequest(context.Background(), in); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("unsupported body: %v", err)
	}
}

func TestDynamicVariableValues(t *testing.T) {
	for _, name := range DynamicNames {
		v, ok := DynamicVar(name)
		if !ok || v == "" {
			t.Fatalf("%s: %q %v", name, v, ok)
		}
	}
	if _, ok := DynamicVar("$nope"); ok {
		t.Fatal("unknown dynamic name accepted")
	}
}

func TestTruncatedResponseReportsFullSize(t *testing.T) {
	size := maxResponseBody + 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.Write(bytes.Repeat([]byte("a"), size))
	}))
	defer srv.Close()
	res, err := NewHTTPService().Send(context.Background(),
		inputFor(RequestData{Method: "GET", URL: srv.URL}, nil, nil), func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.SizeBytes != maxResponseBody || res.FullSize != int64(size) {
		t.Fatalf("sizes: %+v", res)
	}
}

// A changed client secret must not keep serving the old token.
func TestOAuth2CacheKeyCoversClientSecret(t *testing.T) {
	var hits int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"t%d"}`, n)
	}))
	defer tok.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer api.Close()

	svc := NewHTTPService()
	do := func(secret string) {
		spec := OAuth2Spec{GrantType: "client_credentials", AccessTokenURL: tok.URL,
			ClientID: "id", ClientSecret: secret, AutoFetch: true}
		input := inputFor(RequestData{Method: "GET", URL: api.URL,
			Auth: AuthSpec{Mode: AuthOAuth2, OAuth2: &spec}}, nil, nil)
		if res, err := svc.Send(context.Background(), input, func(ResponseHead) error { return nil }); err != nil || res.ErrorCode != ErrNone {
			t.Fatalf("send: %+v %v", res, err)
		}
	}
	do("a")
	do("a")
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("token fetched %d times for one secret", got)
	}
	do("b")
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("changed secret reused the cached token (%d fetches)", got)
	}
}
