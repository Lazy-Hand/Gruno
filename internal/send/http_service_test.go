package send

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sendTo(t *testing.T, srv *httptest.Server, data RequestData) (ResponseHead, ResponseResult) {
	t.Helper()
	s := NewHTTPService()
	var head ResponseHead
	gotHead := false
	result, err := s.Send(context.Background(), inputFor(data, nil, nil), func(h ResponseHead) error {
		head = h
		gotHead = true
		return nil
	})
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if !gotHead {
		t.Fatal("onHead never called")
	}
	return head, result
}

func TestSendBasicResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("X-Dup", "one")
		w.Header().Add("X-Dup", "two")
		w.WriteHeader(201)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	head, result := sendTo(t, srv, RequestData{Method: "GET", URL: srv.URL})
	if head.StatusCode != 201 || result.ErrorCode != "" {
		t.Fatalf("status=%d err=%s", head.StatusCode, result.ErrorCode)
	}
	if result.BodyKind != BodyKindText || result.BodyText != `{"ok":true}` {
		t.Fatalf("body: kind=%s text=%q", result.BodyKind, result.BodyText)
	}
	// Both Set-Cookie and duplicate headers survive.
	var cookies, dups int
	for _, h := range head.Headers {
		if h.Key == "Set-Cookie" {
			cookies++
		}
		if h.Key == "X-Dup" {
			dups++
		}
	}
	if cookies != 2 || dups != 2 {
		t.Fatalf("headers lost: cookies=%d dups=%d (%+v)", cookies, dups, head.Headers)
	}
	if head.Protocol == "" || head.URL == "" {
		t.Fatalf("head fields: %+v", head)
	}
}

func TestSendErrorStatusesAreResponses(t *testing.T) {
	for _, code := range []int{401, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			fmt.Fprint(w, "err body")
		}))
		head, result := sendTo(t, srv, RequestData{Method: "GET", URL: srv.URL})
		srv.Close()
		if head.StatusCode != code || result.ErrorCode != "" {
			t.Fatalf("%d: status=%d err=%s", code, head.StatusCode, result.ErrorCode)
		}
		if result.BodyText != "err body" {
			t.Fatalf("%d body: %q", code, result.BodyText)
		}
	}
}

func TestSendTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write(make([]byte, maxResponseBody+100))
	}))
	defer srv.Close()
	_, result := sendTo(t, srv, RequestData{Method: "GET", URL: srv.URL})
	if !result.Truncated {
		t.Fatal("should be truncated")
	}
	if result.SizeBytes != maxResponseBody {
		t.Fatalf("size: %d", result.SizeBytes)
	}
	if result.BodyKind != BodyKindText {
		t.Fatalf("kind: %s", result.BodyKind)
	}
}

func TestSendBodyKinds(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        []byte
		want        BodyKind
	}{
		{"utf8", "text/plain; charset=utf-8", []byte("hello 世界"), BodyKindText},
		{"no-charset-text", "text/plain", []byte("plain ascii"), BodyKindText},
		{"json", "application/json", []byte(`{"a":1}`), BodyKindText},
		{"json-suffix", "application/vnd.api+json", []byte(`{"a":1}`), BodyKindText},
		{"xml", "application/atom+xml", []byte(`<a/>`), BodyKindText},
		{"empty", "text/plain", nil, BodyKindText},
		{"binary", "application/octet-stream", []byte{0, 1, 2, 255}, BodyKindBinary},
		{"latin1", "text/plain; charset=iso-8859-1", []byte("caf\xe9"), BodyKindUnsupportedCharset},
		{"bad-utf8", "text/plain; charset=utf-8", []byte{0xff, 0xfe, 0xfd}, BodyKindUnsupportedCharset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.Write(tc.body)
			}))
			defer srv.Close()
			_, result := sendTo(t, srv, RequestData{Method: "GET", URL: srv.URL})
			if result.BodyKind != tc.want {
				t.Fatalf("kind=%s want=%s", result.BodyKind, tc.want)
			}
			if tc.want == BodyKindText && string(tc.body) != "" && result.BodyText != string(tc.body) {
				t.Fatalf("text mismatch: %q", result.BodyText)
			}
			if tc.want != BodyKindText && result.BodyText != "" {
				t.Fatalf("non-text should have empty bodyText: %q", result.BodyText)
			}
		})
	}
}

func TestSendTruncatedMultibyte(t *testing.T) {
	// Body of '世' (3 bytes) repeated, truncated mid-rune at the boundary.
	payload := strings.Repeat("世", maxResponseBody/3+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, payload)
	}))
	defer srv.Close()
	_, result := sendTo(t, srv, RequestData{Method: "GET", URL: srv.URL})
	if !result.Truncated || result.BodyKind != BodyKindText {
		t.Fatalf("truncated multibyte: kind=%s trunc=%v", result.BodyKind, result.Truncated)
	}
	if !utf8Valid(result.BodyText) {
		t.Fatal("bodyText must be valid UTF-8 after rune trim")
	}
}

func utf8Valid(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			i++
			continue
		}
		// crude check: re-encode and compare length
		break
	}
	return len([]rune(s)) >= 0 && strings.ToValidUTF8(s, "\uFFFD") == s
}

func TestSendCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	s := NewHTTPService()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ResponseResult, 1)
	go func() {
		res, _ := s.Send(ctx, inputFor(RequestData{Method: "GET", URL: srv.URL}, nil, nil), func(ResponseHead) error { return nil })
		done <- res
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.ErrorCode != ErrCanceled {
			t.Fatalf("cancel code: %s", res.ErrorCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("send did not return after cancel")
	}
}

func TestSendNetworkError(t *testing.T) {
	// Port 1 is never listening.
	s := NewHTTPService()
	result, err := s.Send(context.Background(), inputFor(RequestData{Method: "GET", URL: "http://127.0.0.1:1/"}, nil, nil), func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatalf("network failure should be a result: %v", err)
	}
	if result.ErrorCode != ErrNetwork {
		t.Fatalf("network error code: %s (%s)", result.ErrorCode, result.ErrorMessage)
	}
}

func TestSendInvalidRequest(t *testing.T) {
	s := NewHTTPService()
	blocked := RequestData{Method: "GET", URL: "not a url"}
	result, err := s.Send(context.Background(), inputFor(blocked, nil, nil), func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatalf("invalid request should be a result, not Go error: %v", err)
	}
	if result.ErrorCode != ErrInvalidReq {
		t.Fatalf("code: %s", result.ErrorCode)
	}
}

func TestSendHeadBeforeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		io.WriteString(w, "late body")
	}))
	defer srv.Close()
	s := NewHTTPService()
	headCh := make(chan ResponseHead, 1)
	done := make(chan ResponseResult, 1)
	go func() {
		res, _ := s.Send(context.Background(), inputFor(RequestData{Method: "GET", URL: srv.URL}, nil, nil), func(h ResponseHead) error {
			headCh <- h
			return nil
		})
		done <- res
	}()
	select {
	case h := <-headCh:
		if h.StatusCode != 200 {
			t.Fatalf("head status: %d", h.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("head not delivered before body")
	}
	res := <-done
	if res.BodyText != "late body" {
		t.Fatalf("body: %q", res.BodyText)
	}
}
