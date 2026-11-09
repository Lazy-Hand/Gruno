// Package importer turns exported API collections from other tools into
// Bruno-format data (bru types). Nothing here touches the disk or the UI:
// the caller writes the result into a collection directory.
//
// Features the target cannot run — Postman's pm.* scripts, file uploads,
// auth schemes Gruno does not execute — are dropped and listed in
// Collection.Warnings, so the import never fails on them.
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gruno/bru"
)

// Collection is an imported collection, not yet written anywhere.
type Collection struct {
	Name     string
	Bru      *bru.CollectionBru // collection-level vars and auth
	Folders  []*Folder
	Requests []*bru.Request
	// Warnings names what could not be carried over.
	Warnings []string
}

// Folder is an imported directory.
type Folder struct {
	Name     string
	Bru      *bru.CollectionBru // folder-level auth and vars
	Folders  []*Folder
	Requests []*bru.Request
}

// ---- Postman v2.0 / v2.1 export format ----

type pmCollection struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Item     []pmItem  `json:"item"`
	Variable []pmKV    `json:"variable"`
	Auth     *pmAuth   `json:"auth"`
	Event    []pmEvent `json:"event"`
}

// pmItem is a folder when Request is nil, otherwise a request.
type pmItem struct {
	Name    string     `json:"name"`
	Item    []pmItem   `json:"item"`
	Request *pmRequest `json:"request"`
	Event   []pmEvent  `json:"event"`
	Auth    *pmAuth    `json:"auth"`
}

type pmRequest struct {
	Method string          `json:"method"`
	URL    json.RawMessage `json:"url"`
	Header []pmKV          `json:"header"`
	Body   *pmBody         `json:"body"`
	Auth   *pmAuth         `json:"auth"`
}

// pmKV is a key/value row: headers, query, form fields, auth settings,
// collection variables.
type pmKV struct {
	Key      string `json:"key"`
	Value    any    `json:"value"`
	Disabled bool   `json:"disabled"`
	Type     string `json:"type"` // formdata: "text" or "file"
}

type pmBody struct {
	Mode    string `json:"mode"`
	Raw     string `json:"raw"`
	Options struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
	URLEncoded []pmKV `json:"urlencoded"`
	FormData   []pmKV `json:"formdata"`
	GraphQL    *struct {
		Query     string `json:"query"`
		Variables string `json:"variables"`
	} `json:"graphql"`
}

type pmAuth struct {
	Type   string `json:"type"`
	Bearer []pmKV `json:"bearer"`
	Basic  []pmKV `json:"basic"`
	APIKey []pmKV `json:"apikey"`
	Digest []pmKV `json:"digest"`
	OAuth2 []pmKV `json:"oauth2"`
}

type pmEvent struct {
	Listen string `json:"listen"`
	Script struct {
		Exec any `json:"exec"` // a string or an array of lines
	} `json:"script"`
}

// ParsePostman reads a Postman collection export (v2.0 or v2.1).
func ParsePostman(data []byte) (*Collection, error) {
	var pc pmCollection
	if err := json.Unmarshal(data, &pc); err != nil {
		return nil, fmt.Errorf("不是有效的 JSON: %w", err)
	}
	if pc.Item == nil {
		return nil, errors.New("不是 Postman 集合（缺少 item）")
	}
	cv := &converter{}
	name := strings.TrimSpace(pc.Info.Name)
	if name == "" {
		name = "Postman 导入"
	}
	cb := &bru.CollectionBru{Vars: kvDict(pc.Variable)}
	setCollectionAuth(cb, cv.authOf(pc.Auth, "none", "集合"))
	cv.scripts(pc.Event, "集合")

	folders, reqs := cv.items(pc.Item, "")
	return &Collection{Name: name, Bru: cb, Folders: folders, Requests: reqs, Warnings: cv.warnings}, nil
}

type converter struct {
	warnings []string
}

func (cv *converter) warnf(format string, args ...any) {
	cv.warnings = append(cv.warnings, fmt.Sprintf(format, args...))
}

// items converts a list of Postman items: folders recurse, requests
// are numbered by their position in the list so the sidebar keeps the
// order the export had.
func (cv *converter) items(items []pmItem, where string) ([]*Folder, []*bru.Request) {
	var folders []*Folder
	var reqs []*bru.Request
	for i, it := range items {
		seq := i + 1
		if it.Request != nil {
			reqs = append(reqs, cv.request(it, seq))
			continue
		}
		path := it.Name
		if where != "" {
			path = where + "/" + it.Name
		}
		f := &Folder{Name: it.Name}
		a := cv.authOf(it.Auth, "inherit", "目录 "+path)
		f.Bru = &bru.CollectionBru{Meta: bru.Meta{Name: it.Name, Seq: seq}}
		setCollectionAuth(f.Bru, a)
		cv.scripts(it.Event, "目录 "+path)
		f.Folders, f.Requests = cv.items(it.Item, path)
		folders = append(folders, f)
	}
	return folders, reqs
}

func (cv *converter) request(it pmItem, seq int) *bru.Request {
	pr := it.Request
	where := "请求 " + it.Name
	base, query := urlParts(pr.URL)
	method := strings.ToLower(pr.Method)
	if method == "" {
		method = "get"
	}
	r := &bru.Request{
		Meta:        bru.Meta{Name: it.Name, Type: "http", Seq: seq},
		Method:      method,
		URL:         base,
		ParamsQuery: query,
		Headers:     kvDict(pr.Header),
	}
	a := cv.authOf(pr.Auth, "inherit", where)
	r.Auth = a.Sel
	r.AuthBasic, r.AuthBearer, r.AuthAPIKey, r.AuthDigest, r.AuthOAuth2 = a.Basic, a.Bearer, a.APIKey, a.Digest, a.OAuth2
	cv.bodyOf(pr.Body, r, where)
	cv.scripts(it.Event, where)
	return r
}

// scripts notes scripts that are not imported: Postman's pm.* API is
// not Bruno's.
func (cv *converter) scripts(events []pmEvent, where string) {
	for _, ev := range events {
		if strings.TrimSpace(execText(ev.Script.Exec)) != "" {
			cv.warnf("%s：Postman 脚本未导入（pm.* 与 Bruno 脚本不兼容）", where)
			return
		}
	}
}

func execText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		lines := make([]string, 0, len(x))
		for _, l := range x {
			lines = append(lines, kvString(l))
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// bodyOf maps a Postman body onto the request's body mode and fields.
func (cv *converter) bodyOf(b *pmBody, r *bru.Request, where string) {
	r.Body = "none"
	if b == nil {
		return
	}
	switch b.Mode {
	case "", "none":
	case "raw":
		switch strings.ToLower(b.Options.Raw.Language) {
		case "json":
			r.Body, r.BodyJSON = "json", b.Raw
		case "xml":
			r.Body, r.BodyXML = "xml", b.Raw
		default:
			r.Body, r.BodyText = "text", b.Raw
		}
	case "urlencoded":
		r.Body, r.BodyFormURLEnc = "form-urlencoded", kvDict(b.URLEncoded)
	case "formdata":
		r.Body = "multipart-form"
		for _, kv := range b.FormData {
			if kv.Type == "file" {
				cv.warnf("%s：文件字段 %q 未导入（暂不支持文件上传）", where, kv.Key)
				continue
			}
			r.BodyMultipart = append(r.BodyMultipart, kvDict([]pmKV{kv})...)
		}
	case "graphql":
		r.Body = "graphql"
		if b.GraphQL != nil {
			r.BodyGraphQL = b.GraphQL.Query
			r.BodyGraphQLVars = strings.TrimSpace(b.GraphQL.Variables)
		}
	default:
		cv.warnf("%s：请求体类型 %q 未导入", where, b.Mode)
	}
}

// authSet is the auth selector and its settings, as the bru file
// stores them.
type authSet struct {
	Sel                                   string
	Basic, Bearer, APIKey, Digest, OAuth2 bru.Dict
}

// authOf maps a Postman auth block. A missing block takes def
// ("inherit" for requests and folders, "none" for the collection).
func (cv *converter) authOf(a *pmAuth, def, where string) authSet {
	if a == nil || a.Type == "" {
		return authSet{Sel: def}
	}
	switch a.Type {
	case "noauth":
		return authSet{Sel: "none"}
	case "inherit":
		return authSet{Sel: "inherit"}
	case "bearer":
		return authSet{Sel: "bearer", Bearer: dictOf(a.Bearer, map[string]string{"token": "token"})}
	case "basic":
		return authSet{Sel: "basic", Basic: dictOf(a.Basic, map[string]string{"username": "username", "password": "password"})}
	case "digest":
		return authSet{Sel: "digest", Digest: dictOf(a.Digest, map[string]string{"username": "username", "password": "password"})}
	case "apikey":
		d := dictOf(a.APIKey, map[string]string{"key": "key", "value": "value"})
		placement := "header"
		if valueOf(a.APIKey, "in") == "query" {
			placement = "queryparams"
		}
		d = append(d, bru.Entry{Key: "placement", Value: placement, Enabled: true})
		return authSet{Sel: "apikey", APIKey: d}
	case "oauth2":
		return authSet{Sel: "oauth2", OAuth2: oauth2Dict(a.OAuth2)}
	}
	cv.warnf("%s：认证方式 %q 暂不支持，已改为无认证", where, a.Type)
	return authSet{Sel: "none"}
}

// oauth2Dict maps Postman's OAuth2 settings to the auth:oauth2 keys
// Gruno reads; keys it does not name keep Bruno's defaults.
func oauth2Dict(list []pmKV) bru.Dict {
	d := dictOf(list, map[string]string{
		"accessTokenUrl": "access_token_url",
		"clientId":       "client_id",
		"clientSecret":   "client_secret",
		"scope":          "scope",
		"username":       "username",
		"password":       "password",
	})
	grant := "client_credentials"
	if valueOf(list, "grantType") == "password_credentials" {
		grant = "password"
	}
	d = append(d, bru.Entry{Key: "grant_type", Value: grant, Enabled: true})
	if valueOf(list, "clientAuthentication") == "as_basic_auth_header" {
		d = append(d, bru.Entry{Key: "credentials_placement", Value: "basic_auth_header", Enabled: true})
	}
	if valueOf(list, "addTokenTo") == "queryParams" {
		d = append(d, bru.Entry{Key: "token_placement", Value: "url", Enabled: true})
	}
	if p := valueOf(list, "headerPrefix"); p != "" {
		d = append(d, bru.Entry{Key: "token_header_prefix", Value: p, Enabled: true})
	}
	return d
}

func setCollectionAuth(cb *bru.CollectionBru, a authSet) {
	cb.Auth = a.Sel
	cb.AuthBasic, cb.AuthBearer, cb.AuthAPIKey, cb.AuthDigest, cb.AuthOAuth2 = a.Basic, a.Bearer, a.APIKey, a.Digest, a.OAuth2
}

// urlParts splits a Postman URL (a string, or an object with raw and
// query) into the URL without its query and the query rows.
func urlParts(raw json.RawMessage) (string, bru.Dict) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		base, q, _ := strings.Cut(s, "?")
		return base, queryDict(q)
	}
	var o struct {
		Raw   string `json:"raw"`
		Query []pmKV `json:"query"`
	}
	_ = json.Unmarshal(raw, &o)
	base, q, found := strings.Cut(o.Raw, "?")
	if len(o.Query) > 0 || !found {
		return base, kvDict(o.Query)
	}
	return base, queryDict(q)
}

// queryDict parses a raw query string ("a=1&b=2").
func queryDict(q string) bru.Dict {
	var d bru.Dict
	for _, part := range strings.Split(q, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if k == "" {
			continue
		}
		d = append(d, bru.Entry{Key: k, Value: v, Enabled: true})
	}
	return d
}

// kvDict converts key/value rows, keeping order and disabled rows.
func kvDict(list []pmKV) bru.Dict {
	var d bru.Dict
	for _, kv := range list {
		if kv.Key == "" {
			continue
		}
		d = append(d, bru.Entry{Key: kv.Key, Value: kvString(kv.Value), Enabled: !kv.Disabled})
	}
	return d
}

// dictOf keeps the listed keys of list under their Bruno names, in list
// order.
func dictOf(list []pmKV, keys map[string]string) bru.Dict {
	var d bru.Dict
	for _, kv := range list {
		if name, ok := keys[kv.Key]; ok {
			d = append(d, bru.Entry{Key: name, Value: kvString(kv.Value), Enabled: !kv.Disabled})
		}
	}
	return d
}

func valueOf(list []pmKV, key string) string {
	for _, kv := range list {
		if kv.Key == key {
			return kvString(kv.Value)
		}
	}
	return ""
}

func kvString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
