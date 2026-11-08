package ui

// bruno.json is Bruno's file as much as ours: it carries proxy, client
// certificates, scripts config and keys newer Bruno versions add. It is
// therefore edited in place — read as ordered raw JSON, only the keys we
// change rewritten, everything else kept as it was and where it was.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gruno/internal/send"
)

// orderedJSON is a JSON object that remembers its key order.
type orderedJSON struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseOrderedJSON(data []byte) (*orderedJSON, error) {
	o := &orderedJSON{vals: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("不是 JSON 对象")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return nil, err
	}
	return o, nil
}

// get decodes key into v, reporting whether it was present.
func (o *orderedJSON) get(key string, v any) bool {
	raw, ok := o.vals[key]
	if !ok {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

func (o *orderedJSON) has(key string) bool { _, ok := o.vals[key]; return ok }

// set stores v under key, keeping its place or appending it.
func (o *orderedJSON) set(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = raw
	return nil
}

func (o *orderedJSON) del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// marshal renders the object with two-space indent, as Bruno writes it.
func (o *orderedJSON) marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range o.keys {
		if i > 0 {
			b.WriteString(",")
		}
		kb, _ := json.Marshal(k)
		b.WriteString("\n  ")
		b.Write(kb)
		b.WriteString(": ")
		var ind bytes.Buffer
		if err := json.Indent(&ind, o.vals[k], "  ", "  "); err != nil {
			return nil, err
		}
		b.Write(ind.Bytes())
	}
	if len(o.keys) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// updateBrunoJSON reads bruno.json, lets fn change it and writes it
// back. A missing file starts from Bruno's defaults.
func (c *Collection) updateBrunoJSON(fn func(o *orderedJSON) error) error {
	path := filepath.Join(c.Root, "bruno.json")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	o, err := parseOrderedJSON(data)
	if err != nil {
		return fmt.Errorf("bruno.json: %w", err)
	}
	if !o.has("version") {
		o.set("version", "1")
	}
	if !o.has("name") {
		o.set("name", c.Name)
	}
	if !o.has("type") {
		o.set("type", "collection")
	}
	if err := fn(o); err != nil {
		return err
	}
	out, err := o.marshal()
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// ---- proxy ----

// brunoProxyV1 is bruno.json's proxy object: enabled is true, false or
// "global"; port a number or string.
type brunoProxyV1 struct {
	Enabled  any    `json:"enabled"`
	Protocol string `json:"protocol"`
	Hostname string `json:"hostname"`
	Port     any    `json:"port"`
	Auth     struct {
		Enabled  bool   `json:"enabled"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"auth"`
	BypassProxy string `json:"bypassProxy"`
}

func portString(p any) string {
	switch v := p.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	}
	return ""
}

// parseProxy reads bruno.json's proxy: "global" (or none) follows the
// system, false goes direct, true uses the configured proxy.
func parseProxy(o *orderedJSON) send.ProxySettings {
	var p brunoProxyV1
	if !o.get("proxy", &p) {
		return send.ProxySettings{}
	}
	st := send.ProxySettings{Protocol: p.Protocol, Hostname: p.Hostname, Port: portString(p.Port), Bypass: p.BypassProxy}
	if p.Auth.Enabled {
		st.Username, st.Password = p.Auth.Username, p.Auth.Password
	}
	switch v := p.Enabled.(type) {
	case bool:
		if v {
			st.Mode = send.ProxyCustom
		} else {
			st.Mode = send.ProxyNone
		}
	default:
		st.Mode = send.ProxySystem
	}
	return st
}

// proxyJSON renders st in bruno.json's shape, keeping any other keys
// of the existing proxy object.
func proxyJSON(o *orderedJSON, st send.ProxySettings) map[string]any {
	m := map[string]any{}
	o.get("proxy", &m)
	switch st.Mode {
	case send.ProxyCustom:
		m["enabled"] = true
	case send.ProxyNone:
		m["enabled"] = false
	default:
		m["enabled"] = "global"
	}
	m["protocol"] = orDefaultS(st.Protocol, "http")
	m["hostname"] = st.Hostname
	if n, err := strconv.Atoi(st.Port); err == nil {
		m["port"] = n
	} else if st.Port == "" {
		m["port"] = nil
	} else {
		m["port"] = st.Port
	}
	m["auth"] = map[string]any{"enabled": st.Username != "", "username": st.Username, "password": st.Password}
	m["bypassProxy"] = st.Bypass
	return m
}

// ---- client certificates ----

type brunoCert struct {
	Domain       string `json:"domain"`
	Type         string `json:"type"` // "cert" or "pfx"
	CertFilePath string `json:"certFilePath,omitempty"`
	KeyFilePath  string `json:"keyFilePath,omitempty"`
	PfxFilePath  string `json:"pfxFilePath,omitempty"`
	Passphrase   string `json:"passphrase"`
}

type brunoCerts struct {
	Enabled bool        `json:"enabled"`
	Certs   []brunoCert `json:"certs"`
}

// parseClientCerts reads bruno.json's clientCertificates, as stored
// (paths may be relative to the collection).
func parseClientCerts(o *orderedJSON) ([]send.ClientCert, bool) {
	var bc brunoCerts
	if !o.get("clientCertificates", &bc) {
		return nil, false
	}
	out := make([]send.ClientCert, 0, len(bc.Certs))
	for _, c := range bc.Certs {
		cc := send.ClientCert{Domain: c.Domain, Passphrase: c.Passphrase}
		if c.Type == "pfx" {
			cc.PFXFile = c.PfxFilePath
		} else {
			cc.CertFile, cc.KeyFile = c.CertFilePath, c.KeyFilePath
		}
		out = append(out, cc)
	}
	return out, bc.Enabled
}

func clientCertsJSON(certs []send.ClientCert, enabled bool) brunoCerts {
	bc := brunoCerts{Enabled: enabled, Certs: []brunoCert{}}
	for _, c := range certs {
		b := brunoCert{Domain: c.Domain, Passphrase: c.Passphrase}
		if c.PFXFile != "" {
			b.Type, b.PfxFilePath = "pfx", c.PFXFile
		} else {
			b.Type, b.CertFilePath, b.KeyFilePath = "cert", c.CertFile, c.KeyFile
		}
		bc.Certs = append(bc.Certs, b)
	}
	return bc
}

// absCerts resolves relative certificate paths against the collection;
// none while certificates are disabled.
func (c *Collection) absCerts() []send.ClientCert {
	if !c.ClientCertsOn {
		return nil
	}
	out := make([]send.ClientCert, 0, len(c.ClientCerts))
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		if strings.HasPrefix(p, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				return filepath.Join(home, p[2:])
			}
		}
		return filepath.Join(c.Root, p)
	}
	for _, cc := range c.ClientCerts {
		cc.CertFile, cc.KeyFile, cc.PFXFile = abs(cc.CertFile), abs(cc.KeyFile), abs(cc.PFXFile)
		out = append(out, cc)
	}
	return out
}

// SaveNetworkSettings writes the collection's proxy and client
// certificates to bruno.json.
func (c *Collection) SaveNetworkSettings() error {
	return c.updateBrunoJSON(func(o *orderedJSON) error {
		if c.Proxy == (send.ProxySettings{}) && !o.has("proxy") {
			// Nothing configured and nothing there: keep the file as is.
		} else if err := o.set("proxy", proxyJSON(o, c.Proxy)); err != nil {
			return err
		}
		if len(c.ClientCerts) == 0 && !o.has("clientCertificates") {
			return nil
		}
		return o.set("clientCertificates", clientCertsJSON(c.ClientCerts, c.ClientCertsOn))
	})
}
