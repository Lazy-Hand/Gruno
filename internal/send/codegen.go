package send

// Code generation: the request as it would go out — variables resolved,
// auth attached — written as a snippet in a few languages. OAuth2 tokens
// are fetched only at send time, so a snippet for an OAuth2 request
// carries a placeholder Authorization header.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

// CodeLangs are the languages GenerateCode writes, in menu order.
var CodeLangs = []string{"cURL", "JavaScript (fetch)", "Python (requests)", "Go (net/http)", "HTTPie"}

// codeRequest is what every generator needs.
type codeRequest struct {
	Method  string
	URL     string
	Headers [][2]string
	Body    string
	// Form is set for multipart bodies instead of Body: name and value,
	// or name and file path.
	Form []formField
}

type formField struct {
	Name, Value string
	File        bool
}

// GenerateCode writes input as lang (one of CodeLangs).
func GenerateCode(input SendInput, lang string) (string, error) {
	cr, err := buildCodeRequest(input)
	if err != nil {
		return "", err
	}
	switch lang {
	case "cURL":
		return genCurl(cr), nil
	case "JavaScript (fetch)":
		return genFetch(cr), nil
	case "Python (requests)":
		return genPython(cr), nil
	case "Go (net/http)":
		return genGo(cr), nil
	case "HTTPie":
		return genHTTPie(cr), nil
	}
	return "", fmt.Errorf("不支持的语言 %q", lang)
}

func buildCodeRequest(input SendInput) (codeRequest, error) {
	multipart := input.Request.Body.Mode == BodyMultipart
	built := input
	if multipart {
		// The snippet names the files; reading them here would fail for
		// files that exist only where the snippet runs.
		built.Request.Body = BodySpec{Mode: BodyNone}
	}
	req, err := prepareRequest(context.Background(), built)
	if err != nil {
		return codeRequest{}, err
	}
	cr := codeRequest{Method: req.Method, URL: req.URL.String()}
	if req.Host != "" && req.Host != req.URL.Host {
		cr.Headers = append(cr.Headers, [2]string{"Host", req.Host})
	}
	names := make([]string, 0, len(req.Header))
	for k := range req.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if multipart && k == "Content-Type" {
			continue // the tool writes its own boundary
		}
		for _, v := range req.Header[k] {
			cr.Headers = append(cr.Headers, [2]string{k, v})
		}
	}
	if input.Request.Auth.Mode == AuthOAuth2 {
		cr.Headers = append(cr.Headers, [2]string{"Authorization", "Bearer <OAuth2 access token>"})
	}
	if multipart {
		rows, err := newVarResolver(input.Scopes...).resolvePairs(input.Request.Body.Form)
		if err != nil {
			return codeRequest{}, err
		}
		for _, p := range rows {
			text, files, _ := multipartValue(p.Value)
			if files == nil {
				cr.Form = append(cr.Form, formField{Name: p.Key, Value: text})
				continue
			}
			for _, f := range files {
				if !filepath.IsAbs(f) && input.Request.Body.FileBase != "" {
					f = filepath.Join(input.Request.Body.FileBase, f)
				}
				cr.Form = append(cr.Form, formField{Name: p.Key, Value: f, File: true})
			}
		}
		return cr, nil
	}
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return codeRequest{}, err
		}
		cr.Body = string(data)
	}
	return cr, nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@,+%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func genCurl(r codeRequest) string {
	var b strings.Builder
	b.WriteString("curl")
	// curl infers GET without a body and POST with one.
	hasBody := r.Body != "" || r.Form != nil
	if !(r.Method == http.MethodGet && !hasBody) && !(r.Method == http.MethodPost && hasBody) {
		fmt.Fprintf(&b, " -X %s", r.Method)
	}
	fmt.Fprintf(&b, " %s", shellQuote(r.URL))
	for _, h := range r.Headers {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(h[0]+": "+h[1]))
	}
	for _, f := range r.Form {
		if f.File {
			fmt.Fprintf(&b, " \\\n  -F %s", shellQuote(f.Name+"=@"+f.Value))
		} else {
			fmt.Fprintf(&b, " \\\n  -F %s", shellQuote(f.Name+"="+f.Value))
		}
	}
	if r.Body != "" {
		fmt.Fprintf(&b, " \\\n  --data-raw %s", shellQuote(r.Body))
	}
	return b.String()
}

func jsString(s string) string {
	q, _ := json.Marshal(s)
	return string(q)
}

func genFetch(r codeRequest) string {
	var b strings.Builder
	if r.Form != nil {
		b.WriteString("// Files must be read in the browser (an <input type=file>) or with Node's fs.\n")
		b.WriteString("const form = new FormData();\n")
		for _, f := range r.Form {
			if f.File {
				fmt.Fprintf(&b, "form.append(%s, await fileFrom(%s));\n", jsString(f.Name), jsString(f.Value))
			} else {
				fmt.Fprintf(&b, "form.append(%s, %s);\n", jsString(f.Name), jsString(f.Value))
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "const response = await fetch(%s, {\n", jsString(r.URL))
	fmt.Fprintf(&b, "  method: %s,\n", jsString(r.Method))
	if len(r.Headers) > 0 {
		b.WriteString("  headers: {\n")
		for _, h := range r.Headers {
			fmt.Fprintf(&b, "    %s: %s,\n", jsString(h[0]), jsString(h[1]))
		}
		b.WriteString("  },\n")
	}
	switch {
	case r.Form != nil:
		b.WriteString("  body: form,\n")
	case r.Body != "":
		fmt.Fprintf(&b, "  body: %s,\n", jsString(r.Body))
	}
	b.WriteString("});\n\nconsole.log(response.status, await response.text());\n")
	return b.String()
}

func pyString(s string) string {
	q, _ := json.Marshal(s) // JSON strings are valid Python literals
	return string(q)
}

func genPython(r codeRequest) string {
	var b strings.Builder
	b.WriteString("import requests\n\n")
	fmt.Fprintf(&b, "url = %s\n", pyString(r.URL))
	if len(r.Headers) > 0 {
		b.WriteString("headers = {\n")
		for _, h := range r.Headers {
			fmt.Fprintf(&b, "    %s: %s,\n", pyString(h[0]), pyString(h[1]))
		}
		b.WriteString("}\n")
	}
	args := []string{pyString(r.Method), "url"}
	if len(r.Headers) > 0 {
		args = append(args, "headers=headers")
	}
	if r.Form != nil {
		b.WriteString("data = {\n")
		for _, f := range r.Form {
			if !f.File {
				fmt.Fprintf(&b, "    %s: %s,\n", pyString(f.Name), pyString(f.Value))
			}
		}
		b.WriteString("}\nfiles = [\n")
		for _, f := range r.Form {
			if f.File {
				fmt.Fprintf(&b, "    (%s, open(%s, \"rb\")),\n", pyString(f.Name), pyString(f.Value))
			}
		}
		b.WriteString("]\n")
		args = append(args, "data=data", "files=files")
	} else if r.Body != "" {
		fmt.Fprintf(&b, "payload = %s\n", pyString(r.Body))
		args = append(args, "data=payload.encode()")
	}
	fmt.Fprintf(&b, "\nresponse = requests.request(%s)\nprint(response.status_code, response.text)\n", strings.Join(args, ", "))
	return b.String()
}

func genGo(r codeRequest) string {
	var b strings.Builder
	imports := []string{`"fmt"`, `"io"`, `"net/http"`}
	if r.Body != "" {
		imports = append(imports, `"strings"`)
	}
	if r.Form != nil {
		imports = append(imports, `"bytes"`, `"mime/multipart"`, `"os"`, `"path/filepath"`)
	}
	sort.Strings(imports)
	b.WriteString("package main\n\nimport (\n")
	for _, im := range imports {
		fmt.Fprintf(&b, "\t%s\n", im)
	}
	b.WriteString(")\n\nfunc main() {\n")
	body := "nil"
	switch {
	case r.Form != nil:
		b.WriteString("\tvar buf bytes.Buffer\n\tw := multipart.NewWriter(&buf)\n")
		for _, f := range r.Form {
			if f.File {
				fmt.Fprintf(&b, "\tif data, err := os.ReadFile(%s); err == nil {\n", jsString(f.Value))
				fmt.Fprintf(&b, "\t\tpart, _ := w.CreateFormFile(%s, filepath.Base(%s))\n\t\tpart.Write(data)\n\t}\n", jsString(f.Name), jsString(f.Value))
			} else {
				fmt.Fprintf(&b, "\tw.WriteField(%s, %s)\n", jsString(f.Name), jsString(f.Value))
			}
		}
		b.WriteString("\tw.Close()\n\n")
		body = "&buf"
	case r.Body != "":
		fmt.Fprintf(&b, "\tbody := strings.NewReader(%s)\n", "`"+strings.ReplaceAll(r.Body, "`", "` + \"`\" + `")+"`")
		body = "body"
	}
	fmt.Fprintf(&b, "\treq, err := http.NewRequest(%s, %s, %s)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n", jsString(r.Method), jsString(r.URL), body)
	for _, h := range r.Headers {
		fmt.Fprintf(&b, "\treq.Header.Add(%s, %s)\n", jsString(h[0]), jsString(h[1]))
	}
	if r.Form != nil {
		b.WriteString("\treq.Header.Set(\"Content-Type\", w.FormDataContentType())\n")
	}
	b.WriteString("\n\tresp, err := http.DefaultClient.Do(req)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n")
	b.WriteString("\tdefer resp.Body.Close()\n\tdata, _ := io.ReadAll(resp.Body)\n\tfmt.Println(resp.Status, string(data))\n}\n")
	return b.String()
}

func genHTTPie(r codeRequest) string {
	var b strings.Builder
	b.WriteString("http")
	if r.Form != nil {
		b.WriteString(" --multipart")
	}
	fmt.Fprintf(&b, " %s %s", r.Method, shellQuote(r.URL))
	for _, h := range r.Headers {
		fmt.Fprintf(&b, " \\\n  %s", shellQuote(h[0]+":"+h[1]))
	}
	for _, f := range r.Form {
		if f.File {
			fmt.Fprintf(&b, " \\\n  %s", shellQuote(f.Name+"@"+f.Value))
		} else {
			fmt.Fprintf(&b, " \\\n  %s", shellQuote(f.Name+"="+f.Value))
		}
	}
	if r.Body != "" {
		fmt.Fprintf(&b, " \\\n  --raw %s", shellQuote(r.Body))
	}
	return b.String()
}
