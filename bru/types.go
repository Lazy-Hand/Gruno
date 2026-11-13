// Package bru reads and writes Bruno collection files (.bru), the
// plaintext format Bruno uses to keep API collections in version
// control: one .bru file per request, folders as directories,
// environments under environments/.
//
// The format is a sequence of top-level blocks. A block opens with
// "name {" or "name [" at column 0 and closes with "}" or "]" alone at
// column 0; contents are indented two spaces. Three flavors exist:
// dictionaries ("key: value" lines, "~key" disables an entry), raw
// text (kept verbatim, the two-space indent stripped), and arrays of
// names (vars:secret).
package bru

import "strings"

// Entry is one line of a dictionary block. A leading ~ in the file
// disables the entry without deleting it.
type Entry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// Dict is a dictionary block's entries, in file order.
type Dict []Entry

// Get returns the value of an enabled entry.
func (d Dict) Get(key string) (string, bool) {
	for _, e := range d {
		if e.Enabled && e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

// Meta is the request's meta block.
type Meta struct {
	Name string   `json:"name"`
	Type string   `json:"type"` // "http" normally, "ws" for WebSocket
	Seq  int      `json:"seq"`  // order among siblings, 0 keeps file order
	Tags []string `json:"tags,omitempty"`
}

// Request is one request .bru file.
type Request struct {
	Meta Meta

	Method string // "get", "post", "put", "delete", "patch", "options", "head"
	URL    string
	// Body/Auth name the block that applies: "", "none", or the suffix
	// of the matching body:*/auth:* block.
	Body string
	Auth string

	Headers     Dict // headers block
	ParamsQuery Dict // params:query block
	ParamsPath  Dict // params:path block

	BodyJSON        string // body:json raw text
	BodyText        string // body:text raw text
	BodyXML         string // body:xml raw text
	BodyFormURLEnc  Dict   // body:form-urlencoded
	BodyMultipart   Dict   // body:multipart-form
	BodyGraphQL     string // body:graphql query text
	BodyGraphQLVars string // body:graphql:vars JSON text

	AuthBasic  Dict // auth:basic: username, password
	AuthBearer Dict // auth:bearer: token
	AuthAPIKey Dict // auth:apikey: key, value, placement
	AuthOAuth2 Dict // auth:oauth2: grant_type, *_url, client_id, ...
	AuthDigest Dict // auth:digest

	VarsPreRequest   Dict // vars:pre-request
	VarsPostResponse Dict // vars:post-response

	ScriptPreRequest   string // script:pre-request JS, kept verbatim
	ScriptPostResponse string // script:post-response JS
	Tests              string // tests JS
	Docs               string // docs Markdown

	Assert Dict // assert block: expression -> expected

	// Settings is the settings block: encodeUrl, timeout,
	// followRedirects, maxRedirects.
	Settings Dict

	// WSMessages are the saved messages of a WebSocket request
	// (body:ws), in file order.
	WSMessages []WSMessage

	// Extra keeps blocks this package does not model, written back
	// verbatim so saving never drops what newer Bruno versions add.
	Extra []RawBlock
}

// WSMessage is one saved WebSocket message: a name, a kind (text, json,
// xml) and its body.
type WSMessage struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// IsWebSocket reports whether the request is a WebSocket one.
func (r *Request) IsWebSocket() bool { return strings.EqualFold(r.Meta.Type, "ws") }

// RawBlock is an unmodeled top-level block, content lines as in the
// file.
type RawBlock struct {
	Tag   string
	Open  byte // '{' or '['
	Lines []string
}

// Clone returns a deep copy: every dictionary and raw block is copied,
// so editing the copy never changes the original.
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	out := *r
	out.Headers = cloneDict(r.Headers)
	out.ParamsQuery = cloneDict(r.ParamsQuery)
	out.ParamsPath = cloneDict(r.ParamsPath)
	out.BodyFormURLEnc = cloneDict(r.BodyFormURLEnc)
	out.BodyMultipart = cloneDict(r.BodyMultipart)
	out.AuthBasic = cloneDict(r.AuthBasic)
	out.AuthBearer = cloneDict(r.AuthBearer)
	out.AuthAPIKey = cloneDict(r.AuthAPIKey)
	out.AuthOAuth2 = cloneDict(r.AuthOAuth2)
	out.AuthDigest = cloneDict(r.AuthDigest)
	out.VarsPreRequest = cloneDict(r.VarsPreRequest)
	out.VarsPostResponse = cloneDict(r.VarsPostResponse)
	out.Assert = cloneDict(r.Assert)
	out.Settings = cloneDict(r.Settings)
	out.Extra = make([]RawBlock, len(r.Extra))
	for i, rb := range r.Extra {
		out.Extra[i] = RawBlock{Tag: rb.Tag, Open: rb.Open, Lines: append([]string(nil), rb.Lines...)}
	}
	return &out
}

func cloneDict(d Dict) Dict {
	if d == nil {
		return nil
	}
	return append(Dict(nil), d...)
}

// UpperMethod returns the HTTP method for request lines: uppercase of
// the stored block name, defaulting to GET.
func (r *Request) UpperMethod() string {
	m := strings.ToUpper(r.Method)
	if m == "" {
		return "GET"
	}
	return m
}

// Environment is an environments/*.bru file: a name (from the file
// name), plain variables, and the list of secret names whose values
// live outside the collection.
type Environment struct {
	Name    string
	Vars    Dict
	Secrets []string
	// CRLF records the file's line endings for round-tripping; the
	// writer is in the store, which sets it on load.
	CRLF bool
}

// CollectionBru is the optional collection.bru file: collection-level
// headers, variables, auth and docs. It also parses folder.bru, which
// carries a meta block for the folder's name and order.
type CollectionBru struct {
	Meta               Meta
	Headers            Dict
	Vars               Dict
	Auth               string // selector like Request.Auth
	AuthBasic          Dict
	AuthBearer         Dict
	AuthAPIKey         Dict
	AuthOAuth2         Dict
	AuthDigest         Dict
	ScriptPreRequest   string
	ScriptPostResponse string
	Tests              string
	Docs               string
	Extra              []RawBlock
}
