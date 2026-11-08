package send

// OAuth2: fetch, cache and refresh access tokens for the
// client_credentials and password grants, then attach the token to the
// request like Bruno does (Authorization header with a prefix, or a
// query parameter).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth2Spec is the auth:oauth2 block, values still unresolved.
type OAuth2Spec struct {
	GrantType       string `json:"grantType"` // client_credentials, password, authorization_code
	AccessTokenURL  string `json:"accessTokenUrl"`
	RefreshTokenURL string `json:"refreshTokenUrl"`
	ClientID        string `json:"clientId"`
	ClientSecret    string `json:"clientSecret"`
	Scope           string `json:"scope"`
	Username        string `json:"username"`
	Password        string `json:"password"`
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
func (s *HTTPService) oauth2Attach(ctx context.Context, input SendInput) (func(*http.Request) error, string, error) {
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
	token, note, err := s.oauth2Token(ctx, sp)
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
			appendRawQuery(&u, []Pair{{Key: key, Value: token, Enabled: true}})
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
func (s *HTTPService) oauth2Token(ctx context.Context, sp OAuth2Spec) (string, string, error) {
	if sp.GrantType == "" {
		sp.GrantType = "client_credentials"
	}
	if sp.GrantType != "client_credentials" && sp.GrantType != "password" {
		return "", "", fmt.Errorf("暂不支持 OAuth2 授权类型 %q（支持 client_credentials、password）", sp.GrantType)
	}
	if sp.AccessTokenURL == "" {
		return "", "", errors.New("OAuth2: 未填写 access_token_url")
	}
	key := strings.Join([]string{sp.GrantType, sp.AccessTokenURL, sp.ClientID, sp.Username, sp.Scope, sp.CredentialsID}, "\x00")
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
		if t, err := s.requestToken(ctx, endpoint, sp, form); err == nil {
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
	form := url.Values{"grant_type": {sp.GrantType}}
	if sp.GrantType == "password" {
		form.Set("username", sp.Username)
		form.Set("password", sp.Password)
	}
	t, err := s.requestToken(ctx, sp.AccessTokenURL, sp, form)
	if err != nil {
		return "", "", err
	}
	s.tokens.put(key, t)
	return t.AccessToken, "OAuth2: 已获取新 token (" + sp.GrantType + ")" + expiryNote(t, now), nil
}

func expiryNote(t oauth2Token, now time.Time) string {
	if t.Expiry.IsZero() {
		return ""
	}
	return fmt.Sprintf("，%s 后过期", t.Expiry.Sub(now).Round(time.Second))
}

// requestToken posts a token request and decodes the reply (JSON, or
// form-encoded as some providers still send).
func (s *HTTPService) requestToken(ctx context.Context, endpoint string, sp OAuth2Spec, form url.Values) (oauth2Token, error) {
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
	resp, err := s.client.Do(req)
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
