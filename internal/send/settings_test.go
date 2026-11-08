package send

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sendWith(t *testing.T, svc *HTTPService, rawURL string, st Settings, scopes ...[]Pair) ResponseResult {
	t.Helper()
	res, err := svc.Send(context.Background(), SendInput{
		Request: RequestData{Method: "GET", URL: rawURL}, Scopes: scopes, Settings: st,
	}, func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSettingsTimeoutAndRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		case "/r3":
			http.Redirect(w, r, "/r2", http.StatusFound)
		case "/r2":
			http.Redirect(w, r, "/r1", http.StatusFound)
		case "/r1":
			http.Redirect(w, r, "/end", http.StatusFound)
		case "/end":
			w.Write([]byte("end"))
		}
	}))
	defer srv.Close()
	svc := NewHTTPService()

	if res := sendWith(t, svc, srv.URL+"/slow", Settings{Timeout: 50 * time.Millisecond}); res.ErrorCode != ErrTimeout {
		t.Fatalf("timeout: %+v", res)
	}
	var status int
	head := func(h ResponseHead) error { status = h.StatusCode; return nil }
	res, _ := svc.Send(context.Background(), SendInput{Request: RequestData{Method: "GET", URL: srv.URL + "/r3"}, Settings: Settings{NoRedirects: true}}, head)
	if res.ErrorCode != "" || status != 302 {
		t.Fatalf("no redirects: %d %+v", status, res)
	}
	if res := sendWith(t, svc, srv.URL+"/r3", Settings{MaxRedirects: 2}); res.ErrorCode != ErrNetwork || !strings.Contains(res.ErrorMessage, "重定向超过 2 次") {
		t.Fatalf("max redirects: %+v", res)
	}
	if res := sendWith(t, svc, srv.URL+"/r3", Settings{MaxRedirects: 3}); res.ErrorCode != "" || res.BodyText != "end" {
		t.Fatalf("3 redirects: %+v", res)
	}
}

func TestSettingsTLSVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	svc := NewHTTPService()

	if res := sendWith(t, svc, srv.URL, Settings{}); res.ErrorCode == "" {
		t.Fatal("self-signed certificate accepted by default")
	}
	if res := sendWith(t, svc, srv.URL, Settings{InsecureTLS: true}); res.ErrorCode != "" || res.BodyText != "ok" {
		t.Fatalf("insecure: %+v", res)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	if res := sendWith(t, svc, srv.URL, Settings{CACertFile: ca}); res.ErrorCode != "" {
		t.Fatalf("custom CA: %+v", res)
	}
}

func TestSettingsProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("direct")) }))
	defer target.Close()
	var proxied, proxyAuth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = r.URL.String()
		proxyAuth = r.Header.Get("Proxy-Authorization")
		w.Write([]byte("via-proxy"))
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	svc := NewHTTPService()

	// Upstream through the proxy: the target name need not resolve.
	st := Settings{Proxy: ProxySettings{Mode: ProxyCustom, Protocol: "http", Hostname: "{{ph}}", Port: pu.Port(),
		Username: "u", Password: "p", Bypass: "*.internal, localhost"}}
	res := sendWith(t, svc, "http://api.example.test/x", st, []Pair{pair("ph", pu.Hostname())})
	if res.BodyText != "via-proxy" || proxied != "http://api.example.test/x" || proxyAuth == "" {
		t.Fatalf("proxy: %+v proxied=%q auth=%q", res, proxied, proxyAuth)
	}
	// Bypassed host goes direct.
	proxied = ""
	tu, _ := url.Parse(target.URL)
	direct := "http://localhost:" + tu.Port()
	if res := sendWith(t, svc, direct, st, []Pair{pair("ph", pu.Hostname())}); res.BodyText != "direct" || proxied != "" {
		t.Fatalf("bypass: %+v proxied=%q", res, proxied)
	}
	if res := sendWith(t, svc, "http://x", Settings{Proxy: ProxySettings{Mode: ProxyCustom, Protocol: "socks4", Hostname: "h"}}); res.ErrorCode != ErrInvalidReq {
		t.Fatalf("bad protocol: %+v", res)
	}
	if !bypassed(".corp.com", "corp.com") || !bypassed("*.a.com", "x.a.com") || bypassed("*.a.com", "a.com.evil") {
		t.Fatal("bypass matching")
	}
}

func TestSettingsClientCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeClientCert(t, dir)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(r.TLS.PeerCertificates[0].Subject.CommonName))
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	svc := NewHTTPService()

	st := Settings{InsecureTLS: true, ClientCerts: []ClientCert{
		{Domain: "other.example.com", CertFile: "/nope", KeyFile: "/nope"},
		{Domain: "127.0.0.*:" + u.Port(), CertFile: certFile, KeyFile: keyFile},
	}}
	if res := sendWith(t, svc, srv.URL, st); res.ErrorCode != "" || res.BodyText != "gruno-client" {
		t.Fatalf("client cert: %+v", res)
	}
	st.ClientCerts = st.ClientCerts[:1]
	var status int
	svc.Send(context.Background(), SendInput{Request: RequestData{Method: "GET", URL: srv.URL}, Settings: st},
		func(h ResponseHead) error { status = h.StatusCode; return nil })
	if status != 401 {
		t.Fatalf("unmatched domain should send no cert, got %d", status)
	}
}

func writeClientCert(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gruno-client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	return certFile, keyFile
}
