package send

// HTTPService executes requests built by prepareRequest and streams the
// response head back through a channel before the bounded body read.
// It is the only network path; the page's fetch is never used, so CORS
// does not apply and Set-Cookie headers survive intact.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// HTTPService sends requests. Each send gets a client for its
// Settings; transports are shared between sends with equal TLS and
// proxy settings, so connections are reused.
type HTTPService struct {
	transports transportCache
	tokens     tokenCache
	// Cookies keeps Set-Cookie responses and sends them back, unless a
	// send's Settings.NoCookies opts out.
	Cookies *CookieJar
	// OpenURL opens an authorization page in the browser for an OAuth2
	// authorization_code flow. The app sets it; nil fails the flow with
	// a clear error.
	OpenURL func(string) error
}

// NewHTTPService builds the service with in-memory dependencies only.
func NewHTTPService() *HTTPService {
	return &HTTPService{Cookies: NewCookieJar()}
}

// Send executes the request: onHead receives the head once, errors are
// classified into ResponseResult, and only internal failures return a
// Go error.
func (s *HTTPService) Send(ctx context.Context, input SendInput, onHead func(ResponseHead) error) (ResponseResult, error) {
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

	resolver := newVarResolver(input.Scopes...)
	clientFor := func(u *url.URL) (*http.Client, error) { return s.clientFor(input.Settings, u, resolver) }

	var notes []string
	var attach func(*http.Request) error
	if input.Request.Auth.Mode == AuthOAuth2 {
		var note string
		var err error
		attach, note, err = s.oauth2Attach(ctx, input, clientFor)
		if err != nil {
			if ctx.Err() != nil {
				return fail(classifyDoError(ctx, ctx.Err()), err.Error()), nil
			}
			return fail(ErrAuth, err.Error()), nil
		}
		notes = append(notes, note)
	}

	req, err := prepareRequest(ctx, input)
	if err != nil {
		return fail(ErrInvalidReq, err.Error()), nil
	}
	if attach != nil {
		if err := attach(req); err != nil {
			return fail(ErrAuth, err.Error()), nil
		}
	}

	client, err := clientFor(req.URL)
	if err != nil {
		return fail(ErrInvalidReq, err.Error()), nil
	}

	sent := req
	// Digest credentials are hashed on challenge, after resolution.
	var digestUser, digestPass string
	if input.Request.Auth.Mode == AuthDigest {
		var rerr error
		if digestUser, rerr = resolver.resolveString(input.Request.Auth.Username); rerr != nil {
			return fail(ErrAuth, rerr.Error()), nil
		}
		if digestPass, rerr = resolver.resolveString(input.Request.Auth.Password); rerr != nil {
			return fail(ErrAuth, rerr.Error()), nil
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail(classifyDoError(ctx, err), sanitizeRequestError(err)), nil
	}

	// Digest: answer a 401 challenge with a computed second request.
	if input.Request.Auth.Mode == AuthDigest {
		if retry, rerr := digestRetry(req, resp, digestUser, digestPass); rerr != nil {
			resp.Body.Close()
			return fail(ErrInvalidReq, rerr.Error()), nil
		} else if retry != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			sent = retry
			resp, err = client.Do(retry)
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
		Sent:          sentRequestOf(sent),
	}
	if err := onHead(head); err != nil {
		return ResponseResult{}, err // bridge failure: report as Go error
	}

	var onChunk func(string)
	if input.OnBody != nil && streamable(resp) {
		onChunk = input.OnBody
	}
	data, truncated, readErr := readBody(resp, onChunk)
	if readErr != nil {
		code := classifyReadError(ctx, readErr)
		res := fail(code, sanitizeRequestError(readErr))
		// Keep what arrived: a canceled stream still shows its events.
		if len(data) > 0 {
			res.BodyKind, res.BodyText = classifyBody(resp, data, truncated)
			if res.BodyKind == BodyKindText {
				res.BodyText = strings.ToValidUTF8(res.BodyText, "")
			}
			res.SizeBytes = int64(len(data))
			res.Truncated = truncated
			if code == ErrCanceled {
				res.Notes = append(res.Notes, "流已取消，保留已接收的数据")
			}
		}
		return res, nil
	}

	kind, text := classifyBody(resp, data, truncated)
	if truncated && kind == BodyKindText {
		// The cut may have landed inside a rune; drop the partial tail.
		text = strings.ToValidUTF8(text, "")
	}
	full := int64(len(data))
	if truncated && resp.ContentLength > full {
		full = resp.ContentLength
	}
	return ResponseResult{
		BodyKind:   kind,
		BodyText:   text,
		SizeBytes:  int64(len(data)),
		FullSize:   full,
		Truncated:  truncated,
		DurationMs: elapsed(),
		Notes:      notes,
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
	// Map iteration order is random; sort for stable output.
	sort.Strings(names)
	pairs := []Pair{}
	for _, name := range names {
		for _, value := range resp.Header[name] {
			pairs = append(pairs, Pair{Key: name, Value: value, Enabled: true})
		}
	}
	return pairs
}

// readBody reads at most maxResponseBody bytes (setting truncated when
// there is more) and hands text chunks to onChunk as they arrive, so a
// live stream can be shown while it runs.
func readBody(resp *http.Response, onChunk func(string)) (data []byte, truncated bool, err error) {
	var all bytes.Buffer
	buf := make([]byte, 32<<10)
	var carry []byte
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if all.Len() < maxResponseBody {
				take := n
				if rem := maxResponseBody - all.Len(); take > rem {
					take = rem
					truncated = true
				}
				all.Write(buf[:take])
			} else {
				truncated = true
			}
			if onChunk != nil && !truncated {
				chunk := append(carry, buf[:n]...)
				cut := len(chunk)
				if !utf8.Valid(chunk) {
					// A chunk may end mid-rune: hold back its tail for
					// the next read.
					for drop := 1; drop <= 3 && drop < len(chunk); drop++ {
						if utf8.Valid(chunk[:len(chunk)-drop]) {
							cut = len(chunk) - drop
							break
						}
					}
				}
				if !utf8.Valid(chunk[:cut]) {
					cut = len(chunk) // interior damage; show it as it is
				}
				carry = append(carry[:0], chunk[cut:]...)
				if cut > 0 {
					onChunk(string(chunk[:cut]))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return all.Bytes(), truncated, rerr
		}
	}
	if len(carry) > 0 && onChunk != nil && utf8.Valid(carry) {
		onChunk(string(carry))
	}
	return all.Bytes(), truncated, nil
}

// streamable reports whether the response is live text worth handing to
// the caller chunk by chunk (text/event-stream and other text bodies).
func streamable(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || !isTextual(strings.ToLower(mt)) {
		return false
	}
	if cs, ok := params["charset"]; ok {
		cs = strings.ToLower(strings.TrimSpace(strings.Trim(cs, `"`)))
		if cs != "utf-8" && cs != "utf8" && cs != "us-ascii" && cs != "ascii" {
			return false
		}
	}
	return true
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

// sentRequestOf captures what the client sent, headers sorted by name.
func sentRequestOf(req *http.Request) SentRequest {
	names := make([]string, 0, len(req.Header))
	for k := range req.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	out := SentRequest{Method: req.Method, URL: req.URL.String()}
	for _, k := range names {
		for _, v := range req.Header[k] {
			out.Headers = append(out.Headers, Pair{Key: k, Value: v, Enabled: true})
		}
	}
	return out
}
