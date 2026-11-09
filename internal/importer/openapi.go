package importer

// OpenAPI 3.x import: each operation becomes a request, grouped into
// folders by its first tag. Servers become the baseUrl collection
// variable; security schemes become auth using {{token}}, {{apiKey}},
// {{username}} and {{password}}, which the environment must define.
// Example values fill parameters and bodies; a body with no example is
// built from its schema.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"gruno/bru"
)

// oaMethods lists operation keys in the order requests are written.
var oaMethods = []string{"get", "post", "put", "patch", "delete", "head", "options"}

var pathParam = regexp.MustCompile(`\{([^}/]+)\}`)

type oaDoc struct {
	root map[string]any
	cv   *converter
}

// ParseOpenAPI reads an OpenAPI 3.x document in JSON or YAML.
func ParseOpenAPI(data []byte) (*Collection, error) {
	var root any
	if strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("不是有效的 JSON: %w", err)
		}
	} else if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("不是有效的 YAML: %w", err)
	}
	doc, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("不是 OpenAPI 文档")
	}
	if _, ok := doc["swagger"]; ok {
		return nil, errors.New("暂不支持 Swagger 2.0，请使用 OpenAPI 3.x")
	}
	ver := kvString(doc["openapi"])
	if ver == "" {
		return nil, errors.New("不是 OpenAPI 文档（缺少 openapi 字段）")
	}
	if !strings.HasPrefix(ver, "3.") {
		return nil, fmt.Errorf("暂不支持 OpenAPI %s，请使用 3.x", ver)
	}

	d := &oaDoc{root: doc, cv: &converter{}}
	name := strings.TrimSpace(kvString(asMap(doc["info"])["title"]))
	if name == "" {
		name = "OpenAPI 导入"
	}

	cb := &bru.CollectionBru{}
	servers := asList(doc["servers"])
	base := ""
	if len(servers) > 0 {
		base = strings.TrimSuffix(kvString(asMap(servers[0])["url"]), "/")
	}
	if len(servers) > 1 {
		d.cv.warnf("只导入第一个服务器地址 %s", base)
	}
	if base == "" {
		d.cv.warnf("文档没有服务器地址，请在集合变量 baseUrl 中设置")
	}
	cb.Vars = bru.Dict{{Key: "baseUrl", Value: base, Enabled: true}}
	if _, ok := doc["webhooks"]; ok {
		d.cv.warnf("webhooks 未导入")
	}
	setCollectionAuth(cb, d.authOf(doc["security"], "none", "集合"))

	paths := asMap(doc["paths"])
	var folders []*Folder
	folderOf := map[string]*Folder{}
	var rootReqs []*bru.Request
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		item := d.resolve(paths[p])
		for _, m := range oaMethods {
			op := asMap(item[m])
			if op == nil {
				continue
			}
			r := d.operation(p, m, op, asList(item["parameters"]))
			tags := asList(op["tags"])
			tag := ""
			if len(tags) > 0 {
				tag = kvString(tags[0])
			}
			if tag == "" {
				r.Meta.Seq = len(rootReqs) + 1
				rootReqs = append(rootReqs, r)
				continue
			}
			f := folderOf[tag]
			if f == nil {
				f = &Folder{Name: tag, Bru: &bru.CollectionBru{Meta: bru.Meta{Name: tag, Seq: len(folders) + 1}, Auth: "inherit"}}
				folderOf[tag] = f
				folders = append(folders, f)
			}
			r.Meta.Seq = len(f.Requests) + 1
			f.Requests = append(f.Requests, r)
		}
	}
	return &Collection{Name: name, Bru: cb, Folders: folders, Requests: rootReqs, Warnings: d.cv.warnings}, nil
}

// operation converts one operation; shared holds the path item's
// parameters, which the operation overrides by name and location.
func (d *oaDoc) operation(path, method string, op map[string]any, shared []any) *bru.Request {
	name := strings.TrimSpace(kvString(op["summary"]))
	if name == "" {
		name = kvString(op["operationId"])
	}
	if name == "" {
		name = strings.ToUpper(method) + " " + path
	}
	where := "接口 " + name
	r := &bru.Request{
		Meta:   bru.Meta{Name: name, Type: "http"},
		Method: method,
		URL:    "{{baseUrl}}" + pathParam.ReplaceAllString(path, ":$1"),
		Docs:   kvString(op["description"]),
		Body:   "none",
	}
	if len(asList(op["servers"])) > 0 {
		d.cv.warnf("%s：接口自己的服务器地址未导入，使用 baseUrl", where)
	}

	seen := map[string]bool{}
	var params []map[string]any
	for _, v := range asList(op["parameters"]) {
		p := d.resolve(v)
		seen[kvString(p["in"])+":"+kvString(p["name"])] = true
		params = append(params, p)
	}
	for _, v := range shared {
		p := d.resolve(v)
		if !seen[kvString(p["in"])+":"+kvString(p["name"])] {
			params = append(params, p)
		}
	}
	for _, p := range params {
		key := kvString(p["name"])
		if key == "" {
			continue
		}
		required := p["required"] == true
		val := kvString(paramValue(p, d))
		switch kvString(p["in"]) {
		case "query":
			r.ParamsQuery = append(r.ParamsQuery, bru.Entry{Key: key, Value: val, Enabled: required})
		case "header":
			r.Headers = append(r.Headers, bru.Entry{Key: key, Value: val, Enabled: required})
		case "path":
			r.ParamsPath = append(r.ParamsPath, bru.Entry{Key: key, Value: val, Enabled: true})
		case "cookie":
			d.cv.warnf("%s：Cookie 参数 %q 未导入", where, key)
		}
	}

	if rb := d.resolve(op["requestBody"]); rb != nil {
		d.body(rb, r, where)
	}
	a := d.authOf(op["security"], "inherit", where)
	r.Auth = a.Sel
	r.AuthBasic, r.AuthBearer, r.AuthAPIKey, r.AuthDigest, r.AuthOAuth2 = a.Basic, a.Bearer, a.APIKey, a.Digest, a.OAuth2
	if method == "trace" {
		d.cv.warnf("%s：TRACE 方法未导入", where)
	}
	return r
}

// body maps the first supported media type of a request body.
func (d *oaDoc) body(rb map[string]any, r *bru.Request, where string) {
	content := asMap(rb["content"])
	mt := pickMedia(content)
	if mt == "" {
		return
	}
	m := asMap(content[mt])
	schema := d.resolve(m["schema"])
	sample := d.mediaSample(m)
	switch {
	case strings.Contains(mt, "json"):
		r.Body = "json"
		r.BodyJSON = prettyJSON(sample)
	case mt == "application/x-www-form-urlencoded":
		r.Body = "form-urlencoded"
		r.BodyFormURLEnc = d.formFields(schema, sample, where)
	case mt == "multipart/form-data":
		r.Body = "multipart-form"
		r.BodyMultipart = d.formFields(schema, sample, where)
	case mt == "text/plain":
		r.Body = "text"
		r.BodyText = kvString(sample)
	case strings.Contains(mt, "xml"):
		r.Body = "xml"
		r.BodyXML = kvString(sample)
	default:
		d.cv.warnf("%s：请求体类型 %q 未导入", where, mt)
	}
}

// pickMedia prefers JSON, then forms, text and XML.
func pickMedia(content map[string]any) string {
	keys := slices.Sorted(maps.Keys(content))
	rank := func(k string) int {
		switch {
		case strings.Contains(k, "json"):
			return 0
		case k == "application/x-www-form-urlencoded":
			return 1
		case k == "multipart/form-data":
			return 2
		case k == "text/plain":
			return 3
		case strings.Contains(k, "xml"):
			return 4
		}
		return 9
	}
	best := ""
	for _, k := range keys {
		if best == "" || rank(k) < rank(best) {
			best = k
		}
	}
	return best
}

// mediaSample is the body example: the media type's example, the first
// of its examples, or a sample built from the schema.
func (d *oaDoc) mediaSample(m map[string]any) any {
	if ex, ok := m["example"]; ok {
		return ex
	}
	examples := asMap(m["examples"])
	if keys := slices.Sorted(maps.Keys(examples)); len(keys) > 0 {
		return asMap(d.resolve(examples[keys[0]]))["value"]
	}
	return d.sampleOf(m["schema"], 0)
}

// paramValue is a parameter's example, else a value from its schema.
func paramValue(p map[string]any, d *oaDoc) any {
	if ex, ok := p["example"]; ok {
		return ex
	}
	return d.sampleOf(p["schema"], 0)
}

// formFields turns a form body's sample into key/value rows; binary
// file fields are skipped.
func (d *oaDoc) formFields(schema map[string]any, sample any, where string) bru.Dict {
	obj, _ := sample.(map[string]any)
	props := asMap(schema["properties"])
	var out bru.Dict
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if kvString(asMap(d.resolve(props[k]))["format"]) == "binary" {
			d.cv.warnf("%s：文件字段 %q 未导入（暂不支持文件上传）", where, k)
			continue
		}
		out = append(out, bru.Entry{Key: k, Value: kvString(obj[k]), Enabled: true})
	}
	return out
}

// authOf maps a security requirement list. nil means def (no key at
// that level); an empty list means no auth.
func (d *oaDoc) authOf(sec any, def, where string) authSet {
	if sec == nil {
		return authSet{Sel: def}
	}
	list := asList(sec)
	if len(list) == 0 {
		return authSet{Sel: "none"}
	}
	names := slices.Sorted(maps.Keys(asMap(list[0])))
	if len(names) == 0 {
		return authSet{Sel: "none"}
	}
	if len(list) > 1 || len(names) > 1 {
		d.cv.warnf("%s：只导入第一个认证方式", where)
	}
	return d.scheme(names[0], where)
}

func (d *oaDoc) scheme(name, where string) authSet {
	comps := asMap(d.root["components"])
	s := d.resolve(asMap(asMap(comps["securitySchemes"])[name]))
	switch kvString(s["type"]) {
	case "http":
		switch strings.ToLower(kvString(s["scheme"])) {
		case "basic":
			return authSet{Sel: "basic", Basic: bru.Dict{
				{Key: "username", Value: "{{username}}", Enabled: true},
				{Key: "password", Value: "{{password}}", Enabled: true},
			}}
		case "bearer":
			return authSet{Sel: "bearer", Bearer: bru.Dict{{Key: "token", Value: "{{token}}", Enabled: true}}}
		}
	case "apiKey":
		placement := ""
		switch kvString(s["in"]) {
		case "header":
			placement = "header"
		case "query":
			placement = "queryparams"
		}
		if placement != "" {
			return authSet{Sel: "apikey", APIKey: bru.Dict{
				{Key: "key", Value: kvString(s["name"]), Enabled: true},
				{Key: "value", Value: "{{apiKey}}", Enabled: true},
				{Key: "placement", Value: placement, Enabled: true},
			}}
		}
	}
	d.cv.warnf("%s：认证方式 %q 暂不支持，已改为无认证", where, name)
	return authSet{Sel: "none"}
}

// resolve follows local $ref pointers to the object they name.
func (d *oaDoc) resolve(v any) map[string]any {
	for range 16 {
		m := asMap(v)
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		target := d.pointer(ref)
		if target == nil {
			d.cv.warnf("无法解析引用 %s", ref)
			return nil
		}
		v = target
	}
	return nil
}

func (d *oaDoc) pointer(ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = d.root
	for part := range strings.SplitSeq(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		cur = asMap(cur)[part]
		if cur == nil {
			return nil
		}
	}
	return cur
}

// sampleOf builds an example value: the schema's example, default or
// first enum value, else a value per type. Depth caps recursive
// schemas.
func (d *oaDoc) sampleOf(v any, depth int) any {
	m := d.resolve(v)
	if m == nil || depth > 6 {
		return nil
	}
	if ex, ok := m["example"]; ok {
		return ex
	}
	if def, ok := m["default"]; ok {
		return def
	}
	if en := asList(m["enum"]); len(en) > 0 {
		return en[0]
	}
	if all := asList(m["allOf"]); len(all) > 0 {
		out := map[string]any{}
		for _, s := range all {
			if sm, ok := d.sampleOf(s, depth+1).(map[string]any); ok {
				maps.Copy(out, sm)
			}
		}
		return out
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if l := asList(m[key]); len(l) > 0 {
			return d.sampleOf(l[0], depth+1)
		}
	}
	switch kvString(m["type"]) {
	case "array":
		return []any{d.sampleOf(m["items"], depth+1)}
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "string":
		return ""
	}
	props := asMap(m["properties"])
	if len(props) > 0 || kvString(m["type"]) == "object" {
		out := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(props)) {
			out[k] = d.sampleOf(props[k], depth+1)
		}
		return out
	}
	return nil
}

func prettyJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
