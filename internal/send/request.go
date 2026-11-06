package send

// Request construction: variable resolution, URL/query/path assembly,
// headers, body and auth. Everything here is pure and unit-testable;
// prepareRequest never touches the network.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxResponseBody bounds the decompressed body kept in memory.
const maxResponseBody = 8 << 20

// varResolver resolves {{name}} references against ordered variable
// scopes. Later scopes win; within a scope the last enabled row wins.
// Values may reference other variables; cycles and missing names fail
// before anything is sent.
type varResolver struct {
	scopes   [][]Pair
	cache    map[string]string
	visiting map[string]bool
}

func newVarResolver(scopes ...[]Pair) *varResolver {
	return &varResolver{
		scopes:   scopes,
		cache:    map[string]string{},
		visiting: map[string]bool{},
	}
}

// lookup finds the raw (unresolved) value of name, last enabled row of
// the highest-precedence scope first.
func (r *varResolver) lookup(name string) (string, bool) {
	for i := len(r.scopes) - 1; i >= 0; i-- {
		pairs := r.scopes[i]
		for j := len(pairs) - 1; j >= 0; j-- {
			p := pairs[j]
			if p.Enabled && p.Key == name {
				return p.Value, true
			}
		}
	}
	return "", false
}

// resolveVar resolves one variable name to its final value.
func (r *varResolver) resolveVar(name string) (string, error) {
	if strings.HasPrefix(name, "$") {
		return "", fmt.Errorf("不支持 Postman 动态变量 %s", "{{"+name+"}}")
	}
	if v, ok := r.cache[name]; ok {
		return v, nil
	}
	if r.visiting[name] {
		return "", fmt.Errorf("变量 %q 循环引用", name)
	}
	raw, ok := r.lookup(name)
	if !ok {
		return "", fmt.Errorf("变量 %q 未定义", name)
	}
	r.visiting[name] = true
	resolved, err := r.resolveString(raw)
	delete(r.visiting, name)
	if err != nil {
		return "", err
	}
	r.cache[name] = resolved
	return resolved, nil
}

// resolveString replaces every {{name}} in s. Strings without a
// reference return unchanged. Errors name the variable, never its value.
func (r *varResolver) resolveString(s string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])
		rel := strings.Index(rest[i:], "}}")
		if rel < 0 {
			return "", fmt.Errorf("变量引用缺少闭合: %q", rest[i:min(i+40, len(rest))])
		}
		name := strings.TrimSpace(rest[i+2 : i+rel])
		val, err := r.resolveVar(name)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		rest = rest[i+rel+2:]
	}
}

// resolvePairs resolves key and value of every enabled row, in order.
// Disabled rows are dropped from the result.
func (r *varResolver) resolvePairs(pairs []Pair) ([]Pair, error) {
	out := make([]Pair, 0, len(pairs))
	for _, p := range pairs {
		if !p.Enabled {
			continue
		}
		key, err := r.resolveString(p.Key)
		if err != nil {
			return nil, err
		}
		value, err := r.resolveString(p.Value)
		if err != nil {
			return nil, err
		}
		p.Key, p.Value = key, value
		out = append(out, p)
	}
	return out, nil
}

// isToken reports whether s is a valid RFC 7230 token (methods, header
// names).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue reports whether v is a legal header field value:
// HTAB, SP, VCHAR and obs-text only.
func validHeaderValue(v string) bool {
	for i := range len(v) {
		c := v[i]
		if c == '\t' || c == ' ' || (c >= 0x21 && c <= 0x7e) || c >= 0x80 {
			continue
		}
		return false
	}
	return true
}

// originOf returns scheme://host for error messages, never userinfo,
// path or query which may carry credentials.
func originOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "<invalid-url>"
}

// sanitizeRequestError strips URLs down to their origin so resolved
// variable values (which may be credentials) never reach the UI as part
// of a transport error.
func sanitizeRequestError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Sprintf("%s %s: %s", ue.Op, originOf(ue.URL), ue.Err)
	}
	return err.Error()
}

// prepareRequest builds an *http.Request from input. It is the single
// place request semantics live; every rejection is an error the caller
// maps to invalid-request.
func prepareRequest(ctx context.Context, input SendInput) (*http.Request, error) {
	req := input.Request

	resolver := newVarResolver(input.Scopes...)

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !isToken(method) {
		return nil, fmt.Errorf("非法 HTTP 方法 %q", req.Method)
	}

	// URL: resolve variables, then split literal query/fragment. The
	// stored template carries neither; a variable value may still add a
	// query, which is kept and appended to by the Query rows.
	rawURL, err := resolver.resolveString(req.URL)
	if err != nil {
		return nil, err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("URL 为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("无法解析 URL %s: %v", originOf(rawURL), err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("仅支持 http/https URL: %s", originOf(rawURL))
	}
	if u.Host == "" {
		return nil, fmt.Errorf("URL 缺少主机名: %s", originOf(rawURL))
	}
	u.Fragment = ""

	// Path params: Postman-style :name whole segments.
	pathParams, err := resolver.resolvePairs(req.PathParams)
	if err != nil {
		return nil, err
	}
	pathValues := map[string]string{}
	for _, p := range pathParams {
		pathValues[p.Key] = p.Value
	}
	if err := applyPathParams(u, pathValues); err != nil {
		return nil, err
	}

	// Query rows, appended in order after any query the URL itself has.
	query, err := resolver.resolvePairs(req.Query)
	if err != nil {
		return nil, err
	}
	appendRawQuery(u, query)

	// Body first: its mode decides the default Content-Type.
	var bodyReader io.Reader
	contentType := ""
	switch req.Body.Mode {
	case BodyNone, "":
		// no body
	case BodyJSON:
		text, err := resolver.resolveString(req.Body.Text)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(text)
		contentType = "application/json"
	case BodyText:
		text, err := resolver.resolveString(req.Body.Text)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(text)
		contentType = "text/plain; charset=utf-8"
	case BodyURLEncoded:
		form, err := resolver.resolvePairs(req.Body.Form)
		if err != nil {
			return nil, err
		}
		encoded, err := encodeForm(form)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(encoded)
		contentType = "application/x-www-form-urlencoded"
	default:
		return nil, fmt.Errorf("未知请求体模式 %q", req.Body.Mode)
	}

	// Headers: manual rows in order, then auth, then defaults.
	headers, err := resolver.resolvePairs(req.Headers)
	if err != nil {
		return nil, err
	}
	var host string
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("无法构建请求 %s: %v", originOf(u.String()), err)
	}

	explicitCT := false
	for _, h := range headers {
		key := h.Key
		if strings.EqualFold(key, "host") {
			host = h.Value
			continue
		}
		if strings.EqualFold(key, "content-length") {
			continue // managed from the real body
		}
		if !isToken(key) {
			return nil, fmt.Errorf("非法请求头名称 %q", key)
		}
		if !validHeaderValue(h.Value) {
			return nil, fmt.Errorf("请求头 %q 含非法字符", key)
		}
		if strings.EqualFold(key, "content-type") {
			explicitCT = true
		}
		httpReq.Header.Add(key, h.Value)
	}
	if host != "" {
		httpReq.Host = host
	}
	if contentType != "" && !explicitCT {
		httpReq.Header.Set("Content-Type", contentType)
	}

	// Auth, after manual fields so conflicts are detectable.
	if err := applyAuth(resolver, req.Auth, httpReq, headers, query); err != nil {
		return nil, err
	}

	return httpReq, nil
}

// applyPathParams rewrites :name whole path segments. A segment left
// unresolved means a missing value: reject rather than send it literally.
func applyPathParams(u *url.URL, values map[string]string) error {
	esc := u.EscapedPath()
	if esc == "" {
		esc = "/"
	}
	segs := strings.Split(esc, "/")
	changed := false
	for i, seg := range segs {
		if !strings.HasPrefix(seg, ":") || len(seg) < 2 {
			continue
		}
		name := seg[1:]
		if strings.ContainsAny(name, "/:?#") {
			continue // not a plain :name segment
		}
		value, ok := values[name]
		if !ok {
			return fmt.Errorf("路径参数 %q 缺少值", name)
		}
		segs[i] = url.PathEscape(value) + pathSuffix(seg[len(name)+1:])
		changed = true
	}
	if !changed {
		// Still reject an unresolved :param segment.
		for _, seg := range segs {
			if strings.HasPrefix(seg, ":") && len(seg) > 1 && !strings.ContainsAny(seg[1:], "/:?#") {
				name := seg[1:]
				if _, ok := values[name]; !ok {
					return fmt.Errorf("路径参数 %q 缺少值", name)
				}
			}
		}
		return nil
	}
	newEsc := strings.Join(segs, "/")
	decoded, err := url.PathUnescape(newEsc)
	if err != nil {
		return fmt.Errorf("路径参数编码失败: %w", err)
	}
	u.Path = decoded
	u.RawPath = newEsc
	return nil
}

// pathSuffix keeps any literal tail after :name (we only match whole
// segments, so this is normally empty).
func pathSuffix(s string) string { return s }

// appendRawQuery appends resolved query rows to u.RawQuery in order,
// preserving duplicate keys. Values are escaped exactly once.
func appendRawQuery(u *url.URL, rows []Pair) {
	if len(rows) == 0 {
		return
	}
	var parts []string
	for _, p := range rows {
		parts = append(parts, url.QueryEscape(p.Key)+"="+url.QueryEscape(p.Value))
	}
	extra := strings.Join(parts, "&")
	if u.RawQuery == "" {
		u.RawQuery = extra
	} else {
		u.RawQuery += "&" + extra
	}
}

// encodeForm encodes enabled urlencoded rows in order. Empty keys and
// values are legal; rows are never map-ified.
func encodeForm(rows []Pair) (string, error) {
	parts := make([]string, 0, len(rows))
	for _, p := range rows {
		parts = append(parts, url.QueryEscape(p.Key)+"="+url.QueryEscape(p.Value))
	}
	return strings.Join(parts, "&"), nil
}

// applyAuth resolves and applies the auth spec, rejecting conflicts with
// enabled manual Authorization/API-key fields instead of overwriting.
func applyAuth(r *varResolver, a AuthSpec, req *http.Request, manualHeaders, manualQuery []Pair) error {
	switch a.Mode {
	case "", AuthNone:
		return nil
	case AuthBasic:
		user, err := r.resolveString(a.Username)
		if err != nil {
			return err
		}
		pass, err := r.resolveString(a.Password)
		if err != nil {
			return err
		}
		if hasHeader(manualHeaders, "authorization") {
			return errors.New("Basic 认证与手动 Authorization 请求头冲突: 请删除其中一项")
		}
		req.SetBasicAuth(user, pass)
		return nil
	case AuthBearer:
		token, err := r.resolveString(a.Token)
		if err != nil {
			return err
		}
		if hasHeader(manualHeaders, "authorization") {
			return errors.New("Bearer 认证与手动 Authorization 请求头冲突: 请删除其中一项")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	case AuthAPIKey:
		key, err := r.resolveString(a.Key)
		if err != nil {
			return err
		}
		value, err := r.resolveString(a.Value)
		if err != nil {
			return err
		}
		switch a.Location {
		case AuthLocationHeader, "":
			if hasHeader(manualHeaders, key) {
				return fmt.Errorf("API Key 认证与手动请求头 %q 冲突: 请删除其中一项", key)
			}
			req.Header.Set(key, value)
			return nil
		case AuthLocationQuery:
			for _, q := range manualQuery {
				if q.Key == key {
					return fmt.Errorf("API Key 认证与手动 Query 参数 %q 冲突: 请删除其中一项", key)
				}
			}
			u, err := url.Parse(req.URL.String())
			if err != nil {
				return err
			}
			appendRawQuery(u, []Pair{{Key: key, Value: value, Enabled: true}})
			req.URL = u
			req.RequestURI = ""
			return nil
		default:
			return fmt.Errorf("未知 API Key 位置 %q", a.Location)
		}
	case AuthDigest:
		// Digest needs the server's 401 challenge; the send layer retries
		// with the computed response after the first round.
		if _, err := r.resolveString(a.Username); err != nil {
			return err
		}
		if _, err := r.resolveString(a.Password); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("未知认证模式 %q", a.Mode)
	}
}

// hasHeader reports whether an enabled manual header row names key
// (case-insensitive, per HTTP semantics).
func hasHeader(headers []Pair, key string) bool {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(h.Key, key) {
			return true
		}
	}
	return false
}
