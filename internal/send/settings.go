package send

// Per-send transport settings: timeout, redirects, TLS verification,
// custom CA, proxy and client certificates. The zero Settings is the
// default client: system proxy, verified TLS, up to 10 redirects, no
// timeout of its own (the caller's context still bounds the send).

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pkcs12"

	"github.com/egoist/mygo/netproxy"
)

// ProxyMode picks where requests go.
type ProxyMode string

const (
	ProxySystem ProxyMode = ""       // the system's proxy settings
	ProxyNone   ProxyMode = "none"   // always direct
	ProxyCustom ProxyMode = "custom" // Proxy.URL
)

// ProxySettings is a collection's proxy. Fields may hold {{vars}}.
type ProxySettings struct {
	Mode     ProxyMode `json:"mode"`
	Protocol string    `json:"protocol"` // http, https, socks5
	Hostname string    `json:"hostname"`
	Port     string    `json:"port"`
	Username string    `json:"username"`
	Password string    `json:"password"`
	// Bypass lists hosts sent direct: comma-separated names,
	// "*.example.com" wildcards, IPs.
	Bypass string `json:"bypass"`
}

// ClientCert is a certificate presented to hosts matching Domain
// ("api.example.com", "*.example.com", optionally with ":port"). Either
// CertFile+KeyFile (PEM) or PFXFile is set.
type ClientCert struct {
	Domain     string `json:"domain"`
	CertFile   string `json:"certFile"`
	KeyFile    string `json:"keyFile"`
	PFXFile    string `json:"pfxFile"`
	Passphrase string `json:"passphrase"`
}

// Settings tune one send's client.
type Settings struct {
	Timeout      time.Duration `json:"timeout"` // 0: none
	NoRedirects  bool          `json:"noRedirects"`
	MaxRedirects int           `json:"maxRedirects"` // 0: Go's default of 10
	InsecureTLS  bool          `json:"insecureTls"`
	CACertFile   string        `json:"caCertFile"` // PEM, added to the system pool
	Proxy        ProxySettings `json:"proxy"`
	ClientCerts  []ClientCert  `json:"clientCerts"`
	// NoCookies neither sends the jar's cookies nor keeps new ones.
	NoCookies bool `json:"noCookies"`
}

// transportKey identifies a transport configuration; equal keys share
// a transport and so its connection pool.
type transportKey struct {
	insecure bool
	caFile   string
	proxy    string // resolved proxy URL, "system" or "none"
	bypass   string
	cert     ClientCert
}

type transportCache struct {
	mu sync.Mutex
	m  map[transportKey]*http.Transport
}

func (c *transportCache) get(k transportKey, build func() (*http.Transport, error)) (*http.Transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.m[k]; ok {
		return t, nil
	}
	t, err := build()
	if err != nil {
		return nil, err
	}
	if c.m == nil {
		c.m = map[transportKey]*http.Transport{}
	}
	c.m[k] = t
	return t, nil
}

// clientFor builds the client for a send to target under st, resolving
// proxy fields against r.
func (s *HTTPService) clientFor(st Settings, target *url.URL, r *varResolver) (*http.Client, error) {
	key := transportKey{insecure: st.InsecureTLS, caFile: st.CACertFile, proxy: "system"}
	var proxyURL *url.URL
	switch st.Proxy.Mode {
	case ProxyNone:
		key.proxy = "none"
	case ProxyCustom:
		u, err := resolveProxy(st.Proxy, r)
		if err != nil {
			return nil, err
		}
		proxyURL = u
		key.proxy = u.String()
		key.bypass = st.Proxy.Bypass
	}
	if target != nil {
		if cc, ok := matchClientCert(st.ClientCerts, target); ok {
			key.cert = cc
		}
	}

	var tr *http.Transport
	if key == (transportKey{proxy: "system"}) {
		tr = netproxy.Transport()
	} else {
		var err error
		tr, err = s.transports.get(key, func() (*http.Transport, error) {
			return buildTransport(key, proxyURL)
		})
		if err != nil {
			return nil, err
		}
	}

	c := &http.Client{Transport: tr, Timeout: st.Timeout}
	if !st.NoCookies && s.Cookies != nil {
		c.Jar = s.Cookies
	}
	switch {
	case st.NoRedirects:
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	case st.MaxRedirects > 0:
		max := st.MaxRedirects
		c.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
			if len(via) > max {
				return fmt.Errorf("重定向超过 %d 次", max)
			}
			return nil
		}
	}
	return c, nil
}

func buildTransport(k transportKey, proxyURL *url.URL) (*http.Transport, error) {
	tr := netproxy.Transport().Clone()
	tlsCfg := &tls.Config{InsecureSkipVerify: k.insecure}
	if k.caFile != "" {
		pem, err := os.ReadFile(k.caFile)
		if err != nil {
			return nil, fmt.Errorf("读取 CA 证书失败: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA 证书 %s 中没有可用的 PEM 证书", k.caFile)
		}
		tlsCfg.RootCAs = pool
	}
	if k.cert != (ClientCert{}) {
		cert, err := loadClientCert(k.cert)
		if err != nil {
			return nil, err
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	tr.TLSClientConfig = tlsCfg
	switch k.proxy {
	case "system":
	case "none":
		tr.Proxy = nil
	default:
		bypass := k.bypass
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			if bypassed(bypass, req.URL.Hostname()) {
				return nil, nil
			}
			return proxyURL, nil
		}
	}
	return tr, nil
}

func resolveProxy(p ProxySettings, r *varResolver) (*url.URL, error) {
	fields := []*string{&p.Protocol, &p.Hostname, &p.Port, &p.Username, &p.Password}
	for _, f := range fields {
		v, err := r.resolveString(*f)
		if err != nil {
			return nil, fmt.Errorf("代理配置: %w", err)
		}
		*f = strings.TrimSpace(v)
	}
	if p.Hostname == "" {
		return nil, errors.New("代理配置: 未填写主机")
	}
	scheme := strings.ToLower(orDefault(p.Protocol, "http"))
	switch scheme {
	case "http", "https", "socks5":
	case "socks5h":
		scheme = "socks5"
	default:
		return nil, fmt.Errorf("代理配置: 不支持的协议 %q（支持 http、https、socks5）", p.Protocol)
	}
	host := p.Hostname
	if p.Port != "" {
		host = net.JoinHostPort(p.Hostname, p.Port)
	}
	u := &url.URL{Scheme: scheme, Host: host}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// bypassed reports whether host matches the comma-separated list.
func bypassed(list, host string) bool {
	host = strings.ToLower(host)
	for _, pat := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		pat = strings.ToLower(strings.TrimSpace(pat))
		switch {
		case pat == "":
		case pat == "*" || pat == host:
			return true
		case strings.HasPrefix(pat, "*."):
			if strings.HasSuffix(host, pat[1:]) {
				return true
			}
		case strings.HasPrefix(pat, "."):
			if strings.HasSuffix(host, pat) || host == pat[1:] {
				return true
			}
		}
	}
	return false
}

// matchClientCert picks the first certificate whose domain matches
// target's host (and port, when the pattern has one).
func matchClientCert(certs []ClientCert, target *url.URL) (ClientCert, bool) {
	host := strings.ToLower(target.Hostname())
	port := target.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[target.Scheme]
	}
	for _, cc := range certs {
		pat := strings.ToLower(strings.TrimSpace(cc.Domain))
		pat = strings.TrimPrefix(strings.TrimPrefix(pat, "https://"), "http://")
		if pat == "" {
			continue
		}
		ph, pp, err := net.SplitHostPort(pat)
		if err != nil {
			ph, pp = pat, ""
		}
		if pp != "" && pp != port {
			continue
		}
		if ok, _ := path.Match(ph, host); ok || ph == host {
			return cc, true
		}
	}
	return ClientCert{}, false
}

func loadClientCert(cc ClientCert) (tls.Certificate, error) {
	if cc.PFXFile != "" {
		data, err := os.ReadFile(cc.PFXFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("读取客户端证书失败: %w", err)
		}
		blocks, err := pkcs12.ToPEM(data, cc.Passphrase)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("解析 PFX 客户端证书失败: %w", err)
		}
		var certPEM, keyPEM []byte
		for _, b := range blocks {
			if b.Type == "CERTIFICATE" {
				certPEM = append(certPEM, pem.EncodeToMemory(b)...)
			} else {
				keyPEM = append(keyPEM, pem.EncodeToMemory(b)...)
			}
		}
		return tls.X509KeyPair(certPEM, keyPEM)
	}
	certPEM, err := os.ReadFile(cc.CertFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("读取客户端证书失败: %w", err)
	}
	keyPEM, err := os.ReadFile(cc.KeyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("读取客户端证书私钥失败: %w", err)
	}
	if cc.Passphrase != "" {
		if b, _ := pem.Decode(keyPEM); b != nil && x509.IsEncryptedPEMBlock(b) { //nolint:staticcheck // legacy encrypted PEM keys
			der, err := x509.DecryptPEMBlock(b, []byte(cc.Passphrase)) //nolint:staticcheck
			if err != nil {
				return tls.Certificate{}, fmt.Errorf("解密客户端证书私钥失败: %w", err)
			}
			keyPEM = pem.EncodeToMemory(&pem.Block{Type: b.Type, Bytes: der})
		}
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("客户端证书无效: %w", err)
	}
	return cert, nil
}
