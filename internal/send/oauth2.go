package send

// OAuth2: fetch, cache and refresh access tokens for the
// client_credentials and password grants, then attach the token to the
// request like Bruno does (Authorization header with a prefix, or a
// query parameter).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth2Spec is the auth:oauth2 block, values still unresolved.
type OAuth2Spec struct {
	GrantType        string `json:"grantType"` // client_credentials, password, authorization_code
	AccessTokenURL   string `json:"accessTokenUrl"`
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	CallbackURL      string `json:"callbackUrl,omitempty"`
	PKCE             bool   `json:"pkce,omitempty"`
	RefreshTokenURL  string `json:"refreshTokenUrl"`
	ClientID         string `json:"clientId"`
	ClientSecret     string `json:"clientSecret"`
	Scope            string `json:"scope"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	// CredentialsPlacement is "body" (client_id/client_secret form
	// fields) or "basic_auth_header".
	CredentialsPlacement string `json:"credentialsPlacement"`
	CredentialsID        string `json:"credentialsId"`
	// TokenPlacement is "header" or "url"; TokenHeaderPrefix precedes
	// the token in the header ("" sends the bare token), TokenQueryKey
	// names the query parameter.
	TokenPlacement    string `json:"tokenPlacement"`
	TokenHeaderPrefix string `json:"tokenHeaderPrefix"`
	TokenQueryKey     string `json:"tokenQueryKey"`
	AutoFetch         bool   `json:"autoFetch"`
	AutoRefresh       bool   `json:"autoRefresh"`
}

// oauth2Token is a cached token.
type oauth2Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time // zero: no expires_in given
}

func (t oauth2Token) valid(now time.Time) bool {
	return t.AccessToken != "" && (t.Expiry.IsZero() || now.Add(10*time.Second).Before(t.Expiry))
}

// tokenCache keeps tokens for the life of the app, keyed by the
// resolved endpoint, client and user.
type tokenCache struct {
	mu     sync.Mutex
	tokens map[string]oauth2Token
}

func (c *tokenCache) get(key string) (oauth2Token, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[key]
	return t, ok
}

func (c *tokenCache) put(key string, t oauth2Token) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = map[string]oauth2Token{}
	}
	c.tokens[key] = t
}

func (c *tokenCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens = nil
}

// ClearOAuth2Tokens forgets every cached token, so the next send
// fetches afresh.
func (s *HTTPService) ClearOAuth2Tokens() { s.tokens.clear() }

// resolvedOAuth2 resolves every field of spec.
func resolvedOAuth2(r *varResolver, spec OAuth2Spec) (OAuth2Spec, error) {
	fields := []*string{&spec.GrantType, &spec.AccessTokenURL, &spec.RefreshTokenURL, &spec.ClientID,
		&spec.ClientSecret, &spec.Scope, &spec.Username, &spec.Password, &spec.CredentialsID,
		&spec.TokenHeaderPrefix, &spec.TokenQueryKey}
	for _, f := range fields {
		v, err := r.resolveString(*f)
		if err != nil {
			return spec, err
		}
		*f = strings.TrimSpace(v)
	}
	return spec, nil
}

// oauth2Attach obtains a token for input's oauth2 auth and returns
// how to attach it to the built request: as a literal, after variable
// resolution, so a token is never read as a template. note says what
// happened, for the timeline.
func (s *HTTPService) oauth2Attach(ctx context.Context, input SendInput, clientFor func(*url.URL) (*http.Client, error)) (func(*http.Request) error, string, error) {
	spec := input.Request.Auth.OAuth2
	if spec == nil {
		return nil, "", errors.New("OAuth2 配置缺失")
	}
	sp, err := resolvedOAuth2(newVarResolver(input.Scopes...), *spec)
	if err != nil {
		return nil, "", err
	}
	if sp.TokenPlacement != "url" && hasHeader(input.Request.Headers, "authorization") {
		return nil, "", errors.New("OAuth2 认证与手动 Authorization 请求头冲突: 请删除其中一项")
	}
	token, note, err := s.oauth2Token(ctx, sp, clientFor)
	if err != nil {
		return nil, "", err
	}
	return func(req *http.Request) error {
		if sp.TokenPlacement == "url" {
			key := sp.TokenQueryKey
			if key == "" {
				key = "access_token"
			}
			u := *req.URL
			appendRawQuery(&u, []Pair{{Key: key, Value: token, Enabled: true}}, true)
			req.URL = &u
			return nil
		}
		v := token
		if sp.TokenHeaderPrefix != "" {
			v = sp.TokenHeaderPrefix + " " + token
		}
		req.Header.Set("Authorization", v)
		return nil
	}, note, nil
}

// oauth2Token returns a usable token: cached, refreshed or fetched.
func (s *HTTPService) oauth2Token(ctx context.Context, sp OAuth2Spec, clientFor func(*url.URL) (*http.Client, error)) (string, string, error) {
	if sp.GrantType == "" {
		sp.GrantType = "client_credentials"
	}
	switch sp.GrantType {
	case "client_credentials", "password", "authorization_code":
	default:
		return "", "", fmt.Errorf("暂不支持 OAuth2 授权类型 %q（支持 client_credentials、password、authorization_code）", sp.GrantType)
	}
	if sp.AccessTokenURL == "" {
		return "", "", errors.New("OAuth2: 未填写 access_token_url")
	}
	// The key covers the credentials too: changing a secret or password
	// must not keep serving the token fetched with the old one.
	key := strings.Join([]string{sp.GrantType, sp.AccessTokenURL, sp.AuthorizationURL, sp.CallbackURL,
		sp.ClientID, sp.ClientSecret, sp.Username, sp.Password, sp.Scope, sp.CredentialsID,
		sp.CredentialsPlacement, sp.TokenPlacement, sp.TokenQueryKey}, "\x00")
	now := time.Now()
	cached, ok := s.tokens.get(key)
	if ok && cached.valid(now) {
		return cached.AccessToken, "OAuth2: 使用缓存的 token" + expiryNote(cached, now), nil
	}
	if ok && cached.RefreshToken != "" && sp.AutoRefresh {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {cached.RefreshToken}}
		endpoint := sp.RefreshTokenURL
		if endpoint == "" {
			endpoint = sp.AccessTokenURL
		}
		if t, err := s.requestToken(ctx, clientFor, endpoint, sp, form); err == nil {
			if t.RefreshToken == "" {
				t.RefreshToken = cached.RefreshToken
			}
			s.tokens.put(key, t)
			return t.AccessToken, "OAuth2: 已刷新 token" + expiryNote(t, now), nil
		}
		// A failed refresh falls through to a full fetch.
	}
	if !sp.AutoFetch {
		return "", "", errors.New("OAuth2: 没有可用的 token，且未开启自动获取 (auto_fetch_token)")
	}
	var t oauth2Token
	var err error
	if sp.GrantType == "authorization_code" {
		t, err = s.authorizationCodeToken(ctx, sp, clientFor)
	} else {
		form := url.Values{"grant_type": {sp.GrantType}}
		if sp.GrantType == "password" {
			form.Set("username", sp.Username)
			form.Set("password", sp.Password)
		}
		t, err = s.requestToken(ctx, clientFor, sp.AccessTokenURL, sp, form)
	}
	if err != nil {
		return "", "", err
	}
	s.tokens.put(key, t)
	return t.AccessToken, "OAuth2: 已获取新 token (" + sp.GrantType + ")" + expiryNote(t, now), nil
}

// authFlowTimeout bounds the wait for the browser's callback.
const authFlowTimeout = 2 * time.Minute

// authorizationCodeToken runs the browser authorization_code flow with
// PKCE: a loopback listener takes the redirect, the browser opens the
// authorization page, and the code is exchanged at the token endpoint.
func (s *HTTPService) authorizationCodeToken(ctx context.Context, sp OAuth2Spec, clientFor func(*url.URL) (*http.Client, error)) (oauth2Token, error) {
	if sp.AuthorizationURL == "" {
		return oauth2Token{}, errors.New("OAuth2: 未填写 authorization_url")
	}
	ln, path, redirect, err := callbackListener(sp.CallbackURL)
	if err != nil {
		return oauth2Token{}, err
	}
	defer ln.Close()

	state := randToken(16)
	verifier, challenge := "", ""
	if sp.PKCE {
		verifier = randToken(32)
		sum := sha256.Sum256([]byte(verifier))
		challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			select {
			case errCh <- errors.New(msg):
			default:
			}
			http.Error(w, "授权失败: "+msg, http.StatusBadRequest)
			return
		}
		if q.Get("state") != state {
			select {
			case errCh <- errors.New("回调 state 不匹配"):
			default:
			}
			http.Error(w, "state 不匹配", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			select {
			case errCh <- errors.New("回调缺少 code"):
			default:
			}
			http.Error(w, "缺少 code", http.StatusBadRequest)
			return
		}
		select {
		case codeCh <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><meta charset=utf-8><p>授权完成，可以关闭此窗口。")
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	auth, err := url.Parse(sp.AuthorizationURL)
	if err != nil || auth.Host == "" {
		return oauth2Token{}, fmt.Errorf("OAuth2: authorization_url 无效: %s", originOf(sp.AuthorizationURL))
	}
	q := auth.Query()
	q.Set("response_type", "code")
	if sp.ClientID != "" {
		q.Set("client_id", sp.ClientID)
	}
	q.Set("redirect_uri", redirect)
	if sp.Scope != "" {
		q.Set("scope", sp.Scope)
	}
	q.Set("state", state)
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	auth.RawQuery = q.Encode()
	if s.OpenURL == nil {
		return oauth2Token{}, errors.New("OAuth2: 无法打开浏览器")
	}
	if err := s.OpenURL(auth.String()); err != nil {
		return oauth2Token{}, fmt.Errorf("OAuth2: 打开授权页面失败: %w", err)
	}
	timer := time.NewTimer(authFlowTimeout)
	defer timer.Stop()
	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return oauth2Token{}, fmt.Errorf("OAuth2: 授权失败: %w", err)
	case <-ctx.Done():
		return oauth2Token{}, errors.New("OAuth2: 授权已取消")
	case <-timer.C:
		return oauth2Token{}, errors.New("OAuth2: 等待授权回调超时")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return s.requestToken(ctx, clientFor, sp.AccessTokenURL, sp, form)
}

// callbackListener starts the loopback redirect listener and returns it,
// its path and the redirect URI to send to the authorization server.
func callbackListener(callback string) (net.Listener, string, string, error) {
	path := "/callback"
	addr := "127.0.0.1:0"
	redirect := ""
	if cb := strings.TrimSpace(callback); cb != "" {
		u, err := url.Parse(cb)
		if err != nil || u.Host == "" {
			return nil, "", "", errors.New("OAuth2: 回调地址无效")
		}
		switch strings.ToLower(u.Hostname()) {
		case "127.0.0.1", "localhost", "::1":
		default:
			return nil, "", "", errors.New("OAuth2: 回调地址必须是本机回环地址")
		}
		if u.Port() == "" {
			return nil, "", "", errors.New("OAuth2: 回调地址需要端口")
		}
		addr = net.JoinHostPort(u.Hostname(), u.Port())
		path = u.Path
		if path == "" {
			path = "/callback"
		}
		redirect = u.Scheme + "://" + u.Host + path
		if u.Scheme == "" {
			redirect = "http://" + u.Host + path
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", "", fmt.Errorf("OAuth2: 无法监听回调端口: %w", err)
	}
	if redirect == "" {
		redirect = "http://" + ln.Addr().String() + path
	}
	return ln, path, redirect, nil
}

// randToken returns n random bytes as hex.
func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

func expiryNote(t oauth2Token, now time.Time) string {
	if t.Expiry.IsZero() {
		return ""
	}
	return fmt.Sprintf("，%s 后过期", t.Expiry.Sub(now).Round(time.Second))
}

// requestToken posts a token request and decodes the reply (JSON, or
// form-encoded as some providers still send).
func (s *HTTPService) requestToken(ctx context.Context, clientFor func(*url.URL) (*http.Client, error), endpoint string, sp OAuth2Spec, form url.Values) (oauth2Token, error) {
	if sp.Scope != "" {
		form.Set("scope", sp.Scope)
	}
	if sp.CredentialsPlacement == "basic_auth_header" {
		// RFC 6749 §2.3.1: form-encode before Basic.
	} else {
		if sp.ClientID != "" {
			form.Set("client_id", sp.ClientID)
		}
		if sp.ClientSecret != "" {
			form.Set("client_secret", sp.ClientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauth2Token{}, fmt.Errorf("OAuth2: token 地址无效: %s", originOf(endpoint))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if sp.CredentialsPlacement == "basic_auth_header" {
		req.SetBasicAuth(url.QueryEscape(sp.ClientID), url.QueryEscape(sp.ClientSecret))
	}
	client, err := clientFor(req.URL)
	if err != nil {
		return oauth2Token{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return oauth2Token{}, fmt.Errorf("OAuth2: 请求 token 失败: %s", sanitizeRequestError(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauth2Token{}, fmt.Errorf("OAuth2: 读取 token 响应失败: %v", err)
	}

	var raw map[string]any
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt == "application/x-www-form-urlencoded" || mt == "text/plain" {
		if vals, err := url.ParseQuery(string(body)); err == nil {
			raw = map[string]any{}
			for k := range vals {
				raw[k] = vals.Get(k)
			}
		}
	}
	if raw == nil {
		_ = json.Unmarshal(body, &raw)
	}
	if resp.StatusCode/100 != 2 || raw == nil || raw["access_token"] == nil {
		msg := fmt.Sprintf("OAuth2: token 接口返回 %d", resp.StatusCode)
		if raw != nil && raw["error"] != nil {
			msg += fmt.Sprintf(": %v", raw["error"])
			if d := raw["error_description"]; d != nil {
				msg += fmt.Sprintf(" (%v)", d)
			}
		} else if resp.StatusCode/100 == 2 {
			msg += "，但响应中没有 access_token"
		}
		return oauth2Token{}, errors.New(msg)
	}
	t := oauth2Token{AccessToken: fmt.Sprint(raw["access_token"])}
	if rt, ok := raw["refresh_token"].(string); ok {
		t.RefreshToken = rt
	}
	switch e := raw["expires_in"].(type) {
	case float64:
		t.Expiry = time.Now().Add(time.Duration(e) * time.Second)
	case string:
		var n float64
		if _, err := fmt.Sscan(e, &n); err == nil {
			t.Expiry = time.Now().Add(time.Duration(n) * time.Second)
		}
	}
	return t, nil
}
