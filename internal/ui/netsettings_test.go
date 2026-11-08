package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"gruno/bru"
	"gruno/internal/send"
)

const brunoJSONWithExtras = `{
  "version": "1",
  "name": "api",
  "type": "collection",
  "scripts": {
    "moduleWhitelist": ["crypto"]
  },
  "proxy": {
    "enabled": true,
    "protocol": "socks5",
    "hostname": "{{proxyHost}}",
    "port": 1080,
    "auth": {"enabled": true, "username": "u", "password": "p"},
    "bypassProxy": "localhost"
  },
  "clientCertificates": {
    "enabled": true,
    "certs": [{"domain": "*.corp.com", "type": "cert", "certFilePath": "certs/c.pem", "keyFilePath": "certs/k.pem", "passphrase": ""}]
  },
  "ignore": ["node_modules", ".git"],
  "presets": {"requestType": "http"}
}
`

func TestBrunoJSONKeepsUnknownKeysAndNetworkSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "bruno.json"), []byte(brunoJSONWithExtras), 0o644)
	c, err := OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	want := send.ProxySettings{Mode: send.ProxyCustom, Protocol: "socks5", Hostname: "{{proxyHost}}", Port: "1080", Username: "u", Password: "p", Bypass: "localhost"}
	if c.Proxy != want {
		t.Fatalf("proxy %+v", c.Proxy)
	}
	certs := c.absCerts()
	if len(certs) != 1 || certs[0].CertFile != filepath.Join(root, "certs/c.pem") || certs[0].Domain != "*.corp.com" {
		t.Fatalf("certs %+v", certs)
	}

	if err := c.SetActiveEnv("dev"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameCollection("API 2"); err != nil {
		t.Fatal(err)
	}
	c.Proxy.Mode = send.ProxyNone
	c.ClientCertsOn = false
	if err := c.SaveNetworkSettings(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "bruno.json"))
	out := string(data)
	for _, w := range []string{`"moduleWhitelist"`, `"presets"`, `"activeEnvironment": "dev"`, `"name": "API 2"`, `"enabled": false`, `"certFilePath": "certs/c.pem"`} {
		if !strings.Contains(out, w) {
			t.Fatalf("missing %s in:\n%s", w, out)
		}
	}
	// Key order is kept: scripts still precedes proxy, presets stays last.
	if !(strings.Index(out, `"scripts"`) < strings.Index(out, `"proxy"`) && strings.Index(out, `"ignore"`) < strings.Index(out, `"presets"`)) {
		t.Fatalf("order changed:\n%s", out)
	}
	c2, _ := OpenCollection(root)
	if c2.Proxy.Mode != send.ProxyNone || c2.ClientCertsOn || len(c2.ClientCerts) != 1 || c2.ActiveEnv != "dev" {
		t.Fatalf("reopened: %+v on=%v", c2.Proxy, c2.ClientCertsOn)
	}
}

func TestSendSettingsLayers(t *testing.T) {
	a := openedApp(t)
	a.net = netPrefs{InsecureTLS: true, CACertFile: "/ca.pem", TimeoutMs: 5000}
	a.coll.Proxy = send.ProxySettings{Mode: send.ProxyNone}
	br, _ := a.coll.AddRequest("", "R")

	st := a.sendSettings(br)
	if st.Timeout != 5*time.Second || !st.InsecureTLS || st.CACertFile != "/ca.pem" || st.Proxy.Mode != send.ProxyNone || st.NoRedirects {
		t.Fatalf("defaults %+v", st)
	}
	br.Req.Settings = bru.Dict{
		{Key: "encodeUrl", Value: "true", Enabled: true},
		{Key: "timeout", Value: "800", Enabled: true},
		{Key: "followRedirects", Value: "false", Enabled: true},
	}
	st = a.sendSettings(br)
	if st.Timeout != 800*time.Millisecond || !st.NoRedirects {
		t.Fatalf("request override %+v", st)
	}
	br.Req.Settings = bru.Dict{{Key: "timeout", Value: "0", Enabled: true}, {Key: "maxRedirects", Value: "3", Enabled: true}}
	st = a.sendSettings(br)
	if st.Timeout != 5*time.Second || st.MaxRedirects != 3 {
		t.Fatalf("zero timeout inherits %+v", st)
	}
}

func TestInsecureTLSPreferenceReachesSend(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tls-ok")) }))
	defer srv.Close()
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = srv.URL
	a.openTab(br)
	ts := a.tabState(br)

	a.send(br, ts)
	waitSent(t, ts)
	if ts.result == nil || ts.result.ErrorCode == "" {
		t.Fatalf("self-signed accepted with verification on: %+v", ts.result)
	}
	a.net.InsecureTLS = true
	a.send(br, ts)
	waitSent(t, ts)
	if ts.result == nil || ts.result.ErrorCode != "" || ts.result.BodyText != "tls-ok" {
		t.Fatalf("insecure: %+v", ts.result)
	}
}

func TestSettingsSectionsRender(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	a.openTab(br)
	ts := a.tabState(br)
	ts.section = 9
	tt := ui.NewTester(a.view, 1200, 800)
	for _, w := range []string{"请求设置", "跟随重定向", "超时 (ms)"} {
		if !tt.HasText(w) {
			t.Fatalf("request settings missing %q: %q", w, tt.Texts())
		}
	}

	a.coll.Proxy.Mode = send.ProxyCustom
	a.initCollectionSettingsEditors()
	a.collSettingsOpen = true
	a.collSettingsTab = 6
	tt = ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("不走代理的主机") {
		t.Fatalf("proxy tab: %q", tt.Texts())
	}
	a.collSettingsTab = 7
	a.coll.ClientCerts = []send.ClientCert{{Domain: "x"}}
	tt = ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("启用客户端证书") || !tt.HasText("私钥") {
		t.Fatalf("certs tab: %q", tt.Texts())
	}

	a.collSettingsOpen = false
	a.openPrefs()
	tt = ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("校验 TLS/SSL 证书") || a.prefsTimeoutIn != "30000" {
		t.Fatalf("prefs: %q", tt.Texts())
	}
}
