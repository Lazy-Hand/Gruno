package send

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamingTextBodyChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, ev := range []string{"data: one\n\n", "data: two\n\n"} {
			io.WriteString(w, ev)
			fl.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var chunks []string
	input := inputFor(RequestData{Method: "GET", URL: srv.URL}, nil, nil)
	input.OnBody = func(s string) {
		mu.Lock()
		chunks = append(chunks, s)
		mu.Unlock()
	}
	res, err := NewHTTPService().Send(context.Background(), input, func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != ErrNone {
		t.Fatalf("result: %+v", res)
	}
	want := "data: one\n\ndata: two\n\n"
	if res.BodyText != want {
		t.Fatalf("body: %q", res.BodyText)
	}
	mu.Lock()
	joined := strings.Join(chunks, "")
	n := len(chunks)
	mu.Unlock()
	if joined != want || n < 2 {
		t.Fatalf("chunks (%d): %q", n, joined)
	}
}

func TestStreamCancelKeepsPartialBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, "data: one\n\n")
		fl.Flush()
		for i := 0; i < 100; i++ {
			time.Sleep(20 * time.Millisecond)
			select {
			case <-r.Context().Done():
				return
			default:
			}
			io.WriteString(w, "data: more\n\n")
			fl.Flush()
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := inputFor(RequestData{Method: "GET", URL: srv.URL}, nil, nil)
	input.OnBody = func(s string) {
		if strings.Contains(s, "one") {
			cancel()
		}
	}
	res, err := NewHTTPService().Send(ctx, input, func(ResponseHead) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != ErrCanceled || !strings.Contains(res.BodyText, "data: one") {
		t.Fatalf("partial stream: %+v", res)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "流已取消") {
		t.Fatalf("notes: %+v", res.Notes)
	}
}
