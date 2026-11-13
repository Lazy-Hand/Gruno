package ui

// WebSocket requests: a saved list of messages (body:ws) plus a live
// connection that sends them and shows the transcript. The .bru shape
// follows Bruno's: meta.type ws, a ws block with the URL, and
// body:ws nested message blocks.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"gruno/bru"
	"gruno/internal/send"
)

// wsDialTimeout bounds one connection attempt.
const wsDialTimeout = 15 * time.Second

type wsEntry struct {
	Out  bool // sent by us
	Text string
	At   time.Time
}

// wsState is one tab's connection, transcript and message editors.
type wsState struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	status  string // "", connecting, connected, closed
	err     string
	log     []wsEntry
	bufs    map[int]*editor.State
	saved   map[*editor.Buffer]uint64
	msgKeys int // message count the buffers were built for
}

func (ts *tabState) wsState() *wsState {
	if ts.ws == nil {
		ts.ws = &wsState{bufs: map[int]*editor.State{}, saved: map[*editor.Buffer]uint64{}}
	}
	return ts.ws
}

func (ws *wsState) append(out bool, text string) {
	ws.mu.Lock()
	ws.log = append(ws.log, wsEntry{Out: out, Text: text, At: time.Now()})
	if len(ws.log) > 500 {
		ws.log = ws.log[len(ws.log)-500:]
	}
	ws.mu.Unlock()
}

func (ws *wsState) snapshot() (status, errText string, log []wsEntry) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.status, ws.err, append([]wsEntry(nil), ws.log...)
}

// requestMethod is the badge a request shows: WS for WebSocket files,
// the HTTP method otherwise.
func requestMethod(r *bru.Request) string {
	if r.IsWebSocket() {
		return "WS"
	}
	return strings.ToUpper(r.Method)
}

// wsScopes resolves variables as a send would.
func (a *app) wsScopes(br *BruRequest) [][]send.Pair {
	if a.coll == nil {
		return nil
	}
	return a.coll.scriptVarsOf(br, a.runtimeVars, a.secretPairs(a.coll.activeEnvironment())).Scopes()
}

// wsConnect opens the connection in the background.
func (a *app) wsConnect(br *BruRequest, ts *tabState) {
	ws := ts.wsState()
	ws.mu.Lock()
	if ws.status == "connecting" || ws.status == "connected" {
		ws.mu.Unlock()
		return
	}
	ws.status, ws.err = "connecting", ""
	ws.mu.Unlock()

	raw, err := send.Interpolate(br.Req.URL, a.wsScopes(br)...)
	if err != nil {
		ws.setError(err.Error())
		return
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "ws://") && !strings.HasPrefix(raw, "wss://") {
		ws.setError("WebSocket 地址必须以 ws:// 或 wss:// 开头")
		return
	}
	origin := "http://localhost"
	if u := strings.SplitN(raw, "://", 2); len(u) == 2 {
		scheme := "http"
		if u[0] == "wss" {
			scheme = "https"
		}
		host := strings.SplitN(u[1], "/", 2)[0]
		origin = scheme + "://" + host
	}
	cfg, err := websocket.NewConfig(raw, origin)
	if err != nil {
		ws.setError("地址无效: " + err.Error())
		return
	}
	services := a.services
	ch := make(chan struct {
		conn *websocket.Conn
		err  error
	}, 1)
	go func() {
		c, derr := websocket.DialConfig(cfg)
		ch <- struct {
			conn *websocket.Conn
			err  error
		}{c, derr}
	}()
	go func() {
		var res struct {
			conn *websocket.Conn
			err  error
		}
		select {
		case res = <-ch:
		case <-time.After(wsDialTimeout):
			ws.setError("连接超时（" + wsDialTimeout.String() + "）")
			services.Invalidate()
			// A late success must not leak a live connection.
			if late := <-ch; late.conn != nil {
				late.conn.Close()
			}
			return
		}
		if res.err != nil {
			ws.setError("连接失败: " + res.err.Error())
			services.Invalidate()
			return
		}
		ws.mu.Lock()
		ws.conn, ws.status = res.conn, "connected"
		ws.mu.Unlock()
		ws.append(false, "已连接 "+raw)
		services.Invalidate()
		a.wsReadLoop(ws, res.conn, services)
	}()
}

func (ws *wsState) setError(msg string) {
	ws.mu.Lock()
	ws.status, ws.err = "closed", msg
	ws.mu.Unlock()
}

// wsReadLoop appends received messages until the connection closes.
func (a *app) wsReadLoop(ws *wsState, c *websocket.Conn, services ui.Services) {
	for {
		var text string
		if err := websocket.Message.Receive(c, &text); err != nil {
			ws.mu.Lock()
			ws.conn, ws.status = nil, "closed"
			ws.mu.Unlock()
			ws.append(false, "连接已关闭: "+err.Error())
			services.Invalidate()
			return
		}
		ws.append(false, text)
		services.Invalidate()
	}
}

// wsDisconnect closes the tab's connection, if any.
func (a *app) wsDisconnect(ts *tabState) {
	if ts == nil || ts.ws == nil {
		return
	}
	ws := ts.ws
	ws.mu.Lock()
	c := ws.conn
	ws.conn, ws.status = nil, "closed"
	ws.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// wsSend sends one saved message, resolving its variables first.
func (a *app) wsSend(br *BruRequest, ts *tabState, idx int) {
	if idx < 0 || idx >= len(br.Req.WSMessages) {
		return
	}
	ws := ts.wsState()
	ws.mu.Lock()
	c := ws.conn
	ws.mu.Unlock()
	if c == nil {
		ws.setError("请先连接")
		return
	}
	text, err := send.Interpolate(br.Req.WSMessages[idx].Body, a.wsScopes(br)...)
	if err != nil {
		ws.append(true, "变量解析失败: "+err.Error())
		return
	}
	services := a.services
	go func() {
		if err := websocket.Message.Send(c, text); err != nil {
			ws.append(true, "发送失败: "+err.Error())
		} else {
			ws.append(true, text)
		}
		services.Invalidate()
	}()
}

// watchWSBuffers saves an edited message body, debounced like the other
// editors.
func (a *app) watchWSBuffers(br *BruRequest, ts *tabState) {
	ws := ts.wsState()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	dirty := false
	for i, st := range ws.bufs {
		if st == nil || i >= len(br.Req.WSMessages) {
			continue
		}
		v := st.Buffer.Version()
		if ws.saved[st.Buffer] == v {
			continue
		}
		ws.saved[st.Buffer] = v
		br.Req.WSMessages[i].Body = st.Buffer.String()
		dirty = true
	}
	if dirty {
		a.saveSoon(br)
	}
}

// wsMessageState returns (creating) the editor for message i.
func (ws *wsState) messageState(i int, msg bru.WSMessage) *editor.State {
	if st, ok := ws.bufs[i]; ok {
		return st
	}
	b := editor.NewBuffer(msg.Body)
	b.SetLanguage(editor.LanguageFor("ws." + msg.Kind))
	st := &editor.State{Buffer: b}
	ws.bufs[i] = st
	return st
}

// wsPane draws the WebSocket request: URL, connection controls, saved
// messages and the transcript.
func (a *app) wsPane(c *ui.Context, br *BruRequest, ts *tabState) {
	t := c.Theme()
	r := br.Req
	ws := ts.wsState()
	status, errText, log := ws.snapshot()

	if ws.msgKeys != len(r.WSMessages) {
		ws.mu.Lock()
		ws.bufs, ws.saved = map[int]*editor.State{}, map[*editor.Buffer]uint64{}
		ws.msgKeys = len(r.WSMessages)
		ws.mu.Unlock()
	}

	statusLabel := "未连接"
	switch status {
	case "connecting":
		statusLabel = "连接中…"
	case "connected":
		statusLabel = "已连接"
	case "closed":
		statusLabel = "已断开"
	}

	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).Padding(10, 12, 4, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "WebSocket").Bold().FontSize(14)
			ui.Text(c, statusLabel).FontSize(11.5).TextColor(t.TextMuted)
			if status == "connected" {
				ui.Text(c, "●").FontSize(11).TextColor(t.Success)
			}
			ui.Box(c).Grow(1)
			if status == "connected" || status == "connecting" {
				if ui.Button(c.Key(br.Path+"-ws-off"), "断开").Padding(4, 10).Clicked() {
					a.wsDisconnect(ts)
					ws.append(false, "已手动断开")
				}
			} else {
				connect := ui.PrimaryButton(c.Key(br.Path+"-ws-on"), "连接").Padding(4, 12)
				if connect.Clicked() {
					a.wsConnect(br, ts)
				}
			}
		})
		ui.Row(c).Padding(2, 12, 8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.varInput(c, br.Path+"-ws-url", br, &r.URL, varInputOpts{Placeholder: "wss://example.com/socket", Label: "WebSocket 地址"}) {
				a.saveSoon(br)
			}
		})
		if errText != "" {
			ui.Text(c, errText).FontSize(11.5).TextColor(t.Danger).Padding(0, 12, 6, 12).MaxLines(2)
		}
		ui.Row(c).Padding(2, 12, 6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Textf(c, "消息（%d）", len(r.WSMessages)).Bold().FontSize(12).Grow(1)
			if ui.Button(c.Key(br.Path+"-ws-add"), "添加消息").Padding(3, 8).Clicked() {
				r.WSMessages = append(r.WSMessages, bru.WSMessage{Name: fmt.Sprintf("message %d", len(r.WSMessages)+1), Kind: "json", Body: "{}"})
				a.saveSoon(br)
			}
			if len(log) > 0 && ui.Button(c.Key(br.Path+"-ws-clear"), "清空记录").Padding(3, 8).Clicked() {
				ws.mu.Lock()
				ws.log = nil
				ws.mu.Unlock()
			}
		})
		ui.Scroll(c).Grow(1).Padding(4, 12).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				deleteAt := -1
				for i := range r.WSMessages {
					i := i
					msg := &r.WSMessages[i]
					rowKey := fmt.Sprintf("%s-ws-msg-%d", br.Path, i)
					ui.Column(c).Gap(4).Padding(8, 10).Radius(6).
						BorderWidth(1).BorderColor(t.Border).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							if ui.TextInput(c.Key(rowKey+"-name"), &msg.Name).Width(160).Placeholder("消息名称").Changed() {
								a.saveSoon(br)
							}
							kind := orDefaultS(msg.Kind, "text")
							if ui.Select(c.Key(rowKey+"-kind"), &kind, []string{"text", "json", "xml"}).Changed() {
								msg.Kind = kind
								ws.mu.Lock()
								delete(ws.bufs, i)
								ws.mu.Unlock()
								a.saveSoon(br)
							}
							ui.Box(c).Grow(1)
							sendBtn := ui.Button(c.Key(rowKey+"-send"), "发送").Padding(3, 10)
							if sendBtn.Clicked() {
								a.wsSend(br, ts, i)
							}
							del := ui.Button(c.Key(rowKey+"-del"), "").Padding(3, 6).Label("删除消息")
							del.Children(func() { ui.Icon(c, iconTrash).Size(13, 13).TextColor(t.TextMuted) })
							if del.Clicked() {
								deleteAt = i
							}
						})
						st := ws.messageState(i, *msg)
						editor.View(c.Key(rowKey+"-body"), st).Height(110).FillWidth()
					})
				}
				if deleteAt >= 0 && deleteAt < len(r.WSMessages) {
					r.WSMessages = append(r.WSMessages[:deleteAt], r.WSMessages[deleteAt+1:]...)
					a.saveSoon(br)
				}
				if len(r.WSMessages) == 0 {
					ui.Text(c, "暂无消息，点击「添加消息」保存一条，连接后可发送").FontSize(12).TextColor(t.TextMuted).Padding(8, 0)
				}
				ui.Text(c, "记录").Bold().FontSize(12).Padding(8, 0, 2, 0)
				if len(log) == 0 {
					ui.Text(c, "连接后发送或接收的消息会显示在这里").FontSize(12).TextColor(t.TextMuted)
				}
				for _, e := range log {
					dir, col := "←", t.Text
					if e.Out {
						dir, col = "→", t.Accent
					}
					ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
						ui.Text(c, e.At.Format("15:04:05.000")).FontSize(11).TextColor(t.TextMuted).Width(90)
						ui.Text(c, dir).FontSize(12).Bold().TextColor(col).Width(14)
						ui.Text(c, e.Text).FontSize(12).TextColor(col).Grow(1).MinWidth(0).Selectable()
					})
				}
				ui.Text(c, "WebSocket 请求的脚本与测试暂不执行；消息与地址照 Bruno 的 body:ws 格式保存。").
					FontSize(11).TextColor(t.TextMuted).Padding(8, 0, 0, 0)
			})
		})
		a.watchWSBuffers(br, ts)
	})
}
