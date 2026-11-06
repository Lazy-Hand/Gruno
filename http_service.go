package main

// HTTPService executes requests built by prepareRequest and streams the
// response head back through a channel before the bounded body read.
// It is the only network path; the page's fetch is never used, so CORS
// does not apply and Set-Cookie headers survive intact.

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/netproxy"
)

// HTTPService holds the shared client. Transport follows the system
// proxy; TLS uses system trust with no "ignore certificate" switch.
type HTTPService struct {
	client *http.Client
}

// newHTTPService builds the service with in-memory dependencies only.
func newHTTPService() *HTTPService {
	return &HTTPService{client: &http.Client{
		Transport: netproxy.Transport(),
		Timeout:   30 * time.Second,
	}}
}

// send executes the request: onHead receives the head once, errors are
// classified into ResponseResult, and only internal failures return a
// Go error.
func (s *HTTPService) send(ctx context.Context, input SendInput, onHead func(ResponseHead) error) (ResponseResult, error) {
	start := time.Now()
	elapsed := func() float64 { return float64(time.Since(start)) / float64(time.Millisecond) }
	fail := func(code ErrorCode, msg string) ResponseResult {
		return ResponseResult{
			BodyKind:     BodyKindText,
			ErrorCode:    code,
			ErrorMessage: msg,
			DurationMs:   elapsed(),
		}
	}

	req, err := prepareRequest(ctx, input)
	if err != nil {
		return fail(ErrInvalidReq, err.Error()), nil
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fail(classifyDoError(ctx, err), sanitizeRequestError(err)), nil
	}

	// Digest: answer a 401 challenge with a computed second request.
	if input.Request.Auth.Mode == AuthDigest {
		if retry, rerr := digestRetry(req, resp, input.Request.Auth); rerr != nil {
			resp.Body.Close()
			return fail(ErrInvalidReq, rerr.Error()), nil
		} else if retry != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			resp, err = s.client.Do(retry)
			if err != nil {
				return fail(classifyDoError(ctx, err), sanitizeRequestError(err)), nil
			}
		}
	}
	defer resp.Body.Close()

	head := ResponseHead{
		StatusCode:    resp.StatusCode,
		StatusText:    statusText(resp),
		URL:           resp.Request.URL.String(),
		Protocol:      resp.Proto,
		Headers:       responseHeaderPairs(resp),
		ContentLength: resp.ContentLength,
	}
	if err := onHead(head); err != nil {
		return ResponseResult{}, err // bridge failure: report as Go error
	}

	data, truncated, readErr := readBody(resp)
	if readErr != nil {
		return fail(classifyReadError(ctx, readErr), sanitizeRequestError(readErr)), nil
	}

	kind, text := classifyBody(resp, data, truncated)
	if truncated && kind == BodyKindText {
		// The cut may have landed inside a rune; drop the partial tail.
		text = strings.ToValidUTF8(text, "")
	}
	return ResponseResult{
		BodyKind:   kind,
		BodyText:   text,
		SizeBytes:  int64(len(data)),
		Truncated:  truncated,
		DurationMs: elapsed(),
	}, nil
}

// statusText strips the numeric code from resp.Status ("200 OK" → "OK").
func statusText(resp *http.Response) string {
	code := strconv.Itoa(resp.StatusCode)
	return strings.TrimPrefix(resp.Status, code+" ")
}

// responseHeaderPairs flattens all response headers, preserving repeated
// values including Set-Cookie. resp.Header is already a multimap; order
// within a name is kept, names are sorted for determinism.
func responseHeaderPairs(resp *http.Response) []Pair {
	names := make([]string, 0, len(resp.Header))
	for name := range resp.Header {
		names = append(names, name)
	}
	// sort.Strings would need the import; insertion order of a Go map is
	// random, so sort for stable output.
	sortStrings(names)
	pairs := []Pair{}
	for _, name := range names {
		for _, value := range resp.Header[name] {
			pairs = append(pairs, Pair{Key: name, Value: value, Enabled: true})
		}
	}
	return pairs
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// readBody reads at most maxResponseBody+1 bytes so a truncated flag can
// be set without downloading the rest. The probe byte is discarded.
func readBody(resp *http.Response) (data []byte, truncated bool, err error) {
	lr := io.LimitReader(resp.Body, maxResponseBody+1)
	data, err = io.ReadAll(lr)
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxResponseBody {
		return data[:maxResponseBody], true, nil
	}
	return data, false, nil
}

// classifyBody decides how data is displayed. Undecoded content
// encodings (the transport only auto-decodes gzip it requested itself)
// and non-text types stay binary; declared non-UTF-8 charsets are
// reported, never mojibake'd.
func classifyBody(resp *http.Response, data []byte, truncated bool) (BodyKind, string) {
	if len(data) == 0 {
		return BodyKindText, ""
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") && !resp.Uncompressed {
		return BodyKindBinary, ""
	}

	mediaType, params := parseContentType(resp, data)
	if !isTextual(mediaType) {
		return BodyKindBinary, ""
	}
	if cs, ok := params["charset"]; ok {
		cs = strings.ToLower(strings.TrimSpace(strings.Trim(cs, `"`)))
		if cs != "utf-8" && cs != "utf8" && cs != "us-ascii" && cs != "ascii" {
			return BodyKindUnsupportedCharset, ""
		}
	}
	if !utf8.Valid(data) {
		if truncated {
			// A cut may end mid-rune; allow dropping up to 3 tail bytes.
			for n := 1; n <= 3 && len(data) > n; n++ {
				if utf8.Valid(data[:len(data)-n]) {
					data = data[:len(data)-n]
					break
				}
			}
		}
		if !utf8.Valid(data) {
			return BodyKindUnsupportedCharset, ""
		}
	}
	return BodyKindText, string(data)
}

// parseContentType returns the media type and params, falling back to
// sniffing when Content-Type is absent.
func parseContentType(resp *http.Response, data []byte) (string, map[string]string) {
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		mt, params, err := mime.ParseMediaType(ct)
		if err == nil {
			return strings.ToLower(mt), params
		}
		// Unparseable header: treat the raw value as the type.
		return strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0])), map[string]string{}
	}
	sniff := http.DetectContentType(data)
	mt, params, err := mime.ParseMediaType(sniff)
	if err != nil {
		return strings.ToLower(sniff), map[string]string{}
	}
	return strings.ToLower(mt), params
}

// isTextual reports whether a media type is displayable text: text/*,
// JSON and XML including structured-syntax suffixes.
func isTextual(mt string) bool {
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch {
	case mt == "application/json", strings.HasSuffix(mt, "+json"):
		return true
	case mt == "application/xml", strings.HasSuffix(mt, "+xml"):
		return true
	case mt == "application/javascript", mt == "application/ecmascript",
		mt == "application/x-javascript", mt == "image/svg+xml":
		return true
	}
	return false
}

// classifyDoError maps a client.Do failure: user/bridge cancellation,
// timeouts (including the client's own 30s deadline), then network.
func classifyDoError(ctx context.Context, err error) ErrorCode {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ErrCanceled
		}
		return ErrTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	return ErrNetwork
}

// classifyReadError maps a body-read failure the same way; anything
// else is a body-read problem.
func classifyReadError(ctx context.Context, err error) ErrorCode {
	code := classifyDoError(ctx, err)
	if code == ErrNetwork {
		return ErrBodyRead
	}
	return code
}
