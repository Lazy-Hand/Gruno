package bru

import (
	"fmt"
	"strconv"
	"strings"
)

// block is a top-level chunk of a .bru file.
type block struct {
	tag   string // "meta", "body:json", "vars:secret", "get", ...
	lines []string
	open  byte // '{' or '['
}

// splitBlocks parses the file into its top-level blocks. A block opens
// with "tag {" or "tag [" at column 0; the matching closer sits alone
// on a line at column 0. Content lines keep their raw indentation minus
// the conventional two spaces.
func splitBlocks(src string) ([]block, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var blocks []block
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}
		// A block header must start at column 0 and end with { or [.
		if line != strings.TrimLeft(line, " \t") {
			return nil, fmt.Errorf("line %d: unexpected indentation before block: %q", i+1, line)
		}
		trimmed := strings.TrimRight(line, " \t")
		var tag string
		var open byte
		if strings.HasSuffix(trimmed, "{") {
			tag, open = strings.TrimSpace(trimmed[:len(trimmed)-1]), '{'
		} else if strings.HasSuffix(trimmed, "[") {
			tag, open = strings.TrimSpace(trimmed[:len(trimmed)-1]), '['
		} else {
			return nil, fmt.Errorf("line %d: expected block header, got %q", i+1, line)
		}
		if tag == "" {
			return nil, fmt.Errorf("line %d: empty block name", i+1)
		}
		close := "}"
		if open == '[' {
			close = "]"
		}
		i++
		var content []string
		for ; i < len(lines); i++ {
			l := lines[i]
			if l == close {
				break
			}
			content = append(content, l)
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("block %q opened but never closed", tag)
		}
		i++ // skip the closer
		blocks = append(blocks, block{tag: tag, lines: content, open: open})
	}
	return blocks, nil
}

// unindent removes the conventional two-space indent from content
// lines. Lines with less indent (an empty line) pass through.
func unindent(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimPrefix(l, "  ")
	}
	return out
}

// parseDict turns block content lines into entries: "key: value",
// "~key: value" for disabled. Blank lines are skipped.
func parseDict(lines []string) Dict {
	var d Dict
	for _, l := range unindent(lines) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		enabled := true
		if strings.HasPrefix(l, "~") {
			enabled = false
			l = l[1:]
		}
		k, v, _ := strings.Cut(l, ":")
		d = append(d, Entry{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v), Enabled: enabled})
	}
	return d
}

// parseText keeps block content verbatim, minus the two-space indent.
func parseText(lines []string) string {
	u := unindent(lines)
	// Drop leading/trailing blank lines inside the block.
	for len(u) > 0 && strings.TrimSpace(u[0]) == "" {
		u = u[1:]
	}
	for len(u) > 0 && strings.TrimSpace(u[len(u)-1]) == "" {
		u = u[:len(u)-1]
	}
	return strings.Join(u, "\n")
}

// parseMeta reads a meta block, including the tags array.
func parseMeta(lines []string) Meta {
	var m Meta
	inTags := false
	for _, l := range unindent(lines) {
		t := strings.TrimSpace(l)
		if inTags {
			if t == "]" {
				inTags = false
				continue
			}
			if t != "" {
				m.Tags = append(m.Tags, t)
			}
			continue
		}
		if strings.HasPrefix(t, "tags:") && strings.HasSuffix(t, "[") {
			inTags = true
			continue
		}
		k, v, _ := strings.Cut(t, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "name":
			m.Name = v
		case "type":
			m.Type = v
		case "seq":
			if n, err := strconv.Atoi(v); err == nil {
				m.Seq = n
			}
		}
	}
	return m
}

// parseWSMessages reads the nested message blocks of body:ws. Each is
// "name [kind] {" at the block's indentation, its body indented one
// level deeper, and a closing brace.
func parseWSMessages(lines []string) []WSMessage {
	var out []WSMessage
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		t := strings.TrimSpace(raw)
		if t == "" || !strings.HasSuffix(t, "{") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		header := strings.TrimSpace(strings.TrimSuffix(t, "{"))
		name, kind := header, "text"
		if j := strings.LastIndex(header, "["); j >= 0 && strings.HasSuffix(header, "]") {
			kind = strings.TrimSpace(header[j+1 : len(header)-1])
			name = strings.TrimSpace(header[:j])
		}
		var body []string
		j := i + 1
		for ; j < len(lines); j++ {
			line := lines[j]
			// Only a brace at the message's own indentation closes it; a
			// pretty-printed body may contain braces one level deeper.
			if strings.TrimSpace(line) == "}" && (len(line)-len(strings.TrimLeft(line, " "))) <= indent {
				break
			}
			if pad := indent + 2; len(line) >= pad && strings.TrimSpace(line[:pad]) == "" {
				line = line[pad:]
			}
			body = append(body, line)
		}
		for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
			body = body[1:]
		}
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		out = append(out, WSMessage{Name: name, Kind: kind, Body: strings.Join(body, "\n")})
		i = j
	}
	return out
}

var methodTags = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "options": true, "head": true, "trace": true, "connect": true,
}

// ParseRequest decodes one request .bru file.
func ParseRequest(src string) (*Request, error) {
	blocks, err := splitBlocks(src)
	if err != nil {
		return nil, err
	}
	r := &Request{Method: "get", Body: "none", Auth: "none"}
	for _, b := range blocks {
		switch b.tag {
		case "meta":
			r.Meta = parseMeta(b.lines)
		case "ws":
			for _, e := range parseDict(b.lines) {
				if e.Key == "url" {
					r.URL = e.Value
				}
			}
		case "body:ws":
			r.WSMessages = parseWSMessages(b.lines)
		case "body":
			// Bruno's canonical JSON body block (body { ... }).
			r.BodyJSON = parseText(b.lines)
		case "headers":
			r.Headers = parseDict(b.lines)
		case "params", "params:query", "query":
			r.ParamsQuery = parseDict(b.lines)
		case "params:path":
			r.ParamsPath = parseDict(b.lines)
		case "body:json":
			r.BodyJSON = parseText(b.lines)
		case "body:text":
			r.BodyText = parseText(b.lines)
		case "body:xml":
			r.BodyXML = parseText(b.lines)
		case "body:form-urlencoded":
			r.BodyFormURLEnc = parseDict(b.lines)
		case "body:multipart-form":
			r.BodyMultipart = parseDict(b.lines)
		case "body:graphql":
			r.BodyGraphQL = parseText(b.lines)
		case "body:graphql:vars":
			r.BodyGraphQLVars = parseText(b.lines)
		case "auth:basic":
			r.AuthBasic = parseDict(b.lines)
		case "auth:bearer":
			r.AuthBearer = parseDict(b.lines)
		case "auth:apikey":
			r.AuthAPIKey = parseDict(b.lines)
		case "auth:oauth2":
			r.AuthOAuth2 = parseDict(b.lines)
		case "auth:digest":
			r.AuthDigest = parseDict(b.lines)
		case "vars", "vars:pre-request":
			// "vars" is the older request-variable block; keep it as the
			// pre-request scope so values survive a save.
			r.VarsPreRequest = parseDict(b.lines)
		case "vars:post-response":
			r.VarsPostResponse = parseDict(b.lines)
		case "script:pre-request":
			r.ScriptPreRequest = parseText(b.lines)
		case "script:post-response":
			r.ScriptPostResponse = parseText(b.lines)
		case "tests":
			r.Tests = parseText(b.lines)
		case "docs":
			r.Docs = parseText(b.lines)
		case "assert":
			r.Assert = parseDict(b.lines)
		case "settings":
			r.Settings = parseDict(b.lines)
		default:
			if methodTags[b.tag] {
				r.Method = b.tag
				for _, e := range parseDict(b.lines) {
					switch e.Key {
					case "url":
						r.URL = e.Value
					case "body":
						r.Body = e.Value
					case "auth":
						r.Auth = e.Value
					}
				}
				continue
			}
			// Unknown blocks survive a save untouched.
			r.Extra = append(r.Extra, RawBlock{Tag: b.tag, Open: b.open, Lines: b.lines})
		}
	}
	return r, nil
}

// ParseEnvironment decodes an environments/*.bru file.
func ParseEnvironment(src string, name string) (*Environment, error) {
	blocks, err := splitBlocks(src)
	if err != nil {
		return nil, err
	}
	e := &Environment{Name: name}
	for _, b := range blocks {
		switch b.tag {
		case "vars":
			e.Vars = parseDict(b.lines)
		case "vars:secret":
			for _, l := range unindent(b.lines) {
				if t := strings.TrimSpace(l); t != "" {
					e.Secrets = append(e.Secrets, t)
				}
			}
		}
	}
	return e, nil
}

// ParseCollection decodes collection.bru.
func ParseCollection(src string) (*CollectionBru, error) {
	blocks, err := splitBlocks(src)
	if err != nil {
		return nil, err
	}
	cb := &CollectionBru{Auth: "none"}
	for _, b := range blocks {
		switch b.tag {
		case "headers":
			cb.Headers = parseDict(b.lines)
		case "vars":
			cb.Vars = parseDict(b.lines)
		case "auth":
			for _, e := range parseDict(b.lines) {
				if e.Key == "mode" {
					cb.Auth = e.Value
				}
			}
		case "auth:basic":
			cb.AuthBasic = parseDict(b.lines)
		case "auth:bearer":
			cb.AuthBearer = parseDict(b.lines)
		case "auth:apikey":
			cb.AuthAPIKey = parseDict(b.lines)
		case "auth:oauth2":
			cb.AuthOAuth2 = parseDict(b.lines)
		case "meta":
			cb.Meta = parseMeta(b.lines)
		case "auth:digest":
			cb.AuthDigest = parseDict(b.lines)
		case "script:pre-request":
			cb.ScriptPreRequest = parseText(b.lines)
		case "script:post-response":
			cb.ScriptPostResponse = parseText(b.lines)
		case "tests":
			cb.Tests = parseText(b.lines)
		case "docs":
			cb.Docs = parseText(b.lines)
		default:
			cb.Extra = append(cb.Extra, RawBlock{Tag: b.tag, Open: b.open, Lines: b.lines})
		}
	}
	return cb, nil
}
