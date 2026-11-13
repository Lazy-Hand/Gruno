package ui

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/egoist/mygo/ui"

	"gruno/bru"
)

func wsWait(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestWebSocketTabConnectsAndEchoes(t *testing.T) {
	srv := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		var msg string
		for {
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				return
			}
			if err := websocket.Message.Send(conn, "echo:"+msg); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "Socket")
	br.Req.Meta.Type = "ws"
	br.Req.URL = wsURL
	br.Req.WSMessages = []bru.WSMessage{{Name: "hello", Kind: "text", Body: "hi"}}
	a.openTab(br)
	ts := a.tabState(br)

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if !tt.HasText("WebSocket") || !tt.HasText("添加消息") {
		t.Fatalf("ws pane missing: %q", tt.Texts())
	}

	a.wsConnect(br, ts)
	ws := ts.wsState()
	wsWait(t, func() bool {
		status, _, _ := ws.snapshot()
		return status == "connected"
	})
	a.wsSend(br, ts, 0)
	wsWait(t, func() bool {
		_, _, log := ws.snapshot()
		gotOut, gotIn := false, false
		for _, e := range log {
			if e.Out && e.Text == "hi" {
				gotOut = true
			}
			if !e.Out && e.Text == "echo:hi" {
				gotIn = true
			}
		}
		return gotOut && gotIn
	})
	a.wsDisconnect(ts)
	wsWait(t, func() bool {
		status, _, _ := ws.snapshot()
		return status == "closed"
	})

	// The ws file keeps its shape: no HTTP method block is invented.
	if err := a.coll.SaveRequest(br); err != nil {
		t.Fatal(err)
	}
	data, err := readFileString(a.coll, br.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, "\nget {") || !strings.Contains(data, "body:ws") || !strings.Contains(data, "type: ws") {
		t.Fatalf("ws file rewritten as http:\n%s", data)
	}
}

func TestWebSocketRefusesBadURL(t *testing.T) {
	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "Socket")
	br.Req.Meta.Type = "ws"
	br.Req.URL = "https://example.com"
	a.openTab(br)
	ts := a.tabState(br)
	a.wsConnect(br, ts)
	_, errText, _ := ts.wsState().snapshot()
	if !strings.Contains(errText, "ws://") {
		t.Fatalf("err: %q", errText)
	}
}

func readFileString(c *Collection, rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(rel)))
	return string(data), err
}
