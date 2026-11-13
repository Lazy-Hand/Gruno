package ui

// send.go adapts a bru.Request plus its collection scopes into the
// SendInput that prepareRequest/HTTPService.send execute.

import (
	"encoding/json"
	"net/url"
	"strings"

	"gruno/bru"
	"gruno/internal/script"
	"gruno/internal/send"
)

// dictPairs converts a bru dictionary to send-time pairs.
func dictPairs(d bru.Dict) []send.Pair {
	out := make([]send.Pair, 0, len(d))
	for _, e := range d {
		out = append(out, send.Pair{Key: e.Key, Value: e.Value, Enabled: e.Enabled})
	}
	return out
}

// authBlocks are the auth dictionaries of a request or collection.
type authBlocks struct {
	basic, bearer, apiKey, digest, oauth2 bru.Dict
}

// authSpec maps a selector and its blocks to an AuthSpec; "inherit" is
// resolved by the caller.
func authSpec(sel string, b authBlocks) send.AuthSpec {
	switch sel {
	case "basic":
		return send.AuthSpec{Mode: send.AuthBasic,
			Username: get(b.basic, "username"), Password: get(b.basic, "password")}
	case "bearer":
		return send.AuthSpec{Mode: send.AuthBearer, Token: get(b.bearer, "token")}
	case "apikey":
		return send.AuthSpec{Mode: send.AuthAPIKey,
			Key: get(b.apiKey, "key"), Value: get(b.apiKey, "value"),
			Location: send.AuthLocation(orDefaultS(get(b.apiKey, "placement"), "header"))}
	case "digest":
		return send.AuthSpec{Mode: send.AuthDigest,
			Username: get(b.digest, "username"), Password: get(b.digest, "password")}
	case "oauth2":
		return oauth2AuthSpec(b.oauth2)
	case "awsv4", "wsse", "ntlm":
		return send.AuthSpec{Mode: send.AuthMode(sel)}
	default:
		return send.AuthSpec{Mode: send.AuthNone}
	}
}

// bruAuthSpec maps a .bru auth selector + blocks to an AuthSpec.
// "inherit" resolves to the collection/folder auth of the request.
func bruAuthSpec(sel string, r *BruRequest, c *Collection) send.AuthSpec {
	req := r.Req
	if sel == "inherit" {
		return collectionAuthSpec(c, r)
	}
	return authSpec(sel, authBlocks{
		basic: req.AuthBasic, bearer: req.AuthBearer, apiKey: req.AuthAPIKey,
		digest: req.AuthDigest, oauth2: req.AuthOAuth2,
	})
}

// collectionAuthSpec finds the effective auth for "inherit": innermost
// folder.bru auth, else collection.bru.
func collectionAuthSpec(c *Collection, r *BruRequest) send.AuthSpec {
	if r != nil && r.Folder != "" {
		parts := strings.Split(r.Folder, "/")
		for i := len(parts); i >= 1; i-- {
			f := c.folderByPath(strings.Join(parts[:i], "/"))
			if f != nil && f.Bru != nil && f.Bru.Auth != "" && f.Bru.Auth != "none" && f.Bru.Auth != "inherit" {
				return cbAuthSpec(f.Bru)
			}
		}
	}
	if c.Dot != nil && c.Dot.Auth != "" && c.Dot.Auth != "none" && c.Dot.Auth != "inherit" {
		return cbAuthSpec(c.Dot)
	}
	return send.AuthSpec{Mode: send.AuthNone}
}

// cbAuthSpec maps a CollectionBru auth selector to an AuthSpec.
func cbAuthSpec(cb *bru.CollectionBru) send.AuthSpec {
	return authSpec(cb.Auth, authBlocks{
		basic: cb.AuthBasic, bearer: cb.AuthBearer, apiKey: cb.AuthAPIKey,
		digest: cb.AuthDigest, oauth2: cb.AuthOAuth2,
	})
}

// bruBodySpec maps the selector to a BodySpec; unsupported modes keep
// their raw text for the editor but send as BodyNone.
func bruBodySpec(sel string, r *bru.Request) send.BodySpec {
	switch sel {
	case "json":
		return send.BodySpec{Mode: send.BodyJSON, Text: r.BodyJSON}
	case "text":
		return send.BodySpec{Mode: send.BodyText, Text: r.BodyText}
	case "xml":
		// No dedicated BodyMode; send as text so it still goes out.
		return send.BodySpec{Mode: send.BodyText, Text: r.BodyXML}
	case "form-urlencoded":
		return send.BodySpec{Mode: send.BodyURLEncoded, Form: dictPairs(r.BodyFormURLEnc)}
	case "multipart-form":
		return send.BodySpec{Mode: send.BodyMultipart, Form: dictPairs(r.BodyMultipart)}
	case "graphql":
		// Bruno sends {query, variables} as JSON.
		q, _ := json.Marshal(r.BodyGraphQL)
		if r.BodyGraphQLVars != "" {
			return send.BodySpec{Mode: send.BodyJSON, Text: `{"query":` + string(q) + `,"variables":` + r.BodyGraphQLVars + `}`}
		}
		return send.BodySpec{Mode: send.BodyJSON, Text: `{"query":` + string(q) + `}`}
	case "", "none":
		return send.BodySpec{Mode: send.BodyNone}
	default:
		// file, sparql and kinds newer Bruno adds: keep the block, but
		// say so instead of sending an empty body.
		return send.BodySpec{Mode: send.BodyUnsupported, Text: sel}
	}
}

// sendInputOf builds SendInput for one stored request.
func (c *Collection) sendInputOf(r *BruRequest) send.SendInput {
	// Collection/folder headers apply first; a request header of the
	// same name overrides rather than duplicates.
	reqHeaderNames := map[string]bool{}
	for _, e := range r.Req.Headers {
		if e.Enabled {
			reqHeaderNames[strings.ToLower(e.Key)] = true
		}
	}
	var headers []send.Pair
	for _, d := range c.collectionHeaders(r) {
		for _, e := range d {
			if e.Enabled && reqHeaderNames[strings.ToLower(e.Key)] {
				continue
			}
			headers = append(headers, send.Pair{Key: e.Key, Value: e.Value, Enabled: e.Enabled})
		}
	}
	headers = append(headers, dictPairs(r.Req.Headers)...)

	scopes := make([][]send.Pair, 0, 8)
	for _, d := range c.varScopes(r) {
		scopes = append(scopes, dictPairs(d))
	}
	// With param rows the URL ships as path only; the rows compose the
	// query at send. A literal query in a never-opened file survives.
	url := sendURL(r.Req)

	body := bruBodySpec(r.Req.Body, r.Req)
	body.FileBase = c.Root
	return send.SendInput{
		Request: send.RequestData{
			Name:       r.Req.Meta.Name,
			Method:     r.Req.UpperMethod(),
			URL:        url,
			Query:      dictPairs(r.Req.ParamsQuery),
			PathParams: dictPairs(r.Req.ParamsPath),
			Headers:    headers,
			Body:       body,
			Auth:       bruAuthSpec(orDefaultS(r.Req.Auth, "none"), r, c),
		},
		Scopes: scopes,
	}
}

// sendURL is the URL a send uses: with query rows present the URL
// ships as path only, since the rows compose the query. Never mutates.
func sendURL(r *bru.Request) string {
	if len(r.ParamsQuery) > 0 {
		path, _, _ := strings.Cut(r.URL, "?")
		return path
	}
	return r.URL
}

// encodeQueryPart escapes s but keeps {{name}} runs literal, so a
// template stays a template in the .bru file.
func encodeQueryPart(s string) string {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			b.WriteString(url.QueryEscape(rest))
			return b.String()
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			b.WriteString(url.QueryEscape(rest))
			return b.String()
		}
		b.WriteString(url.QueryEscape(rest[:i]))
		b.WriteString(rest[i : i+j+2])
		rest = rest[i+j+2:]
	}
}

// decodeQueryPart undoes percent/plus encoding, keeping the raw text
// when it is not valid encoding.
func decodeQueryPart(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}

// syncQueryToURL rewrites r.URL's query string from ParamsQuery. The
// path (and any template vars in it) is kept untouched. With no rows
// the URL — and its literal query — is kept as is.
func syncQueryToURL(r *bru.Request) {
	if len(r.ParamsQuery) == 0 {
		return
	}
	path, _, _ := strings.Cut(r.URL, "?")
	var enc []string
	for _, e := range r.ParamsQuery {
		if !e.Enabled || e.Key == "" {
			continue
		}
		enc = append(enc, encodeQueryPart(e.Key)+"="+encodeQueryPart(e.Value))
	}
	r.URL = path
	if len(enc) > 0 {
		r.URL = path + "?" + strings.Join(enc, "&")
	}
}

// urlQueryToParams fills ParamsQuery from the URL's query when the
// file carries none — Bruno does the same on open.
func urlQueryToParams(r *bru.Request) {
	if len(r.ParamsPath) == 0 {
		syncPathParamsFromURL(r)
	}
	if len(r.ParamsQuery) > 0 {
		return
	}
	_, q, ok := strings.Cut(r.URL, "?")
	if !ok {
		return
	}
	for _, pair := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(pair, "=")
		if k == "" {
			continue
		}
		r.ParamsQuery = append(r.ParamsQuery, bru.Entry{Key: decodeQueryPart(k), Value: decodeQueryPart(v), Enabled: true})
	}
}

func get(d bru.Dict, key string) string {
	v, _ := d.Get(key)
	return v
}

func orDefaultS(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// extractQueryToParams replaces ParamsQuery from the URL's query on
// URL edits, keeping enabled flags where a row survives by position.
func extractQueryToParams(r *bru.Request) {
	_, q, ok := strings.Cut(r.URL, "?")
	var next bru.Dict
	if ok {
		for i, pair := range strings.Split(q, "&") {
			k, v, _ := strings.Cut(pair, "=")
			if k == "" {
				continue
			}
			e := bru.Entry{Key: decodeQueryPart(k), Value: decodeQueryPart(v), Enabled: true}
			if i < len(r.ParamsQuery) {
				e.Enabled = r.ParamsQuery[i].Enabled
			}
			next = append(next, e)
		}
	}
	r.ParamsQuery = next
	syncPathParamsFromURL(r)
}

// syncPathParamsFromURL extracts :name path segments from r.URL and
// syncs them into r.ParamsPath, preserving existing values and enabled flags.
func syncPathParamsFromURL(r *bru.Request) {
	pathPart, _, _ := strings.Cut(r.URL, "?")
	pathPart, _, _ = strings.Cut(pathPart, "#")
	// Skip scheme://host so :port is not mistaken for a path parameter.
	if idx := strings.Index(pathPart, "://"); idx >= 0 {
		rest := pathPart[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			pathPart = rest[slash:]
		} else {
			pathPart = ""
		}
	} else if idx := strings.Index(pathPart, "}}"); idx >= 0 {
		// Handle {{baseUrl}}/users/:id
		rest := pathPart[idx+2:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			pathPart = rest[slash:]
		} else {
			pathPart = ""
		}
	}

	var keys []string
	seen := map[string]bool{}
	for _, seg := range strings.Split(pathPart, "/") {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			name := seg[1:]
			if !strings.ContainsAny(name, "/:?#{} ") && !seen[name] {
				seen[name] = true
				keys = append(keys, name)
			}
		}
	}
	if len(keys) == 0 {
		// No :name segment in the URL: keep the rows. Templates such as
		// {{baseUrl}} carry no path until resolution.
		return
	}
	existing := map[string]bru.Entry{}
	for _, e := range r.ParamsPath {
		existing[e.Key] = e
	}
	var next bru.Dict
	for _, k := range keys {
		if e, ok := existing[k]; ok {
			next = append(next, e)
		} else {
			next = append(next, bru.Entry{Key: k, Value: "", Enabled: true})
		}
	}
	r.ParamsPath = next
}

// scriptVarsOf builds the variable scopes a send's scripts see, from
// the same sources as varScopes plus the session's runtime variables.
// Env (with the environment's secret values) and Runtime are copies:
// scripts change them, the UI applies them back on its own thread.
func (c *Collection) scriptVarsOf(r *BruRequest, runtime []script.RuntimeVar, envSecrets []send.Pair) *script.Vars {
	v := &script.Vars{
		ProcessEnv:     c.loadDotEnv(),
		EnvName:        c.ActiveEnv,
		CollectionName: c.Name,
		Runtime:        append([]script.RuntimeVar(nil), runtime...),
	}
	scopes := c.scopeSet(r)
	v.Collection = dictPairs(scopes.collection)
	for _, d := range scopes.folders {
		v.Folder = append(v.Folder, dictPairs(d)...)
	}
	v.Env = append(dictPairs(scopes.env), envSecrets...)
	v.Request = dictPairs(scopes.request)
	return v
}

// scopeSet is the ordered variable sources of a request, the one place
// the precedence is written down.
type scopeSet struct {
	collection bru.Dict
	folders    []bru.Dict // outermost first
	env        bru.Dict
	request    bru.Dict
}

func (c *Collection) scopeSet(r *BruRequest) scopeSet {
	var s scopeSet
	if c.Dot != nil {
		s.collection = c.Dot.Vars
	}
	var parts []string
	if r != nil && r.Folder != "" {
		parts = strings.Split(r.Folder, "/")
		for i := range parts {
			if f := c.folderByPath(strings.Join(parts[:i+1], "/")); f != nil && f.Bru != nil {
				s.folders = append(s.folders, f.Bru.Vars)
			}
		}
	}
	if env := c.activeEnvironment(); env != nil {
		s.env = env.Vars
	}
	if r != nil {
		s.request = r.Req.VarsPreRequest
	}
	return s
}

// folderChain returns r's folders that have a folder.bru, outermost
// first.
func (c *Collection) folderChain(r *BruRequest) []*Folder {
	if r == nil || r.Folder == "" {
		return nil
	}
	var out []*Folder
	parts := strings.Split(r.Folder, "/")
	for i := range parts {
		if f := c.folderByPath(strings.Join(parts[:i+1], "/")); f != nil && f.Bru != nil {
			out = append(out, f)
		}
	}
	return out
}

func (c *Collection) activeEnvironment() *bru.Environment {
	for _, e := range c.Environments {
		if e.Name == c.ActiveEnv {
			return e
		}
	}
	return nil
}

// requestScripts are the script blocks of one send, in run order:
// pre-request outermost first (collection, folders, request), then
// post-response and tests innermost first, as Bruno's default
// "sandwich" flow runs them.
type requestScripts struct {
	Pre, Post, Tests []script.Script
}

func (c *Collection) scriptsOf(r *BruRequest) requestScripts {
	type src struct {
		label           string
		pre, post, test string
	}
	var chain []src
	if c.Dot != nil {
		chain = append(chain, src{"集合", c.Dot.ScriptPreRequest, c.Dot.ScriptPostResponse, c.Dot.Tests})
	}
	for _, f := range c.folderChain(r) {
		chain = append(chain, src{"目录 " + f.Path, f.Bru.ScriptPreRequest, f.Bru.ScriptPostResponse, f.Bru.Tests})
	}
	chain = append(chain, src{"请求", r.Req.ScriptPreRequest, r.Req.ScriptPostResponse, r.Req.Tests})

	var out requestScripts
	for _, s := range chain {
		out.Pre = append(out.Pre, script.Script{Label: s.label, Source: s.pre})
	}
	for i := len(chain) - 1; i >= 0; i-- {
		out.Post = append(out.Post, script.Script{Label: chain[i].label, Source: chain[i].post})
		out.Tests = append(out.Tests, script.Script{Label: chain[i].label, Source: chain[i].test})
	}
	return out
}

// applyEnvSets writes a finished send's bru.setEnvVar values into
// the active environment in memory, and to its file when any call
// asked for persist.
func (c *Collection) applyEnvSets(v *script.Vars) error {
	if len(v.EnvSets) == 0 {
		return nil
	}
	var env *bru.Environment
	for _, e := range c.Environments {
		if e.Name == v.EnvName {
			env = e
		}
	}
	if env == nil {
		return nil
	}
	persist := false
	for _, s := range v.EnvSets {
		persist = persist || s.Persist
		found := false
		for i := len(env.Vars) - 1; i >= 0; i-- {
			if env.Vars[i].Key == s.Key && env.Vars[i].Enabled {
				env.Vars[i].Value = s.Value
				found = true
				break
			}
		}
		if !found {
			env.Vars = append(env.Vars, bru.Entry{Key: s.Key, Value: s.Value, Enabled: true})
		}
	}
	if persist {
		return c.SaveEnvironment(env)
	}
	return nil
}

// oauth2AuthSpec maps an auth:oauth2 block. Bruno's defaults apply to
// missing keys: Bearer prefix, header placement, credentials in the
// body, token fetched and refreshed automatically.
func oauth2AuthSpec(d bru.Dict) send.AuthSpec {
	prefix, ok := d.Get("token_header_prefix")
	if !ok {
		prefix = "Bearer"
	}
	flag := func(k string) bool {
		v, ok := d.Get(k)
		return !ok || strings.TrimSpace(v) != "false"
	}
	return send.AuthSpec{Mode: send.AuthOAuth2, OAuth2: &send.OAuth2Spec{
		GrantType:            orDefaultS(get(d, "grant_type"), "client_credentials"),
		AccessTokenURL:       get(d, "access_token_url"),
		AuthorizationURL:     get(d, "authorization_url"),
		CallbackURL:          get(d, "callback_url"),
		PKCE:                 flag("pkce"),
		RefreshTokenURL:      get(d, "refresh_token_url"),
		ClientID:             get(d, "client_id"),
		ClientSecret:         get(d, "client_secret"),
		Scope:                get(d, "scope"),
		Username:             get(d, "username"),
		Password:             get(d, "password"),
		CredentialsPlacement: orDefaultS(get(d, "credentials_placement"), "body"),
		CredentialsID:        orDefaultS(get(d, "credentials_id"), "credentials"),
		TokenPlacement:       orDefaultS(get(d, "token_placement"), "header"),
		TokenHeaderPrefix:    prefix,
		TokenQueryKey:        orDefaultS(get(d, "token_query_key"), "access_token"),
		AutoFetch:            flag("auto_fetch_token"),
		AutoRefresh:          flag("auto_refresh_token"),
	}}
}
