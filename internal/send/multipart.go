package send

// multipart/form-data bodies in Bruno's notation: a field whose value is
// @file(a.png|b.pdf) sends those files, paths relative to the
// collection; a trailing @contentType(type) sets the part's type.

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

// multipartValue splits a field value into its text or files and an
// explicit content type.
func multipartValue(v string) (text string, files []string, contentType string) {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, "@contentType("); i >= 0 && strings.HasSuffix(v, ")") {
		contentType = strings.TrimSpace(v[i+len("@contentType(") : len(v)-1])
		v = strings.TrimSpace(v[:i])
	}
	if strings.HasPrefix(v, "@file(") && strings.HasSuffix(v, ")") {
		for _, f := range strings.Split(v[len("@file("):len(v)-1], "|") {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		return "", files, contentType
	}
	return v, nil, contentType
}

// IsFileValue reports whether a form value names files.
func IsFileValue(v string) bool {
	_, files, _ := multipartValue(v)
	return len(files) > 0
}

// encodeMultipart writes rows as multipart/form-data and returns the
// body and its Content-Type with the boundary.
func encodeMultipart(rows []Pair, base string) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range rows {
		text, files, ct := multipartValue(p.Value)
		if files == nil {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, escapeQuotes(p.Key)))
			if ct != "" {
				h.Set("Content-Type", ct)
			}
			part, err := w.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			part.Write([]byte(text))
			continue
		}
		for _, f := range files {
			path := f
			if !filepath.IsAbs(path) && base != "" {
				path = filepath.Join(base, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, "", fmt.Errorf("表单字段 %q: 读取文件失败: %v", p.Key, err)
			}
			typ := ct
			if typ == "" {
				typ = mime.TypeByExtension(filepath.Ext(path))
			}
			if typ == "" {
				typ = "application/octet-stream"
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
				escapeQuotes(p.Key), escapeQuotes(filepath.Base(path))))
			h.Set("Content-Type", typ)
			part, err := w.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			part.Write(data)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }
