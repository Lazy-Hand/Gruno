package bru

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SerializeRequest renders a Request back to .bru text in Bruno's
// block order: meta, the method block, headers, params, body, auth,
// vars, scripts, tests, docs, assert. Empty blocks are omitted.
func SerializeRequest(r *Request) string {
	var b strings.Builder

	writeDict(&b, "meta", Dict{
		{Key: "name", Value: r.Meta.Name, Enabled: true},
		{Key: "type", Value: orDefault(r.Meta.Type, "http"), Enabled: true},
		{Key: "seq", Value: strconv.Itoa(r.Meta.Seq), Enabled: true},
	})

	method := strings.ToLower(orDefault(r.Method, "get"))
	writeDict(&b, method, Dict{
		{Key: "url", Value: r.URL, Enabled: true},
		{Key: "body", Value: orDefault(r.Body, "none"), Enabled: true},
		{Key: "auth", Value: orDefault(r.Auth, "none"), Enabled: true},
	})

	writeDict(&b, "headers", r.Headers)
	writeDict(&b, "params:query", r.ParamsQuery)
	writeDict(&b, "params:path", r.ParamsPath)

	writeText(&b, "body:json", r.BodyJSON)
	writeText(&b, "body:text", r.BodyText)
	writeText(&b, "body:xml", r.BodyXML)
	writeDict(&b, "body:form-urlencoded", r.BodyFormURLEnc)
	writeDict(&b, "body:multipart-form", r.BodyMultipart)
	writeText(&b, "body:graphql", r.BodyGraphQL)
	writeText(&b, "body:graphql:vars", r.BodyGraphQLVars)

	writeDict(&b, "auth:basic", r.AuthBasic)
	writeDict(&b, "auth:bearer", r.AuthBearer)
	writeDict(&b, "auth:apikey", r.AuthAPIKey)
	writeDict(&b, "auth:oauth2", r.AuthOAuth2)
	writeDict(&b, "auth:digest", r.AuthDigest)

	writeDict(&b, "vars:pre-request", r.VarsPreRequest)
	writeDict(&b, "vars:post-response", r.VarsPostResponse)
	writeText(&b, "script:pre-request", r.ScriptPreRequest)
	writeText(&b, "script:post-response", r.ScriptPostResponse)
	writeText(&b, "tests", r.Tests)
	writeText(&b, "docs", r.Docs)
	writeDict(&b, "assert", r.Assert)

	return b.String()
}

// SerializeEnvironment renders an Environment: vars then vars:secret.
func SerializeEnvironment(e *Environment) string {
	var b strings.Builder
	writeDict(&b, "vars", e.Vars)
	if len(e.Secrets) > 0 {
		b.WriteString("vars:secret [\n")
		for _, s := range e.Secrets {
			b.WriteString("  " + s + "\n")
		}
		b.WriteString("]\n")
	}
	return b.String()
}

// SerializeCollection renders collection.bru.
func SerializeCollection(cb *CollectionBru) string {
	var b strings.Builder
	if cb.Meta.Name != "" {
		writeDict(&b, "meta", Dict{
			{Key: "name", Value: cb.Meta.Name, Enabled: true},
			{Key: "seq", Value: strconv.Itoa(cb.Meta.Seq), Enabled: true},
		})
	}
	writeDict(&b, "headers", cb.Headers)
	writeDict(&b, "vars", cb.Vars)
	if cb.Auth != "" && cb.Auth != "none" {
		writeDict(&b, "auth", Dict{{Key: "mode", Value: cb.Auth, Enabled: true}})
	}
	writeDict(&b, "auth:basic", cb.AuthBasic)
	writeDict(&b, "auth:bearer", cb.AuthBearer)
	writeDict(&b, "auth:apikey", cb.AuthAPIKey)
	writeDict(&b, "auth:oauth2", cb.AuthOAuth2)
	writeDict(&b, "auth:digest", cb.AuthDigest)
	writeText(&b, "script:pre-request", cb.ScriptPreRequest)
	writeText(&b, "script:post-response", cb.ScriptPostResponse)
	writeText(&b, "tests", cb.Tests)
	writeText(&b, "docs", cb.Docs)
	return b.String()
}

// writeDict writes "tag {" then indented "key: value" lines then "}".
// Disabled entries get a leading ~. Nothing is written for empty dicts.
func writeDict(b *strings.Builder, tag string, d Dict) {
	if len(d) == 0 {
		return
	}
	fmt.Fprintf(b, "%s {\n", tag)
	for _, e := range d {
		prefix := "  "
		if !e.Enabled {
			prefix = "  ~"
		}
		if e.Value == "" {
			fmt.Fprintf(b, "%s%s\n", prefix, e.Key)
		} else {
			fmt.Fprintf(b, "%s%s: %s\n", prefix, e.Key, e.Value)
		}
	}
	b.WriteString("}\n\n")
}

// writeText writes a text block: every line indented two spaces so
// embedded braces (JSON objects) can't be mistaken for the closer.
// Empty text writes nothing.
func writeText(b *strings.Builder, tag string, text string) {
	if text == "" {
		return
	}
	fmt.Fprintf(b, "%s {\n", tag)
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("}\n\n")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// sortEntries returns enabled entries of d, file order preserved;
// used when flattening vars for resolution.
func sortEntries(d Dict) []Entry {
	out := make([]Entry, 0, len(d))
	for _, e := range d {
		if e.Enabled {
			out = append(out, e)
		}
	}
	return out
}

// VarsMap flattens several dictionaries into one map; later sources
// override earlier ones.
func VarsMap(dicts ...Dict) map[string]string {
	m := map[string]string{}
	for _, d := range dicts {
		for _, e := range sortEntries(d) {
			m[e.Key] = e.Value
		}
	}
	return m
}

// SortBySeq orders names by the seq in each file's meta; entries with
// equal or zero seq sort by name.
func SortBySeq(files []*Request, nameOf func(*Request) string) {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Meta.Seq != files[j].Meta.Seq {
			return files[i].Meta.Seq < files[j].Meta.Seq
		}
		return nameOf(files[i]) < nameOf(files[j])
	})
}
