package ui

// The "Generate Code" dialog: the request as it would be sent, in the
// language chosen, ready to copy.

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"gruno/internal/send"
)

// codegenState backs the dialog.
type codegenState struct {
	open   bool
	target *BruRequest
	lang   string
	ed     *editor.State
	err    string
	copied time.Time
}

// langFile names a file whose extension picks the editor's highlighting.
var langFile = map[string]string{
	"cURL":               "x.sh",
	"JavaScript (fetch)": "x.js",
	"Python (requests)":  "x.py",
	"Go (net/http)":      "x.go",
	"HTTPie":             "x.sh",
}

func (a *app) openCodegen(br *BruRequest) {
	a.codegen.open, a.codegen.target = true, br
	if a.codegen.lang == "" {
		a.codegen.lang = send.CodeLangs[0]
	}
	a.generateCode()
}

// generateCode writes the target request in the chosen language, with
// every scope a send would resolve against, secrets and runtime
// variables included.
func (a *app) generateCode() {
	g := &a.codegen
	br := g.target
	if br == nil || a.coll == nil {
		return
	}
	if ts, ok := a.tabs[br.Path]; ok {
		a.syncFromUI(br, ts)
	}
	input := a.coll.sendInputOf(br)
	input.Scopes = a.coll.scriptVarsOf(br, a.runtimeVars, a.secretPairs(a.coll.activeEnvironment())).Scopes()
	code, err := send.GenerateCode(input, g.lang)
	g.err = ""
	if err != nil {
		g.err = err.Error()
		code = ""
	}
	buf := editor.NewBuffer(code)
	buf.SetLanguage(editor.LanguageFor(langFile[g.lang]))
	g.ed = &editor.State{Buffer: buf, ReadOnly: true}
}

func (a *app) codegenModal(c *ui.Context) {
	g := &a.codegen
	ui.Modal(c, &g.open, func() {
		t := c.Theme()
		ui.Column(c).Width(720).Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				title := "生成代码"
				if g.target != nil {
					title += " — " + g.target.Req.Meta.Name
				}
				ui.Text(c, title).Bold().FontSize(15).Grow(1)
				if ui.Select(c.Key("codegen-lang"), &g.lang, send.CodeLangs).Changed() {
					a.generateCode()
				}
				label := "复制"
				if time.Since(g.copied) < 2*time.Second {
					label = "已复制"
				}
				if ui.PrimaryButton(c.Key("codegen-copy"), label).Clicked() && g.ed != nil {
					a.services.WriteClipboard(g.ed.Buffer.String())
					g.copied = time.Now()
				}
			})
			if g.err != "" {
				ui.Text(c, "无法生成："+g.err).FontSize(12).TextColor(t.Danger)
			} else if g.ed != nil {
				editor.View(c.Key("codegen-code"), g.ed).Height(380).FillWidth()
			}
			ui.Text(c, "变量已按当前环境替换，认证信息已附加；OAuth2 的 token 只在发送时获取，代码中是占位符。").
				FontSize(11).TextColor(t.TextMuted)
			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("codegen-close"), "关闭").Clicked() {
					g.open = false
				}
			})
		})
	})
}
