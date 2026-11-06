package main

// app.go is the whole native UI: a Bruno workbench. The sidebar is a
// tree of folders and requests; open requests are tabs, each editing a
// .bru file live — every change writes the file straight back, as
// Bruno's own app does.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"gruno/bru"
)

// app is the workbench state.
type app struct {
	coll     *Collection
	openErr  string
	svc      *HTTPService
	services ui.Services

	open   []*BruRequest // open tabs, in order
	active string        // tab path shown
	tabs   map[string]*tabState
	split  float32 // sidebar fraction
	vsplit float32 // request pane percent, 0-100

	filter string
	envSel string

	layoutHorizontal bool // true = side-by-side (Bruno default), false = top-bottom
	themeMode        string // "system", "light", "dark"
	recentDirs       []string
	copyTimer        time.Time
	envInspector     bool

	// Modals. Only one shows at a time.
	newRequest       bool
	newRequestIn     string
	newRequestMethod string
	newRequestURL    string
	newFolder        bool
	newFolderIn      string
	renameTarget     *BruRequest
	renameIn         string
	renameOpen       bool
	renameFolder     *Folder
	renameFolderIn   string
	renameFolderOpen bool
	renameCollOpen   bool
	renameCollIn     string
	deleteTarget     *BruRequest
	deleteOpen       bool
	deleteFolder     *Folder
	deleteFolderOpen bool
	envEditor        bool
	envDraft         *bru.Environment
	envTarget        *bru.Environment
	newEnv           bool
	newEnvIn         string
	collSettingsOpen bool
	collSettingsTab  int
	collScriptPre    *editor.State
	collScriptPost   *editor.State
	collTestsEd      *editor.State
	collDocsEd       *editor.State

	// folderCtx is the folder path the last context action targets.
	folderCtx string

	folderOpen  map[string]bool
	methodOpen  bool
	revealAuth  map[string]bool
	mdOpts      markdown.Options
}

// tabState is per-open-request transient state: editor buffers, the
// section tab, the in-flight send and its result.
type tabState struct {
	section int

	method string

	bodyBuf    *editor.Buffer
	bodyEd     *editor.State
	gqlVarsBuf *editor.Buffer
	gqlVarsEd  *editor.State
	respBuf    *editor.Buffer
	respEd     *editor.State
	scriptPre  *editor.State
	scriptPost *editor.State
	testsEd    *editor.State
	docsEd     *editor.State
	docsPreview bool

	respSection int
	respFormat  int // 0 = Pretty, 1 = Raw

	sending bool
	cancel  context.CancelFunc
	mu      sync.Mutex
	head    *ResponseHead
	result  *ResponseResult
	err     error
	sentAt  time.Time
	sentReq *RequestData
	// savedVers tracks each buffer's version at last flush to disk, so
	// Modified()'s sticky flag doesn't re-save every frame.
	savedVers map[*editor.Buffer]uint64
}

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// bodyKinds lists the .bru body selectors shown in the Body tab.
var bodyKinds = []string{"none", "json", "text", "xml", "form-urlencoded", "multipart-form", "graphql"}

// authKinds lists the .bru auth selectors.
var authKinds = []string{"none", "inherit", "basic", "bearer", "apikey", "digest", "oauth2", "awsv4", "wsse", "ntlm"}

func newApp() *app {
	return &app{
		svc:              newHTTPService(),
		tabs:             map[string]*tabState{},
		folderOpen:       map[string]bool{},
		revealAuth:       map[string]bool{},
		split:            240,
		vsplit:           50,
		layoutHorizontal: true,
		themeMode:        "system",
		newRequestMethod: "GET",
	}
}

// prefsData persists last opened collection and recent collection paths.
type prefsData struct {
	Collection string   `json:"collection"`
	Recents    []string `json:"recents,omitempty"`
	Theme      string   `json:"theme,omitempty"`
}

// ---- collection lifecycle ----

// openCollection loads dir, replacing any open collection.
func (a *app) openCollection(dir string) {
	c, err := OpenCollection(dir)
	if err != nil {
		a.openErr = err.Error()
		a.coll = nil
		return
	}
	a.openErr = ""
	a.coll = c
	a.open = nil
	a.active = ""
	a.tabs = map[string]*tabState{}
	a.folderOpen = map[string]bool{}
	a.envSel = c.ActiveEnv
	a.initCollectionSettingsEditors()
	a.saveLastDir(dir)
}

func (a *app) initCollectionSettingsEditors() {
	if a.coll == nil {
		return
	}
	if a.coll.Dot == nil {
		a.coll.Dot = &bru.CollectionBru{Auth: "none"}
	}
	mk := func(text, ext string) *editor.State {
		b := editor.NewBuffer(text)
		b.SetLanguage(editor.LanguageFor("x" + ext))
		return &editor.State{Buffer: b}
	}
	a.collScriptPre = mk(a.coll.Dot.ScriptPreRequest, ".js")
	a.collScriptPost = mk(a.coll.Dot.ScriptPostResponse, ".js")
	a.collTestsEd = mk(a.coll.Dot.Tests, ".js")
	a.collDocsEd = mk(a.coll.Dot.Docs, ".md")
}

func (a *app) saveLastDir(dir string) {
	p, err := a.prefsPath()
	if err != nil {
		return
	}
	var recents []string
	if dir != "" {
		recents = append(recents, dir)
	}
	for _, r := range a.recentDirs {
		if r != dir && r != "" {
			recents = append(recents, r)
		}
		if len(recents) >= 8 {
			break
		}
	}
	a.recentDirs = recents
	data, _ := json.Marshal(prefsData{Collection: dir, Recents: recents, Theme: a.themeMode})
	os.WriteFile(p, data, 0o644)
}

func (a *app) restoreLast() {
	p, err := a.prefsPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m prefsData
	if json.Unmarshal(data, &m) == nil {
		a.recentDirs = m.Recents
		if m.Theme == "light" || m.Theme == "dark" || m.Theme == "system" {
			a.themeMode = m.Theme
		}
		if m.Collection != "" {
			a.openCollection(m.Collection)
		}
	}
}

// appVersion is the packaged version like "v0.1.0", "dev" while running
// unbuilt.
func appVersion() string {
	if v := mygo.App.Version(); v != "" {
		return "v" + v
	}
	return "dev"
}

// applyThemeSource gives the app mygo's appearance of a.themeMode, so the
// native title bar and window controls follow the same light or dark look
// as the painted content. Skipped without a running app: SetSource would
// wait for a main thread that no event loop drains (ui.NewTester).
func (a *app) applyThemeSource() {
	if !mygo.App.IsReady() {
		return
	}
	mygo.Theme.SetSource(mygo.ThemeSource(a.themeMode))
}

func (a *app) prefsPath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil || dir == "" {
		dir = filepath.Join(os.TempDir(), "gruno")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "gruno.json"), nil
}

// ---- tab management ----

func (a *app) openTab(br *BruRequest) {
	for _, t := range a.open {
		if t == br {
			a.active = br.Path
			return
		}
	}
	a.open = append(a.open, br)
	a.active = br.Path
}

func (a *app) closeTab(path string) {
	for i, t := range a.open {
		if t.Path == path {
			a.open = append(a.open[:i], a.open[i+1:]...)
			delete(a.tabs, path)
			if a.active == path {
				if i < len(a.open) {
					a.active = a.open[i].Path
				} else if len(a.open) > 0 {
					a.active = a.open[len(a.open)-1].Path
				} else {
					a.active = ""
				}
			}
			return
		}
	}
}

func (a *app) activeTab() (*BruRequest, *tabState) {
	for _, t := range a.open {
		if t.Path == a.active {
			return t, a.tabState(t)
		}
	}
	return nil, nil
}

// tabState returns (creating) the transient state of br.
func (a *app) tabState(br *BruRequest) *tabState {
	// Bruno mirrors URL query into the params table on open.
	urlQueryToParams(br.Req)
	if ts, ok := a.tabs[br.Path]; ok {
		return ts
	}
	ts := &tabState{method: strings.ToUpper(orDefaultS(br.Req.Method, "get"))}
	lang := ".txt"
	if br.Req.Body == "json" || br.Req.Body == "graphql" {
		lang = ".json"
	} else if br.Req.Body == "xml" {
		lang = ".xml"
	}
	ts.bodyBuf = editor.NewBuffer(a.bodyTextOf(br.Req))
	ts.bodyBuf.SetLanguage(editor.LanguageFor("body" + lang))
	ts.bodyEd = &editor.State{Buffer: ts.bodyBuf}
	ts.gqlVarsBuf = editor.NewBuffer(br.Req.BodyGraphQLVars)
	ts.gqlVarsBuf.SetLanguage(editor.LanguageFor("vars.json"))
	ts.gqlVarsEd = &editor.State{Buffer: ts.gqlVarsBuf}
	ts.respBuf = editor.NewBuffer("")
	ts.respBuf.SetLanguage(editor.LanguageFor("body.json"))
	ts.respEd = &editor.State{Buffer: ts.respBuf, ReadOnly: true}
	mk := func(text, ext string) *editor.State {
		b := editor.NewBuffer(text)
		b.SetLanguage(editor.LanguageFor("x" + ext))
		return &editor.State{Buffer: b}
	}
	ts.scriptPre = mk(br.Req.ScriptPreRequest, ".js")
	ts.scriptPost = mk(br.Req.ScriptPostResponse, ".js")
	ts.testsEd = mk(br.Req.Tests, ".js")
	ts.docsEd = mk(br.Req.Docs, ".md")
	a.tabs[br.Path] = ts
	return ts
}

// bodyTextOf returns the raw text of the active body kind.
func (a *app) bodyTextOf(r *bru.Request) string {
	switch r.Body {
	case "json":
		return r.BodyJSON
	case "text":
		return r.BodyText
	case "xml":
		return r.BodyXML
	case "graphql":
		return r.BodyGraphQL
	default:
		return ""
	}
}

// setBodyText stores the body editor's text into the right field.
func setBodyText(r *bru.Request, text string) {
	switch r.Body {
	case "json":
		r.BodyJSON = text
	case "text":
		r.BodyText = text
	case "xml":
		r.BodyXML = text
	case "graphql":
		r.BodyGraphQL = text
	}
}

// save writes br's file; failures surface in the tab's error line.
func (a *app) save(br *BruRequest) {
	if err := a.coll.SaveRequest(br); err != nil {
		// Save failures are rare (disk full, perms); surface via dialog.
		mygo.Dialog.Error("保存失败", err.Error())
	}
}

// send fires the request in the background.
func (a *app) send(br *BruRequest, ts *tabState) {
	if ts.sending {
		return
	}
	a.syncFromUI(br, ts)
	input := a.coll.sendInputOf(br)
	reqSnap := input.Request
	ts.mu.Lock()
	ts.sending = true
	ts.head = nil
	ts.result = nil
	ts.err = nil
	ts.sentReq = &reqSnap
	ts.sentAt = time.Now()
	ts.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	ts.cancel = cancel
	svc := a.svc
	go func() {
		var head ResponseHead
		res, err := svc.send(ctx, input, func(h ResponseHead) error {
			head = h
			ts.mu.Lock()
			ts.head = &head
			ts.mu.Unlock()
			a.services.Invalidate()
			return nil
		})
		ts.mu.Lock()
		ts.sending = false
		ts.err = err
		if err == nil {
			r := res
			ts.result = &r
			ts.respBuf.Set(respDisplayTextFor(&r, ts.respFormat))
		}
		ts.mu.Unlock()
		a.services.Invalidate()
	}()
}

// respDisplayText picks the response body text for the editor.
func respDisplayText(r *ResponseResult) string {
	return respDisplayTextFor(r, 0)
}

func respDisplayTextFor(r *ResponseResult, format int) string {
	if r == nil {
		return ""
	}
	if r.ErrorCode != "" {
		return r.ErrorMessage
	}
	if r.BodyKind == BodyKindBinary {
		return fmt.Sprintf("[binary body, %d bytes]", r.SizeBytes)
	}
	if r.BodyKind == BodyKindUnsupportedCharset {
		return fmt.Sprintf("[unsupported charset, %d bytes]", r.SizeBytes)
	}
	if format == 0 && len(r.BodyText) > 0 && len(r.BodyText) <= 1<<20 {
		trimmed := strings.TrimSpace(r.BodyText)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return beautifyJSON(r.BodyText)
		}
	}
	return r.BodyText
}

// syncFromUI copies transient editor state back into br.Req.
func (a *app) syncFromUI(br *BruRequest, ts *tabState) {
	r := br.Req
	r.Method = strings.ToLower(ts.method)
	setBodyText(r, ts.bodyBuf.String())
	if ts.gqlVarsBuf != nil {
		r.BodyGraphQLVars = ts.gqlVarsBuf.String()
	}
	r.ScriptPreRequest = ts.scriptPre.Buffer.String()
	r.ScriptPostResponse = ts.scriptPost.Buffer.String()
	r.Tests = ts.testsEd.Buffer.String()
	r.Docs = ts.docsEd.Buffer.String()
}

// ---- the view ----

var (
	brunoAmber       = ui.Hex("#ea580c") // Bruno's primary orange/amber
	brunoAmberHover  = ui.Hex("#c2410c")
	brunoAmberActive = ui.Hex("#9a3412")

	// HTTP Method colors (matching Bruno's exact palette)
	methodGet     = ui.Hex("#10b981") // Green
	methodPost    = ui.Hex("#f59e0b") // Amber / Gold
	methodPut     = ui.Hex("#3b82f6") // Blue
	methodPatch   = ui.Hex("#8b5cf6") // Purple
	methodDelete  = ui.Hex("#ef4444") // Red
	methodHead    = ui.Hex("#14b8a6") // Teal
	methodOptions = ui.Hex("#6366f1") // Indigo
)

// brunoTheme returns the app theme: mygo's with Bruno's orange accent and metrics.
func (a *app) resolveTheme(base *ui.Theme) *ui.Theme {
	switch a.themeMode {
	case "light":
		base = ui.LightTheme()
	case "dark":
		base = ui.DarkTheme()
	}
	return brunoTheme(base)
}

func brunoTheme(t *ui.Theme) *ui.Theme {
	out := *t
	out.Accent = brunoAmber
	out.AccentHover = brunoAmberHover
	out.AccentPressed = brunoAmberActive
	out.AccentText = ui.Hex("#ffffff")
	out.Radius = 6
	return &out
}

func methodColor(t *ui.Theme, m string) ui.Color {
	switch strings.ToUpper(m) {
	case "GET":
		return methodGet
	case "POST":
		return methodPost
	case "PUT":
		return methodPut
	case "PATCH":
		return methodPatch
	case "DELETE":
		return methodDelete
	case "HEAD":
		return methodHead
	case "OPTIONS":
		return methodOptions
	default:
		return t.TextMuted
	}
}

func methodBgColor(t *ui.Theme, m string) ui.Color {
	return methodColor(t, m).Alpha(0.14)
}

func (a *app) view(c *ui.Context) {
	c.SetTheme(a.resolveTheme(c.Theme()))
	a.services = c.Services()
	if c.Shortcut(ui.Cmd, ui.KeyS) {
		if br, ts := a.activeTab(); br != nil {
			a.syncFromUI(br, ts)
			a.save(br)
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyEnter) {
		if br, ts := a.activeTab(); br != nil {
			a.send(br, ts)
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) || c.Shortcut(ui.Cmd, ui.KeyT) {
		if a.coll != nil {
			a.folderCtx = ""
			a.newRequest = true
			a.newRequestIn = ""
			a.newRequestMethod = "GET"
			a.newRequestURL = ""
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyW) {
		if a.active != "" {
			a.closeTab(a.active)
		}
	}
	ui.Column(c).Fill().Children(func() {
		if a.coll == nil {
			ui.Column(c).Grow(1).FillWidth().Children(func() {
				a.welcome(c)
			})
		} else {
			ui.Split(c, &a.split, func() {
				a.sidebar(c)
			}, func() {
				a.workbench(c)
			}).Grow(1).FillWidth()
		}
		a.statusBar(c)
	})
	a.modals(c)
}

func (a *app) cycleTheme() {
	switch a.themeMode {
	case "system":
		a.themeMode = "light"
	case "light":
		a.themeMode = "dark"
	default:
		a.themeMode = "system"
	}
	dir := ""
	if a.coll != nil {
		dir = a.coll.Root
	}
	a.saveLastDir(dir)
	a.applyThemeSource()
}

func (a *app) themeIconAndLabel() (*ui.SVG, string) {
	switch a.themeMode {
	case "light":
		return iconSun, "浅色"
	case "dark":
		return iconMoon, "深色"
	default:
		return iconDeviceDesktop, "跟随系统"
	}
}

// statusBar renders Bruno's bottom footer bar with version, collection status, and theme toggle.
func (a *app) statusBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(4, 12).Gap(8).
		Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).
		AlignItems(ui.Center).Children(func() {
		ui.Image(c, brunoLogoSVG).Size(15, 15)
		ui.Text(c, "Gruno "+appVersion()).FontSize(11).Bold().TextColor(t.TextMuted)
		if a.coll != nil {
			ui.Text(c, "•").FontSize(11).TextColor(t.TextMuted)
			ui.Icon(c, iconBox).Size(12, 12).TextColor(t.TextMuted)
			ui.Textf(c, "%s (%d 个请求)", a.coll.Name, len(a.coll.Requests)).FontSize(11).TextColor(t.TextMuted)
			if a.coll.ActiveEnv != "" {
				ui.Text(c, "•").FontSize(11).TextColor(t.TextMuted)
				ui.Icon(c, iconWorld).Size(12, 12).TextColor(t.Accent)
				ui.Text(c, "环境: "+a.coll.ActiveEnv).FontSize(11).TextColor(t.Accent)
			}
		}
		ui.Box(c).Grow(1)
		thIcon, thLabel := a.themeIconAndLabel()
		btn := ui.Button(c, "").Padding(2, 8).Gap(4).Label("切换主题: " + thLabel)
		btn.Children(func() {
			ui.Icon(c, thIcon).Size(12, 12)
			ui.Text(c, thLabel).FontSize(10.5)
		})
		if btn.Clicked() {
			a.cycleTheme()
		}
	})
}

// welcome is the start screen: Bruno branding, action cards, and recent collections.
func (a *app) welcome(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(20).Padding(24, 16).Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Image(c, brunoLogoSVG).Size(52, 52)
				ui.Text(c, "Bruno").FontSize(34).Bold()
			})
			ui.Text(c, "轻量、开源、Git 友好的 API 客户端").TextColor(t.TextMuted).FontSize(14)
			if a.openErr != "" {
				ui.Text(c, a.openErr).TextColor(t.Danger).Padding(4, 0)
			}
		})

		ui.Row(c).Gap(14).Children(func() {
			card := func(ic *ui.SVG, title, desc string, onClick func()) {
				box := ui.Column(c).Gap(6).Padding(16, 20).Width(220).
					Background(t.Surface).Radius(8).BorderWidth(1).BorderColor(t.Border).
					AlignItems(ui.Start).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, ic).Size(20, 20).TextColor(t.Accent)
						ui.Text(c, title).Bold().FontSize(14)
					})
					ui.Text(c, desc).TextColor(t.TextMuted).FontSize(11)
				})
				if box.Clicked() {
					onClick()
				}
			}

			card(iconFolderOpen, "打开集合…", "打开磁盘上已有的 Bruno 集合目录", func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Title:     "选择集合目录",
					Directory: true,
				})
				if err == nil && len(paths) > 0 {
					a.openCollection(paths[0])
				}
			})

			card(iconFolderPlus, "新建集合…", "在本地新建一个 Bruno API 集合", func() {
				path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
					Title: "选择集合目录位置",
				})
				if err == nil && path != "" {
					if _, err := CreateCollection(path, filepath.Base(path)); err == nil {
						a.openCollection(path)
					} else {
						a.openErr = err.Error()
					}
				}
			})
		})

		if len(a.recentDirs) > 0 {
			ui.Column(c).Gap(8).Width(454).Padding(12, 0).Children(func() {
				ui.Text(c, "最近打开的集合").FontSize(12).Bold().TextColor(t.TextMuted)
				for _, dir := range a.recentDirs {
					dir := dir
					row := ui.Row(c).Gap(8).Padding(8, 12).Background(t.Surface).Radius(6).
						BorderWidth(1).BorderColor(t.Border.Alpha(0.6)).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconFolder).Size(16, 16).TextColor(t.Accent)
						ui.Text(c, filepath.Base(dir)).Bold().FontSize(12)
						ui.Text(c, dir).TextColor(t.TextMuted).FontSize(11).Grow(1)
					})
					if row.Clicked() {
						a.openCollection(dir)
					}
				}
			})
		}
	})
}

// collectionMenu is the sidebar's + menu: request/folder/environment
// creation and collection switching.
func (a *app) collectionMenu(m *ui.Menu) {
	if m.Item("新建请求").Chosen() {
		a.folderCtx = ""
		a.newRequest = true
		a.newRequestIn = ""
		a.newRequestMethod = "GET"
		a.newRequestURL = ""
	}
	if m.Item("新建目录").Chosen() {
		a.folderCtx = ""
		a.newFolder = true
		a.newFolderIn = ""
	}
	m.Separator()
	if m.Item("集合设置…").Chosen() {
		a.initCollectionSettingsEditors()
		a.collSettingsOpen = true
	}
	if m.Item("重命名集合…").Chosen() {
		a.renameCollIn = a.coll.Name
		a.renameCollOpen = true
	}
	if m.Item("新建环境…").Chosen() {
		a.newEnv = true
		a.newEnvIn = ""
	}
	m.Separator()
	if m.Item("打开其他集合…").Chosen() {
		if paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择集合目录", Directory: true}); err == nil && len(paths) > 0 {
			a.openCollection(paths[0])
		}
	}
	if m.Item("新建集合…").Chosen() {
		if path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "选择集合目录位置"}); err == nil && path != "" {
			if _, err := CreateCollection(path, filepath.Base(path)); err == nil {
				a.openCollection(path)
			}
		}
	}
	if len(a.recentDirs) > 1 {
		m.Separator()
		for _, dir := range a.recentDirs {
			if a.coll != nil && dir == a.coll.Root {
				continue
			}
			dir := dir
			if m.Item("切换到: " + filepath.Base(dir)).Chosen() {
				a.openCollection(dir)
			}
		}
	}
	m.Separator()
	if m.Item("关闭当前集合").Chosen() {
		a.coll = nil
		a.open = nil
		a.active = ""
		a.tabs = map[string]*tabState{}
		a.saveLastDir("")
	}
}

// envMenu lists the environments with edit actions.
func (a *app) envMenu(m *ui.Menu) {
	if m.Item("新建环境…").Chosen() {
		a.newEnv = true
		a.newEnvIn = ""
	}
	if m.Item("集合设置…").Chosen() {
		a.initCollectionSettingsEditors()
		a.collSettingsOpen = true
	}
	if len(a.coll.Environments) > 0 {
		m.Separator()
	}
	for _, e := range a.coll.Environments {
		env := e
		if m.Item("编辑 " + e.Name + "…").Chosen() {
			a.envEditor = true
			a.envTarget = env
			draft := *env
			draft.Vars = append(bru.Dict{}, env.Vars...)
			draft.Secrets = append([]string{}, env.Secrets...)
			a.envDraft = &draft
		}
	}
}

// envPicker is the top-right environment selector, as in Bruno.
func (a *app) envPicker(c *ui.Context) {
	names := []string{"No Environment"}
	for _, e := range a.coll.Environments {
		names = append(names, e.Name)
	}
	if a.envSel == "" {
		a.envSel = "No Environment"
	}
	eyeBtn := ui.Button(c, "").Padding(5, 7).Label("查看环境变量")
	eyeBtn.Children(func() { ui.Icon(c, iconEye).Size(15, 15) })
	if eyeBtn.Clicked() {
		a.envInspector = true
	}
	if ui.Select(c, &a.envSel, names).Changed() {
		env := a.envSel
		if env == "No Environment" {
			env = ""
		}
		a.coll.SetActiveEnv(env)
	}
	cfgBtn := ui.Button(c, "").Padding(5, 7).Label("环境设置")
	cfgBtn.Children(func() { ui.Icon(c, iconSettings).Size(15, 15) })
	cfgBtn.Menu(a.envMenu)
}

// sidebar renders the request tree with folder grouping and a filter.
func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Surface).PaddingY(6).Children(func() {
		// Bruno collection header: icon, name, and action buttons.
		hdr := ui.Row(c).Padding(4, 12).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Image(c, brunoLogoSVG).Size(18, 18)
			ui.Text(c, a.coll.Name).Bold().FontSize(13).Grow(1)
			addBtn := ui.Button(c, "").Padding(4, 6).Label("新建")
			addBtn.Children(func() { ui.Icon(c, iconPlus).Size(14, 14) })
			addBtn.Menu(a.collectionMenu)

			moreBtn := ui.Button(c, "").Padding(4, 6).Label("集合操作")
			moreBtn.Children(func() { ui.Icon(c, iconDots).Size(14, 14) })
			moreBtn.Menu(a.collectionMenu)
		})
		hdr.ContextMenu(a.collectionMenu)
		// Filter search field
		ui.Row(c).Padding(2, 10, 8, 10).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.SearchField(c, &a.filter).Grow(1).Placeholder("筛选请求...")
		})
		ui.Scroll(c).Grow(1).Padding(2, 4).Children(func() {
			a.treeLevel(c, "", 0)
		})
	})
}

// matchesFilter reports whether a request name or URL hits the filter.
func (a *app) matchesFilter(r *BruRequest) bool {
	if a.filter == "" {
		return true
	}
	q := strings.ToLower(a.filter)
	return strings.Contains(strings.ToLower(r.Req.Meta.Name), q) ||
		strings.Contains(strings.ToLower(r.Req.URL), q) ||
		strings.Contains(strings.ToLower(r.FileName), q)
}

// folderChildren returns direct child folders of parent ("" = root),
// in collection order.
func (a *app) folderChildren(parent string) []*Folder {
	prefix := parent
	var out []*Folder
	for _, f := range a.coll.Folders {
		if parentOf(f.Path) == prefix {
			out = append(out, f)
		}
	}
	return out
}

// requestsIn returns direct requests of folder in seq order.
func (a *app) requestsIn(folder string) []*BruRequest {
	var out []*BruRequest
	for _, r := range a.coll.Requests {
		if r.Folder == folder {
			out = append(out, r)
		}
	}
	return out
}

// treeLevel renders one level of the collection tree at depth: folder
// rows with a chevron, then request rows with a colored method — as
// Bruno's sidebar draws them.
func (a *app) treeLevel(c *ui.Context, parent string, depth int) {
	t := c.Theme()
	ui.Column(c).Gap(1).Children(func() {
		for _, f := range a.folderChildren(parent) {
			f := f
			open, known := a.folderOpen[f.Path]
			if !known {
				open = true
			}
			hasMatch := a.filter != "" && a.folderHasMatch(f.Path)
			if a.filter != "" && !hasMatch {
				continue
			}
			chevronSvg := iconChevronDown
			folderSvg := iconFolderOpen
			if !open && a.filter == "" {
				chevronSvg = iconChevronRight
				folderSvg = iconFolder
			}
			row := ui.Row(c).Gap(5).Padding(4, 8).PaddingStart(6 + float32(depth)*14).
				AlignItems(ui.Center).Radius(4).Children(func() {
				ui.Icon(c, chevronSvg).Size(12, 12).TextColor(t.TextMuted)
				ui.Icon(c, folderSvg).Size(15, 15).TextColor(t.Accent)
				ui.Text(c, f.Name).FontSize(12.5).Grow(1)
			})
			if row.Clicked() {
				a.folderOpen[f.Path] = !open
			}
			row.ContextMenu(func(m *ui.Menu) { a.folderMenu(m, f) })
			if open || a.filter != "" {
				a.treeLevel(c, f.Path, depth+1)
			}
		}
		for _, r := range a.requestsIn(parent) {
			r := r
			if !a.matchesFilter(r) {
				continue
			}
			label := r.Req.Meta.Name
			if label == "" {
				label = r.FileName
			}
			m := strings.ToUpper(r.Req.Method)
			row := ui.Row(c).Gap(6).Padding(3, 8).PaddingStart(6 + float32(depth)*14).
				AlignItems(ui.Center).Radius(4).Children(func() {
				ui.Box(c).Background(methodBgColor(t, m)).Radius(3).Padding(1, 4).Width(38).Children(func() {
					ui.Text(c, m).FontSize(10).Bold().TextColor(methodColor(t, m))
				})
				ui.Text(c, label).FontSize(12.5).Grow(1)
			})
			if a.active == r.Path {
				row.Background(t.Accent.Alpha(0.14))
			}
			if row.Clicked() {
				a.openTab(r)
			}
			row.ContextMenu(func(m *ui.Menu) { a.requestMenu(m, r) })
		}
	})
}

// folderHasMatch reports whether the subtree contains a filtered name.
func (a *app) folderHasMatch(path string) bool {
	for _, r := range a.coll.Requests {
		if (r.Folder == path || strings.HasPrefix(r.Path, path+"/")) && a.matchesFilter(r) {
			return true
		}
	}
	return false
}

// requestMenu is the context menu of a request row.
func (a *app) requestMenu(m *ui.Menu, r *BruRequest) {
	if m.Item("打开").Chosen() {
		a.openTab(r)
	}
	m.Separator()
	if m.Item("重命名…").Chosen() {
		a.renameTarget = r
		a.renameIn = r.Req.Meta.Name
		a.renameOpen = true
	}
	if m.Item("克隆").Chosen() {
		if dup, err := a.coll.CloneRequest(r); err == nil {
			a.openTab(dup)
		}
	}
	if m.Item("新建请求到此目录").Chosen() {
		a.folderCtx = r.Folder
		a.newRequest = true
		a.newRequestIn = ""
		a.newRequestMethod = "GET"
		a.newRequestURL = ""
	}
	m.Separator()
	if m.Item("删除").Chosen() {
		a.deleteTarget = r
		a.deleteOpen = true
	}
}

// folderMenu is the context menu of a folder row.
func (a *app) folderMenu(m *ui.Menu, f *Folder) {
	if m.Item("新建请求").Chosen() {
		a.folderCtx = f.Path
		a.newRequest = true
		a.newRequestIn = ""
		a.newRequestMethod = "GET"
		a.newRequestURL = ""
	}
	if m.Item("新建子目录").Chosen() {
		a.folderCtx = f.Path
		a.newFolder = true
		a.newFolderIn = ""
	}
	if m.Item("重命名目录…").Chosen() {
		a.renameFolder = f
		a.renameFolderIn = f.Name
		a.renameFolderOpen = true
	}
	m.Separator()
	if m.Item("删除目录…").Chosen() {
		a.deleteFolder = f
		a.deleteFolderOpen = true
	}
}

// workbench renders tabs plus the active request editor.
func (a *app) workbench(c *ui.Context) {
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		a.tabBar(c)
		br, ts := a.activeTab()
		if br == nil {
			t := c.Theme()
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Image(c, brunoLogoSVG).Size(44, 44).Grayscale()
				ui.Text(c, "选择或新建一个请求").Bold().FontSize(15).TextColor(t.TextMuted)
				ui.Text(c, "快捷键: ⌘N 新建请求 • ⌘Enter 发送请求 • ⌘S 保存").FontSize(12).TextColor(t.TextMuted)
				ui.Row(c).Gap(8).Padding(4, 0).Children(func() {
					newBtn := ui.PrimaryButton(c, "").Gap(6)
					newBtn.Children(func() {
						ui.Icon(c, iconPlus).Size(14, 14)
						ui.Text(c, "新建请求")
					})
					if newBtn.Clicked() {
						a.folderCtx = ""
						a.newRequest = true
						a.newRequestIn = ""
						a.newRequestMethod = "GET"
						a.newRequestURL = ""
					}
					cfgBtn := ui.Button(c, "").Gap(6)
					cfgBtn.Children(func() {
						ui.Icon(c, iconSettings).Size(14, 14)
						ui.Text(c, "集合设置")
					})
					if cfgBtn.Clicked() {
						a.initCollectionSettingsEditors()
						a.collSettingsOpen = true
					}
				})
			})
			return
		}
		a.editor(c, br, ts)
	})
}

// tabBar renders open tabs with close buttons and Bruno-style method badges.
func (a *app) tabBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(0).Padding(0, 4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).AlignItems(ui.Center).Children(func() {
		ui.ScrollHorizontal(c).Grow(1).Children(func() {
			ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
				for _, tb := range a.open {
					tb := tb
					label := tb.Req.Meta.Name
					if label == "" {
						label = tb.FileName
					}
					m := strings.ToUpper(tb.Req.Method)
					isActive := tb.Path == a.active
					el := ui.Row(c).Gap(5).Padding(6, 10).AlignItems(ui.Center).Radius(4, 4, 0, 0).Children(func() {
						ui.Text(c, m).FontSize(10).Bold().TextColor(methodColor(t, m))
						ui.Text(c, label).FontSize(12)
						closeBtn := ui.ButtonBase(c.Key(tb.Path+"-x")).Padding(2, 2).Radius(3).Label("关闭标签")
						closeBtn.Children(func() {
							ui.Icon(c, iconX).Size(12, 12).TextColor(t.TextMuted)
						})
						if closeBtn.Clicked() {
							a.closeTab(tb.Path)
						}
					})
					if isActive {
						el.Background(t.Background).BorderWidth(0, 0, 2, 0).BorderColor(t.Accent)
					} else {
						el.Background(t.Surface.Alpha(0.5))
					}
					if el.Clicked() {
						a.active = tb.Path
					}
					el.ContextMenu(func(m *ui.Menu) {
						if m.Item("关闭").Chosen() {
							a.closeTab(tb.Path)
						}
						if m.Item("关闭其他标签").Chosen() {
							a.open = []*BruRequest{tb}
							a.active = tb.Path
						}
						if m.Item("关闭全部标签").Chosen() {
							a.open = nil
							a.active = ""
						}
					})
				}
				newTabBtn := ui.Button(c, "").Padding(5, 7).Label("新建请求")
				newTabBtn.Children(func() {
					ui.Icon(c, iconPlus).Size(13, 13)
				})
				if newTabBtn.Clicked() {
					a.folderCtx = ""
					a.newRequest = true
					a.newRequestIn = ""
					a.newRequestMethod = "GET"
					a.newRequestURL = ""
				}
			})
		})
		// Bruno environment picker & layout toggle at top right
		ui.Row(c).Gap(4).Padding(0, 8).AlignItems(ui.Center).Children(func() {
			a.envPicker(c)
			layoutSvg := iconLayoutColumns
			if !a.layoutHorizontal {
				layoutSvg = iconLayoutRows
			}
			layoutBtn := ui.Button(c, "").Padding(5, 7).Label("切换面板布局")
			layoutBtn.Children(func() {
				ui.Icon(c, layoutSvg).Size(15, 15)
			})
			if layoutBtn.Clicked() {
				a.layoutHorizontal = !a.layoutHorizontal
			}
		})
	})
}

// editor renders the request editor and response pane in side-by-side or vertical split.
func (a *app) editor(c *ui.Context, br *BruRequest, ts *tabState) {
	if a.layoutHorizontal {
		ui.SplitPercent(c, &a.vsplit, func() {
			a.requestPane(c, br, ts)
		}, func() {
			a.response(c, ts)
		}).Grow(1)
	} else {
		ui.SplitVerticalPercent(c, &a.vsplit, func() {
			a.requestPane(c, br, ts)
		}, func() {
			a.response(c, ts)
		}).Grow(1)
	}
}

// requestPane renders the method, URL bar, sub-tabs, and section content.
func (a *app) requestPane(c *ui.Context, br *BruRequest, ts *tabState) {
	r := br.Req
	t := c.Theme()
	ui.Column(c).Fill().MinHeight(0).MinWidth(0).Children(func() {
		// Breadcrumb path (collection / folder / request)
		ui.Row(c).Padding(8, 12, 0, 12).Gap(4).AlignItems(ui.Center).Children(func() {
			if a.coll != nil {
				ui.Text(c, a.coll.Name).FontSize(11).TextColor(t.TextMuted)
			}
			if br.Folder != "" {
				for _, seg := range strings.Split(br.Folder, "/") {
					ui.Text(c, "/").FontSize(11).TextColor(t.TextMuted)
					ui.Text(c, seg).FontSize(11).TextColor(t.TextMuted)
				}
			}
			ui.Text(c, "/").FontSize(11).TextColor(t.TextMuted)
			name := r.Meta.Name
			if name == "" {
				name = br.FileName
			}
			ui.Text(c, name).FontSize(11).Bold().TextColor(t.Text)
		})

		// Request line: colored method select, URL, save, send — as Bruno's.
		ui.Row(c).Padding(8, 12, 10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			m := strings.ToUpper(ts.method)
			anchor := ui.Row(c).Gap(5).AlignItems(ui.Center).Padding(6, 10).
				Background(t.Surface).Radius(6).BorderWidth(1).BorderColor(t.Border).Children(func() {
				ui.Text(c, m).FontSize(12).Bold().TextColor(methodColor(t, m))
				ui.Icon(c, iconChevronDown).Size(12, 12).TextColor(t.TextMuted)
			})
			ui.Popover(c, anchor, &a.methodOpen, func() {
				ui.Column(c).Gap(0).Children(func() {
					for _, mm := range methods {
						mm := mm
						if ui.Row(c).Padding(6, 14).Children(func() {
							ui.Text(c, mm).FontSize(12).Bold().TextColor(methodColor(t, mm))
						}).Clicked() {
							ts.method = mm
							a.methodOpen = false
							a.syncFromUI(br, ts)
							a.save(br)
						}
					}
				})
			})
			if anchor.Clicked() {
				a.methodOpen = true
			}
			urlInput := ui.TextInput(c, &r.URL).Grow(1).Placeholder("https://api.example.com/...")
			urlInput.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter && (k.Mods == 0 || k.Mods == ui.Cmd) {
					r.URL = k.Text()
					extractQueryToParams(r)
					a.save(br)
					a.send(br, ts)
					return true
				}
				return false
			})
			if urlInput.Changed() {
				extractQueryToParams(r)
				a.save(br)
			}
			saveBtn := ui.Button(c, "").Gap(5)
			saveBtn.Children(func() {
				ui.Icon(c, iconDeviceFloppy).Size(14, 14)
				ui.Text(c, "保存")
			})
			if saveBtn.Clicked() {
				a.syncFromUI(br, ts)
				a.save(br)
			}
			if ts.sending {
				cancelBtn := ui.Button(c, "").Gap(5)
				cancelBtn.Children(func() {
					ui.Icon(c, iconX).Size(14, 14)
					ui.Text(c, "取消")
				})
				if cancelBtn.Clicked() && ts.cancel != nil {
					ts.cancel()
				}
			} else {
				sendBtn := ui.PrimaryButton(c, "").Gap(5)
				sendBtn.Children(func() {
					ui.Icon(c, iconSend).Size(14, 14)
					ui.Text(c, "发送")
				})
				if sendBtn.Clicked() {
					a.send(br, ts)
				}
			}
		})

		// Bruno's section sub-tabs with count/status badges
		sections := []string{"Params", "Body", "Headers", "Auth", "Vars", "Script", "Tests", "Assert", "Docs"}
		{
			tabs := ui.TabsBase(c, &ts.section, len(sections))
			tabs.List.Gap(2).Padding(0, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				for i, name := range sections {
					i, name := i, name
					tab := tabs.Tab(i).Padding(8, 8)
					tab.Children(func() {
						col := t.TextMuted
						if i == ts.section {
							col = t.Text
						}
						badgeCount := 0
						badgeDot := false
						switch name {
						case "Params":
							badgeCount = len(r.ParamsQuery) + len(r.ParamsPath)
						case "Headers":
							badgeCount = len(r.Headers)
						case "Vars":
							badgeCount = len(r.VarsPreRequest) + len(r.VarsPostResponse)
						case "Assert":
							badgeCount = len(r.Assert)
						case "Body":
							badgeDot = r.Body != "" && r.Body != "none"
						case "Auth":
							badgeDot = r.Auth != "" && r.Auth != "none" && r.Auth != "inherit"
						case "Script":
							badgeDot = strings.TrimSpace(r.ScriptPreRequest) != "" || strings.TrimSpace(r.ScriptPostResponse) != ""
						case "Tests":
							badgeDot = strings.TrimSpace(r.Tests) != ""
						case "Docs":
							badgeDot = strings.TrimSpace(r.Docs) != ""
						}

						ui.Column(c).Gap(3).Children(func() {
							ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
								ui.Text(c, name).FontSize(12).TextColor(col)
								if badgeCount > 0 {
									ui.Box(c).Background(t.Surface).Radius(8).Padding(0, 5).Children(func() {
										ui.Text(c, fmt.Sprintf("%d", badgeCount)).FontSize(10).TextColor(t.TextMuted)
									})
								} else if badgeDot {
									ui.Box(c).Width(6).Height(6).Radius(3).Background(t.Accent)
								}
							})
							under := ui.Box(c).Height(2).FillWidth()
							if i == ts.section {
								under.Background(t.Accent)
							}
						})
					})
				}
			})
		}
		ui.Scroll(c).Grow(1).Padding(8, 12).Children(func() {
			switch ts.section {
			case 0:
				a.dictTable(c, &r.ParamsQuery, "Query Parameters", br, ts)
				ui.Box(c).Height(10)
				a.dictTable(c, &r.ParamsPath, "Path Variables (:name)", br, ts)
			case 1:
				a.bodyEditor(c, br, ts)
			case 2:
				a.dictTable(c, &r.Headers, "Headers", br, ts)
			case 3:
				a.authEditor(c, br, ts)
			case 4:
				a.dictTable(c, &r.VarsPreRequest, "Pre Request Variables", br, ts)
				ui.Box(c).Height(10)
				a.dictTable(c, &r.VarsPostResponse, "Post Response Variables", br, ts)
			case 5:
				ui.Text(c, "Pre Request").Bold().FontSize(12).Padding(0, 0, 4, 0)
				editor.View(c.Key(br.Path+"-pre"), ts.scriptPre).Height(160).FillWidth()
				ui.Text(c, "Post Response").Bold().FontSize(12).Padding(8, 0, 4, 0)
				editor.View(c.Key(br.Path+"-post"), ts.scriptPost).Height(160).FillWidth()
			case 6:
				ui.Text(c, "Tests (JavaScript)").Bold().FontSize(12).Padding(0, 0, 4, 0)
				editor.View(c.Key(br.Path+"-tests"), ts.testsEd).Height(300).FillWidth()
			case 7:
				a.dictTable(c, &r.Assert, "Assertions (Expr -> Expected)", br, ts)
			case 8:
				a.docsSection(c, br, ts)
			}
		})
		// Buffers persist on save via the section watchers.
		a.watchBuffers(c, br, ts)
	})
}

func (a *app) docsSection(c *ui.Context, br *BruRequest, ts *tabState) {
	t := c.Theme()
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "文档说明 (Markdown)").Bold().FontSize(12).Grow(1)
			btnIcon := iconEye
			btnLabel := "预览 Markdown"
			if ts.docsPreview {
				btnIcon = iconCode
				btnLabel = "编辑源码"
			}
			toggleBtn := ui.Button(c, "").Gap(4).Padding(4, 8)
			toggleBtn.Children(func() {
				ui.Icon(c, btnIcon).Size(13, 13)
				ui.Text(c, btnLabel).FontSize(11)
			})
			if toggleBtn.Clicked() {
				ts.docsPreview = !ts.docsPreview
			}
		})
		if ts.docsPreview {
			src := ts.docsEd.Buffer.String()
			if strings.TrimSpace(src) == "" {
				ui.Box(c).Padding(24, 12).Background(t.Surface.Alpha(0.4)).Radius(6).Children(func() {
					ui.Text(c, "暂无文档内容，点击右上角「编辑源码」撰写接口文档").TextColor(t.TextMuted).FontSize(12)
				})
			} else {
				ui.Box(c).Padding(12, 14).Background(t.Surface.Alpha(0.3)).Radius(6).BorderWidth(1).BorderColor(t.Border).Children(func() {
					markdown.View(c, src, &a.mdOpts)
				})
			}
		} else {
			editor.View(c.Key(br.Path+"-docs"), ts.docsEd).Height(300).FillWidth()
		}
	})
}

// watchBuffers saves the file when an editor buffer's version moved
// past the version last flushed.
func (a *app) watchBuffers(c *ui.Context, br *BruRequest, ts *tabState) {
	if ts.savedVers == nil {
		ts.savedVers = map[*editor.Buffer]uint64{}
	}
	dirty := false
	flush := func(b *editor.Buffer, apply func(string)) {
		if b == nil {
			return
		}
		v := b.Version()
		if ts.savedVers[b] == v {
			return
		}
		ts.savedVers[b] = v
		apply(b.String())
		dirty = true
	}
	flush(ts.bodyBuf, func(s string) { setBodyText(br.Req, s) })
	flush(ts.gqlVarsBuf, func(s string) { br.Req.BodyGraphQLVars = s })
	flush(ts.scriptPre.Buffer, func(s string) { br.Req.ScriptPreRequest = s })
	flush(ts.scriptPost.Buffer, func(s string) { br.Req.ScriptPostResponse = s })
	flush(ts.testsEd.Buffer, func(s string) { br.Req.Tests = s })
	flush(ts.docsEd.Buffer, func(s string) { br.Req.Docs = s })
	if dirty {
		a.save(br)
	}
}

// dictTable renders an editable table with columns: Enabled | Key | Value | Action
func (a *app) dictTable(c *ui.Context, d *bru.Dict, label string, br *BruRequest, ts *tabState) {
	onChange := func() {}
	if br != nil && d == &br.Req.ParamsQuery {
		onChange = func() { syncQueryToURL(br.Req) }
	}
	saveFn := func() {
		if br != nil {
			a.save(br)
		}
	}
	a.dictTableCustom(c, d, label, func() string {
		if br != nil {
			return br.Path
		}
		return "coll"
	}(), func() {
		if br != nil && ts != nil {
			a.syncFromUI(br, ts)
		}
		onChange()
		saveFn()
	})
}

func (a *app) dictTableCustom(c *ui.Context, d *bru.Dict, label, keyPrefix string, onEdit func()) {
	t := c.Theme()
	ui.Column(c).Gap(0).Children(func() {
		// Table title / label
		ui.Row(c).Padding(2, 0, 6, 0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).Bold().FontSize(13)
		})
		// Table header
		ui.Row(c).Padding(4, 6).Background(t.Surface.Alpha(0.6)).
			BorderWidth(1, 1, 1, 1).BorderColor(t.Border).Radius(4, 4, 0, 0).
			AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Width(20) // checkbox placeholder
			ui.Text(c, "KEY").FontSize(11).Bold().TextColor(t.TextMuted).Width(180)
			ui.Text(c, "VALUE").FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
			ui.Box(c).Width(24) // delete placeholder
		})
		// Table rows
		for i := range *d {
			i := i
			e := &(*d)[i]
			rowKey := fmt.Sprintf("%s-%s-%d", keyPrefix, label, i)
			ui.Row(c).Gap(6).Padding(3, 6).AlignItems(ui.Center).
				BorderWidth(0, 1, 1, 1).BorderColor(t.Border.Alpha(0.6)).Children(func() {
				if ui.Checkbox(c.Key(rowKey+"-cb"), &e.Enabled, "").Changed() {
					onEdit()
				}
				if ui.TextInput(c.Key(rowKey+"-k"), &e.Key).Width(180).Placeholder("Key").Changed() {
					onEdit()
				}
				if ui.TextInput(c.Key(rowKey+"-v"), &e.Value).Grow(1).Placeholder("Value").Changed() {
					onEdit()
				}
				delBtn := ui.Button(c.Key(rowKey+"-del"), "").Padding(4, 6).Label("删除行")
				delBtn.Children(func() { ui.Icon(c, iconTrash).Size(13, 13).TextColor(t.TextMuted) })
				if delBtn.Clicked() {
					*d = append((*d)[:i], (*d)[i+1:]...)
					onEdit()
				}
			})
		}
		if len(*d) == 0 {
			ui.Row(c).Padding(8, 12).BorderWidth(0, 1, 1, 1).BorderColor(t.Border.Alpha(0.6)).Children(func() {
				ui.Text(c, "无条目，点击下方按钮添加").TextColor(t.TextMuted).FontSize(12)
			})
		}
		// Add item button below table
		ui.Row(c).Padding(8, 0).Children(func() {
			addBtn := ui.Button(c.Key(keyPrefix+"-"+label+"-add"), "").Gap(4)
			addBtn.Children(func() {
				ui.Icon(c, iconPlus).Size(13, 13)
				ui.Text(c, "添加条目").FontSize(12)
			})
			if addBtn.Clicked() {
				*d = append(*d, bru.Entry{Enabled: true})
				onEdit()
			}
		})
	})
}

func beautifyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err == nil {
		return buf.String()
	}
	return raw
}

// bodyEditor renders Bruno-style body mode pills plus editor/table.
func (a *app) bodyEditor(c *ui.Context, br *BruRequest, ts *tabState) {
	r := br.Req
	t := c.Theme()
	kind := orDefaultS(r.Body, "none")

	// Bruno-style body mode pills
	ui.Row(c).Gap(6).Padding(0, 0, 8, 0).AlignItems(ui.Center).Children(func() {
		for _, bk := range bodyKinds {
			bk := bk
			isSel := kind == bk
			btn := ui.Row(c).Padding(3, 8).Radius(4).AlignItems(ui.Center).Children(func() {
				col := t.TextMuted
				if isSel {
					col = t.Accent
				}
				ui.Text(c, bk).FontSize(11).Bold().TextColor(col)
			})
			if isSel {
				btn.Background(t.Accent.Alpha(0.12)).BorderWidth(1).BorderColor(t.Accent)
			} else {
				btn.Background(t.Surface).BorderWidth(1).BorderColor(t.Border.Alpha(0.5))
			}
			if btn.Clicked() {
				setBodyText(r, ts.bodyBuf.String())
				r.Body = bk
				ts.bodyBuf.Set(a.bodyTextOf(r))
				ext := ".txt"
				if bk == "json" || bk == "graphql" {
					ext = ".json"
				} else if bk == "xml" {
					ext = ".xml"
				}
				ts.bodyBuf.SetLanguage(editor.LanguageFor("body" + ext))
				a.save(br)
			}
		}
	})

	switch kind {
	case "none", "":
		ui.Column(c).Padding(24, 0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "此请求无请求体 (No Body)").TextColor(t.TextMuted).FontSize(12)
		})
	case "form-urlencoded", "multipart-form":
		d := &r.BodyFormURLEnc
		if kind == "multipart-form" {
			d = &r.BodyMultipart
		}
		a.dictTable(c, d, kind, br, ts)
	case "graphql":
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "QUERY").Bold().FontSize(11).TextColor(t.TextMuted)
			editor.View(c.Key(br.Path+"-gql-q"), ts.bodyEd).Height(180).FillWidth()
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "VARIABLES (JSON)").Bold().FontSize(11).TextColor(t.TextMuted).Grow(1)
				fmtBtn := ui.Button(c, "").Gap(4).Padding(3, 8)
				fmtBtn.Children(func() {
					ui.Icon(c, iconWand).Size(13, 13)
					ui.Text(c, "格式化 JSON").FontSize(11)
				})
				if fmtBtn.Clicked() {
					formatted := beautifyJSON(ts.gqlVarsBuf.String())
					ts.gqlVarsBuf.Set(formatted)
					r.BodyGraphQLVars = formatted
					a.save(br)
				}
			})
			editor.View(c.Key(br.Path+"-gql-v"), ts.gqlVarsEd).Height(120).FillWidth()
		})
	default:
		if kind == "json" {
			ui.Row(c).Padding(2, 0, 6, 0).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "JSON").Bold().FontSize(11).TextColor(t.TextMuted).Grow(1)
				fmtBtn := ui.Button(c, "").Gap(4).Padding(3, 8)
				fmtBtn.Children(func() {
					ui.Icon(c, iconWand).Size(13, 13)
					ui.Text(c, "格式化 JSON").FontSize(11)
				})
				if fmtBtn.Clicked() {
					formatted := beautifyJSON(ts.bodyBuf.String())
					ts.bodyBuf.Set(formatted)
					setBodyText(r, formatted)
					a.save(br)
				}
			})
		}
		editor.View(c.Key(br.Path+"-body"), ts.bodyEd).Height(280).FillWidth()
	}
}

// authEditor renders the auth kind picker plus per-kind fields.
func (a *app) authEditor(c *ui.Context, br *BruRequest, ts *tabState) {
	r := br.Req
	t := c.Theme()
	mode := orDefaultS(r.Auth, "none")
	ui.Row(c).Gap(8).Padding(0, 0, 8, 0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "认证方式").Bold().FontSize(12)
		if ui.Select(c, &mode, authKinds).Changed() {
			r.Auth = mode
			a.save(br)
		}
	})
	switch mode {
	case "none":
		ui.Column(c).Padding(20, 0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "此请求不使用认证 (No Auth)").TextColor(t.TextMuted).FontSize(12)
		})
	case "basic":
		a.authFields(c, br.Path, &r.AuthBasic, func() { a.save(br) }, "username", "password")
	case "bearer":
		a.authFields(c, br.Path, &r.AuthBearer, func() { a.save(br) }, "token")
	case "apikey":
		a.authAPIKeyFields(c, br.Path, &r.AuthAPIKey, func() { a.save(br) })
	case "digest":
		a.authFields(c, br.Path, &r.AuthDigest, func() { a.save(br) }, "username", "password")
	case "oauth2", "awsv4", "wsse", "ntlm":
		ui.Text(c, "该认证类型暂不支持编辑参数").TextColor(t.TextMuted)
	case "inherit":
		eff := collectionAuthSpec(a.coll, br)
		effLabel := string(eff.Mode)
		if effLabel == "" || effLabel == "none" {
			effLabel = "无 (None)"
		}
		ui.Column(c).Gap(8).Padding(12, 14).Background(t.Surface.Alpha(0.5)).Radius(6).BorderWidth(1).BorderColor(t.Border).Children(func() {
			ui.Text(c, "继承自集合 / 目录的认证配置").Bold().FontSize(12)
			ui.Textf(c, "当前生效认证: %s", strings.ToUpper(effLabel)).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Children(func() {
				cfgBtn := ui.Button(c, "").Gap(5).Padding(4, 8)
				cfgBtn.Children(func() {
					ui.Icon(c, iconSettings).Size(13, 13)
					ui.Text(c, "打开集合认证设置").FontSize(11)
				})
				if cfgBtn.Clicked() {
					a.initCollectionSettingsEditors()
					a.collSettingsTab = 1
					a.collSettingsOpen = true
				}
			})
		})
	}
}

// authAPIKeyFields renders key, value (masked) and placement selector (header/query).
func (a *app) authAPIKeyFields(c *ui.Context, keyPrefix string, d *bru.Dict, onSave func()) {
	ui.Column(c).Gap(8).Padding(8, 0).Children(func() {
		a.authFieldRow(c, keyPrefix, d, "key", false, onSave)
		a.authFieldRow(c, keyPrefix, d, "value", true, onSave)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "placement").Width(90).FontSize(12)
			pl := orDefaultS(dictValue(*d, "placement"), "header")
			if ui.Select(c.Key(keyPrefix+"-auth-placement"), &pl, []string{"header", "query"}).Changed() {
				dictSet(d, "placement", pl)
				onSave()
			}
		})
	})
}

// authFields renders labeled inputs for keys of d.
func (a *app) authFields(c *ui.Context, keyPrefix string, d *bru.Dict, onSave func(), keys ...string) {
	ui.Column(c).Gap(8).Padding(8, 0).Children(func() {
		for _, k := range keys {
			secret := k == "password" || k == "token" || k == "value"
			a.authFieldRow(c, keyPrefix, d, k, secret, onSave)
		}
	})
}

func (a *app) authFieldRow(c *ui.Context, keyPrefix string, d *bru.Dict, k string, secret bool, onSave func()) {
	fieldKey := keyPrefix + "-auth-" + k
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, k).Width(90).FontSize(12)
		v := dictValue(*d, k)
		inp := ui.TextInput(c.Key(fieldKey), &v).Grow(1).Placeholder(k)
		if secret && !a.revealAuth[fieldKey] {
			inp.Password()
		}
		if inp.Changed() {
			dictSet(d, k, v)
			onSave()
		}
		if secret {
			eyeSvg := iconEye
			if a.revealAuth[fieldKey] {
				eyeSvg = iconEyeOff
			}
			eyeBtn := ui.Button(c.Key(fieldKey+"-eye"), "").Padding(5, 7).Label("切换显示")
			eyeBtn.Children(func() { ui.Icon(c, eyeSvg).Size(14, 14) })
			if eyeBtn.Clicked() {
				a.revealAuth[fieldKey] = !a.revealAuth[fieldKey]
			}
		}
	})
}

// dictValue reads a key from a dict.
func dictValue(d bru.Dict, key string) string {
	v, _ := d.Get(key)
	return v
}

// dictSet upserts an enabled entry.
func dictSet(d *bru.Dict, key, value string) {
	for i := range *d {
		if (*d)[i].Key == key {
			(*d)[i].Value = value
			(*d)[i].Enabled = true
			return
		}
	}
	*d = append(*d, bru.Entry{Key: key, Value: value, Enabled: true})
}

// response renders the response pane with status pills, copy button, and headers.
func (a *app) response(c *ui.Context, ts *tabState) {
	t := c.Theme()
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ts.mu.Lock()
		sending, head, res, serr, sentReq, sentAt := ts.sending, ts.head, ts.result, ts.err, ts.sentReq, ts.sentAt
		ts.mu.Unlock()

		ui.Row(c).Padding(8, 12).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			switch {
			case sending:
				ui.Row(c).Gap(5).Padding(2, 8).Background(t.Accent.Alpha(0.12)).Radius(4).AlignItems(ui.Center).Children(func() {
					spin := ui.Icon(c, iconLoader).Size(13, 13).TextColor(t.Accent)
					spin.Rotate(spin.Loop("spin", time.Second, ui.Linear) * 360)
					ui.Text(c, "发送中…").FontSize(12).Bold().TextColor(t.Accent)
				})
			case serr != nil:
				ui.Row(c).Gap(4).Padding(2, 8).Background(t.Danger.Alpha(0.12)).Radius(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "错误: "+serr.Error()).FontSize(12).Bold().TextColor(t.Danger)
				})
			case res != nil && res.ErrorCode != "":
				ui.Row(c).Gap(4).Padding(2, 8).Background(t.Danger.Alpha(0.12)).Radius(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, string(res.ErrorCode)+": "+res.ErrorMessage).FontSize(12).Bold().TextColor(t.Danger)
				})
			case head != nil:
				col := methodGet // Green #10b981
				if head.StatusCode >= 400 {
					col = methodDelete // Red #ef4444
				} else if head.StatusCode >= 300 {
					col = methodPost // Amber #f59e0b
				}
				ui.Box(c).Background(col.Alpha(0.14)).Radius(4).Padding(2, 8).Children(func() {
					ui.Textf(c, "%d %s", head.StatusCode, head.StatusText).Bold().FontSize(12).TextColor(col)
				})
				if res != nil {
					ui.Row(c).Gap(4).Background(t.Surface).Radius(4).Padding(2, 7).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconClock).Size(12, 12).TextColor(t.TextMuted)
						ui.Textf(c, "%.0f ms", res.DurationMs).FontSize(11).TextColor(t.TextMuted)
					})
					sizeStr := humanBytes(res.SizeBytes)
					if res.Truncated {
						sizeStr += " (已截断)"
					}
					ui.Row(c).Gap(4).Background(t.Surface).Radius(4).Padding(2, 7).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconDatabase).Size(12, 12).TextColor(t.TextMuted)
						ui.Text(c, sizeStr).FontSize(11).TextColor(t.TextMuted)
					})
				}
			default:
				ui.Text(c, "Response").Bold().FontSize(13)
			}
			ui.Box(c).Grow(1)
			if head != nil || res != nil {
				headersLabel := "Headers"
				if head != nil && len(head.Headers) > 0 {
					headersLabel = fmt.Sprintf("Headers (%d)", len(head.Headers))
				}
				ui.Segmented(c, &ts.respSection, "Body", headersLabel, "Timeline")
				if ts.respSection == 0 && res != nil && res.BodyKind == BodyKindText && res.ErrorCode == "" {
					if ui.Segmented(c, &ts.respFormat, "Pretty", "Raw").Changed() {
						ts.mu.Lock()
						ts.respBuf.Set(respDisplayTextFor(ts.result, ts.respFormat))
						ts.mu.Unlock()
					}
				}
				copied := time.Now().Before(a.copyTimer)
				copyIcon := iconCopy
				copyText := "复制"
				if copied {
					copyIcon = iconCheck
					copyText = "已复制"
				}
				copyBtn := ui.Button(c, "").Gap(4).Padding(4, 8)
				copyBtn.Children(func() {
					ui.Icon(c, copyIcon).Size(13, 13)
					ui.Text(c, copyText).FontSize(11)
				})
				if copyBtn.Clicked() {
					a.services.WriteClipboard(ts.respBuf.String())
					a.copyTimer = time.Now().Add(2 * time.Second)
				}
				clearBtn := ui.Button(c, "").Gap(4).Padding(4, 8).Label("清空响应")
				clearBtn.Children(func() {
					ui.Icon(c, iconTrash).Size(13, 13)
					ui.Text(c, "清空").FontSize(11)
				})
				if clearBtn.Clicked() {
					ts.mu.Lock()
					ts.head = nil
					ts.result = nil
					ts.err = nil
					ts.respBuf.Set("")
					ts.mu.Unlock()
				}
			}
		})
		if ts.respSection == 1 && head != nil {
			ui.Scroll(c).Grow(1).Padding(8, 12).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					ui.Row(c).Padding(4, 6).Background(t.Surface.Alpha(0.6)).
						BorderWidth(1, 1, 1, 1).BorderColor(t.Border).Radius(4, 4, 0, 0).Children(func() {
						ui.Text(c, "HEADER").FontSize(11).Bold().TextColor(t.TextMuted).Width(220)
						ui.Text(c, "VALUE").FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
					})
					for _, h := range head.Headers {
						ui.Row(c).Gap(8).Padding(4, 6).
							BorderWidth(0, 1, 1, 1).BorderColor(t.Border.Alpha(0.6)).Children(func() {
							ui.Text(c, h.Key).Bold().FontSize(12).Width(220).Selectable()
							ui.Text(c, h.Value).FontSize(12).Selectable()
						})
					}
				})
			})
		} else if ts.respSection == 2 && (head != nil || res != nil) {
			ui.Scroll(c).Grow(1).Padding(10, 14).Children(func() {
				ui.Column(c).Gap(6).Selectable().Children(func() {
					if !sentAt.IsZero() {
						ui.Textf(c, "* 发起时间: %s", sentAt.Format("2006-01-02 15:04:05.000")).FontSize(12).TextColor(t.TextMuted)
					}
					if sentReq != nil {
						ui.Textf(c, "> %s %s", sentReq.Method, sentReq.URL).Bold().FontSize(12.5).TextColor(methodColor(t, sentReq.Method))
						for _, h := range sentReq.Headers {
							if h.Enabled {
								ui.Textf(c, "> %s: %s", h.Key, h.Value).FontSize(12).TextColor(t.TextMuted)
							}
						}
					}
					if head != nil {
						ui.Box(c).Height(6)
						ui.Textf(c, "* 最终地址: %s", head.URL).FontSize(12).TextColor(t.TextMuted)
						col := methodGet
						if head.StatusCode >= 400 {
							col = methodDelete
						} else if head.StatusCode >= 300 {
							col = methodPost
						}
						ui.Textf(c, "< %s %d %s", head.Protocol, head.StatusCode, head.StatusText).Bold().FontSize(12.5).TextColor(col)
						for _, h := range head.Headers {
							ui.Textf(c, "< %s: %s", h.Key, h.Value).FontSize(12).TextColor(t.TextMuted)
						}
					}
					if res != nil {
						ui.Box(c).Height(6)
						if res.ErrorCode != "" {
							ui.Textf(c, "* 错误 (%s): %s (%.1f ms)", res.ErrorCode, res.ErrorMessage, res.DurationMs).FontSize(12).TextColor(t.Danger)
						} else {
							ui.Textf(c, "* 接收完成: %s, 耗时 %.1f ms", humanBytes(res.SizeBytes), res.DurationMs).FontSize(12).TextColor(t.TextMuted)
						}
					}
				})
			})
		} else if head == nil && res == nil && !sending {
			// Clean empty state matching Bruno
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Image(c, brunoLogoSVG).Size(40, 40).Grayscale()
				ui.Text(c, "尚无响应").Bold().FontSize(15).TextColor(t.TextMuted)
				ui.Text(c, "点击「发送」或按 ⌘Enter 发起请求并查看响应").FontSize(12).TextColor(t.TextMuted)
			})
		} else {
			editor.View(c, ts.respEd).Grow(1).FillWidth()
		}
	})
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ---- modals ----

func (a *app) modals(c *ui.Context) {
	a.newRequestModal(c)
	a.newFolderModal(c)
	a.renameModal(c)
	a.renameFolderModal(c)
	a.renameCollModal(c)
	a.deleteModal(c)
	a.deleteFolderModal(c)
	a.envEditorModal(c)
	a.newEnvModal(c)
	a.envInspectorModal(c)
	a.collectionSettingsModal(c)
}

func (a *app) collectionSettingsModal(c *ui.Context) {
	ui.Modal(c, &a.collSettingsOpen, func() {
		if a.coll == nil || a.coll.Dot == nil {
			return
		}
		t := c.Theme()
		dot := a.coll.Dot
		ui.Column(c).Width(560).Gap(10).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "集合设置 — "+a.coll.Name).Bold().FontSize(15).Grow(1)
			})
			ui.Tabs(c, &a.collSettingsTab, "Headers", "Auth", "Vars", "Script", "Tests", "Docs")
			ui.Scroll(c).Height(340).Padding(6, 2).Children(func() {
				switch a.collSettingsTab {
				case 0:
					a.dictTableCustom(c, &dot.Headers, "Collection Headers", "coll-hdr", func() {
						a.coll.SaveCollectionBru()
					})
				case 1:
					mode := orDefaultS(dot.Auth, "none")
					collAuthKinds := []string{"none", "basic", "bearer", "apikey", "digest"}
					ui.Row(c).Gap(8).Padding(0, 0, 8, 0).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "集合默认认证").Bold().FontSize(12)
						if ui.Select(c.Key("coll-auth-mode"), &mode, collAuthKinds).Changed() {
							dot.Auth = mode
							a.coll.SaveCollectionBru()
						}
					})
					switch mode {
					case "none":
						ui.Text(c, "集合未配置默认认证 (子请求选择 inherit 时将不发送认证)").TextColor(t.TextMuted).FontSize(12)
					case "basic":
						a.authFields(c, "coll", &dot.AuthBasic, func() { a.coll.SaveCollectionBru() }, "username", "password")
					case "bearer":
						a.authFields(c, "coll", &dot.AuthBearer, func() { a.coll.SaveCollectionBru() }, "token")
					case "apikey":
						a.authAPIKeyFields(c, "coll", &dot.AuthAPIKey, func() { a.coll.SaveCollectionBru() })
					case "digest":
						a.authFields(c, "coll", &dot.AuthDigest, func() { a.coll.SaveCollectionBru() }, "username", "password")
					}
				case 2:
					a.dictTableCustom(c, &dot.Vars, "Collection Variables", "coll-vars", func() {
						a.coll.SaveCollectionBru()
					})
				case 3:
					if a.collScriptPre != nil && a.collScriptPost != nil {
						ui.Text(c, "Pre Request").Bold().FontSize(12)
						editor.View(c.Key("coll-pre"), a.collScriptPre).Height(130).FillWidth()
						ui.Text(c, "Post Response").Bold().FontSize(12).Padding(6, 0, 0, 0)
						editor.View(c.Key("coll-post"), a.collScriptPost).Height(130).FillWidth()
					}
				case 4:
					if a.collTestsEd != nil {
						ui.Text(c, "Collection Tests").Bold().FontSize(12)
						editor.View(c.Key("coll-tests"), a.collTestsEd).Height(260).FillWidth()
					}
				case 5:
					if a.collDocsEd != nil {
						ui.Text(c, "Collection Docs (Markdown)").Bold().FontSize(12)
						editor.View(c.Key("coll-docs"), a.collDocsEd).Height(260).FillWidth()
					}
				}
			})
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "保存并关闭").Clicked() {
					if a.collScriptPre != nil {
						dot.ScriptPreRequest = a.collScriptPre.Buffer.String()
					}
					if a.collScriptPost != nil {
						dot.ScriptPostResponse = a.collScriptPost.Buffer.String()
					}
					if a.collTestsEd != nil {
						dot.Tests = a.collTestsEd.Buffer.String()
					}
					if a.collDocsEd != nil {
						dot.Docs = a.collDocsEd.Buffer.String()
					}
					a.coll.SaveCollectionBru()
					a.collSettingsOpen = false
				}
			})
		})
	})
}

func (a *app) envInspectorModal(c *ui.Context) {
	ui.Modal(c, &a.envInspector, func() {
		t := c.Theme()
		ui.Column(c).Width(460).Gap(8).Children(func() {
			ui.Text(c, "当前环境与变量预览").Bold().FontSize(15)
			envName := a.envSel
			if envName == "" {
				envName = "No Environment"
			}
			ui.Text(c, "活动环境: "+envName).FontSize(12).TextColor(t.TextMuted)

			var activeEnv *bru.Environment
			if a.coll != nil {
				for _, e := range a.coll.Environments {
					if e.Name == a.envSel {
						activeEnv = e
						break
					}
				}
			}

			if activeEnv != nil && (len(activeEnv.Vars) > 0 || len(activeEnv.Secrets) > 0) {
				ui.Text(c, "环境变量 ("+activeEnv.Name+")").Bold().FontSize(12).Padding(4, 0)
				for _, v := range activeEnv.Vars {
					col := t.Text
					if !v.Enabled {
						col = t.TextMuted
					}
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, v.Key).Bold().Width(140).TextColor(col)
						ui.Text(c, v.Value).Grow(1).TextColor(col).Selectable()
					})
				}
				for _, s := range activeEnv.Secrets {
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, s).Bold().Width(140)
						ui.Text(c, "•••••••• (Secret)").Grow(1).TextColor(t.TextMuted)
					})
				}
			} else {
				ui.Text(c, "当前环境无变量").TextColor(t.TextMuted).FontSize(11).Padding(4, 0)
			}

			if a.coll != nil && a.coll.Dot != nil && len(a.coll.Dot.Vars) > 0 {
				ui.Text(c, "集合变量").Bold().FontSize(12).Padding(8, 0, 4, 0)
				for _, v := range a.coll.Dot.Vars {
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, v.Key).Bold().Width(140)
						ui.Text(c, v.Value).Grow(1).Selectable()
					})
				}
			}

			ui.Row(c).Justify(ui.End).Gap(8).Padding(12, 0, 0, 0).Children(func() {
				if activeEnv != nil {
					if ui.Button(c, "编辑当前环境…").Clicked() {
						a.envInspector = false
						a.envEditor = true
						a.envTarget = activeEnv
						draft := *activeEnv
						draft.Vars = append(bru.Dict{}, activeEnv.Vars...)
						draft.Secrets = append([]string{}, activeEnv.Secrets...)
						a.envDraft = &draft
					}
				}
				if ui.PrimaryButton(c, "关闭").Clicked() {
					a.envInspector = false
				}
			})
		})
	})
}

func (a *app) newRequestModal(c *ui.Context) {
	ui.Modal(c, &a.newRequest, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			title := "新建请求"
			if a.folderCtx != "" {
				title = "新建请求 (" + a.folderCtx + ")"
			}
			ui.Text(c, title).Bold().FontSize(15)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "名称").Width(50).FontSize(12)
				ui.TextInput(c, &a.newRequestIn).Grow(1).Placeholder("例如: Get Users").AutoFocus()
			})
			if a.newRequestMethod == "" {
				a.newRequestMethod = "GET"
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "方法").Width(50).FontSize(12)
				ui.Select(c, &a.newRequestMethod, methods)
				ui.TextInput(c, &a.newRequestURL).Grow(1).Placeholder("https://api.example.com/users")
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.newRequest = false
				}
				if ui.PrimaryButton(c, "创建").Clicked() && strings.TrimSpace(a.newRequestIn) != "" {
					if br, err := a.coll.AddRequest(a.folderCtx, strings.TrimSpace(a.newRequestIn)); err == nil {
						br.Req.Method = strings.ToLower(orDefaultS(a.newRequestMethod, "GET"))
						if strings.TrimSpace(a.newRequestURL) != "" {
							br.Req.URL = strings.TrimSpace(a.newRequestURL)
							extractQueryToParams(br.Req)
						}
						a.coll.SaveRequest(br)
						a.openTab(br)
					}
					a.newRequest = false
				}
			})
		})
	})
}

func (a *app) newFolderModal(c *ui.Context) {
	ui.Modal(c, &a.newFolder, func() {
		ui.Text(c, "新建目录").Bold()
		ui.TextInput(c, &a.newFolderIn).Placeholder("名称").AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.newFolder = false
			}
			if ui.PrimaryButton(c, "创建").Clicked() && a.newFolderIn != "" {
				a.coll.AddFolder(a.folderCtx, a.newFolderIn)
				a.newFolder = false
			}
		})
	})
}

func (a *app) renameModal(c *ui.Context) {
	ui.Modal(c, &a.renameOpen, func() {
		ui.Text(c, "重命名请求").Bold()
		ui.TextInput(c, &a.renameIn).AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.renameOpen = false
			}
			if ui.PrimaryButton(c, "确定").Clicked() && a.renameTarget != nil {
				a.coll.RenameRequest(a.renameTarget, a.renameIn)
				a.renameOpen = false
			}
		})
	})
}

func (a *app) renameFolderModal(c *ui.Context) {
	ui.Modal(c, &a.renameFolderOpen, func() {
		ui.Text(c, "重命名目录").Bold()
		ui.TextInput(c, &a.renameFolderIn).AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.renameFolderOpen = false
			}
			if ui.PrimaryButton(c, "确定").Clicked() && a.renameFolder != nil {
				a.coll.RenameFolder(a.renameFolder, a.renameFolderIn)
				a.renameFolderOpen = false
			}
		})
	})
}

func (a *app) renameCollModal(c *ui.Context) {
	ui.Modal(c, &a.renameCollOpen, func() {
		ui.Text(c, "重命名集合").Bold()
		ui.TextInput(c, &a.renameCollIn).AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.renameCollOpen = false
			}
			if ui.PrimaryButton(c, "确定").Clicked() && a.coll != nil {
				a.coll.RenameCollection(a.renameCollIn)
				a.renameCollOpen = false
			}
		})
	})
}

func (a *app) deleteModal(c *ui.Context) {
	ui.Modal(c, &a.deleteOpen, func() {
		name := ""
		if a.deleteTarget != nil {
			name = a.deleteTarget.Req.Meta.Name
		}
		ui.Text(c, "删除请求").Bold()
		ui.Text(c, fmt.Sprintf("删除 %q？文件将从磁盘移除。", name))
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.deleteOpen = false
			}
			if ui.PrimaryButton(c, "删除").Clicked() && a.deleteTarget != nil {
				a.closeTab(a.deleteTarget.Path)
				a.coll.DeleteRequest(a.deleteTarget)
				a.deleteOpen = false
			}
		})
	})
}

func (a *app) deleteFolderModal(c *ui.Context) {
	ui.Modal(c, &a.deleteFolderOpen, func() {
		name := ""
		if a.deleteFolder != nil {
			name = a.deleteFolder.Name
		}
		ui.Text(c, "删除目录").Bold()
		ui.Text(c, fmt.Sprintf("确认删除目录 %q 及其下所有请求文件？", name))
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.deleteFolderOpen = false
			}
			if ui.PrimaryButton(c, "删除").Clicked() && a.deleteFolder != nil {
				prefix := a.deleteFolder.Path + "/"
				for i := len(a.open) - 1; i >= 0; i-- {
					if a.open[i].Folder == a.deleteFolder.Path || strings.HasPrefix(a.open[i].Path, prefix) {
						a.closeTab(a.open[i].Path)
					}
				}
				a.coll.DeleteFolder(a.deleteFolder)
				a.deleteFolderOpen = false
			}
		})
	})
}

func (a *app) newEnvModal(c *ui.Context) {
	ui.Modal(c, &a.newEnv, func() {
		ui.Text(c, "新建环境").Bold()
		ui.TextInput(c, &a.newEnvIn).Placeholder("名称").AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.newEnv = false
			}
			if ui.PrimaryButton(c, "创建").Clicked() && a.newEnvIn != "" {
				if env, err := a.coll.AddEnvironment(a.newEnvIn); err == nil {
					a.envSel = env.Name
					a.coll.SetActiveEnv(env.Name)
				}
				a.newEnv = false
			}
		})
	})
}

func (a *app) envEditorModal(c *ui.Context) {
	ui.Modal(c, &a.envEditor, func() {
		if a.envDraft == nil || a.envTarget == nil {
			return
		}
		t := c.Theme()
		ui.Column(c).Width(500).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "环境配置: "+a.envTarget.Name).Bold().FontSize(15).Grow(1)
				delEnvBtn := ui.Button(c, "").Gap(4).Padding(4, 8).TextColor(t.Danger)
				delEnvBtn.Children(func() {
					ui.Icon(c, iconTrash).Size(13, 13)
					ui.Text(c, "删除此环境").FontSize(11)
				})
				if delEnvBtn.Clicked() {
					a.coll.DeleteEnvironment(a.envTarget)
					if a.envSel == a.envTarget.Name {
						a.envSel = "No Environment"
					}
					a.envEditor = false
					return
				}
			})
			ui.Text(c, "环境变量 (支持勾选启用/禁用)").FontSize(12).TextColor(t.TextMuted)
			ui.Scroll(c).MaxHeight(260).Children(func() {
				ui.Column(c).Gap(6).Children(func() {
					for i := range a.envDraft.Vars {
						i := i
						e := &a.envDraft.Vars[i]
						rowKey := fmt.Sprintf("env-%s-%d", a.envTarget.Name, i)
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Checkbox(c.Key(rowKey+"-cb"), &e.Enabled, "")
							ui.TextInput(c.Key(rowKey+"-k"), &e.Key).Width(160).Placeholder("Key")
							ui.TextInput(c.Key(rowKey+"-v"), &e.Value).Grow(1).Placeholder("Value")
							delBtn := ui.Button(c.Key(rowKey+"-del"), "").Padding(4, 6).Label("删除变量")
							delBtn.Children(func() { ui.Icon(c, iconTrash).Size(13, 13) })
							if delBtn.Clicked() {
								a.envDraft.Vars = append(a.envDraft.Vars[:i], a.envDraft.Vars[i+1:]...)
							}
						})
					}
				})
			})
			addVarBtn := ui.Button(c, "").Gap(4)
			addVarBtn.Children(func() {
				ui.Icon(c, iconPlus).Size(13, 13)
				ui.Text(c, "添加变量").FontSize(12)
			})
			if addVarBtn.Clicked() {
				a.envDraft.Vars = append(a.envDraft.Vars, bru.Entry{Enabled: true})
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.envEditor = false
				}
				if ui.PrimaryButton(c, "保存").Clicked() {
					*a.envTarget = *a.envDraft
					a.coll.SaveEnvironment(a.envTarget)
					a.envEditor = false
				}
			})
		})
	})
}

// ---- helpers ----

// parentOf returns the parent dir of a slash path.
func parentOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}

// sortedCopy is unused but kept for future sorting needs.
func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}
