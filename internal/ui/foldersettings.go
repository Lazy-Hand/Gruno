package ui

// Folder settings: the folder.bru blocks (headers, auth, vars, scripts,
// tests, docs) are read and applied at send time; this modal edits them,
// as Bruno's folder settings do.

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"gruno/bru"
)

// collectionAuthKinds are the auth selectors a collection or folder
// can name (a folder may also inherit).
var collectionAuthKinds = []string{"none", "basic", "bearer", "apikey", "digest", "oauth2"}

// openFolderSettings opens the modal on f, creating folder.bru state
// when the directory has none.
func (a *app) openFolderSettings(f *Folder) {
	if f == nil {
		return
	}
	if f.Bru == nil {
		f.Bru = &bru.CollectionBru{Auth: "none"}
	}
	if f.Bru.Auth == "" {
		f.Bru.Auth = "none"
	}
	mk := func(text, ext string) *editor.State {
		b := editor.NewBuffer(text)
		b.SetLanguage(editor.LanguageFor("x" + ext))
		return &editor.State{Buffer: b}
	}
	a.folderScriptPre = mk(f.Bru.ScriptPreRequest, ".js")
	a.folderScriptPost = mk(f.Bru.ScriptPostResponse, ".js")
	a.folderTestsEd = mk(f.Bru.Tests, ".js")
	a.folderDocsEd = mk(f.Bru.Docs, ".md")
	a.folderSettingsFol = f
	a.folderSettingsTab = 0
	a.folderSettingsOpen = true
}

// saveFolderSettings copies the editors' text back into folder.bru and
// writes the file.
func (a *app) saveFolderSettings() {
	f := a.folderSettingsFol
	if f == nil || f.Bru == nil || a.coll == nil {
		return
	}
	if a.folderScriptPre != nil {
		f.Bru.ScriptPreRequest = a.folderScriptPre.Buffer.String()
	}
	if a.folderScriptPost != nil {
		f.Bru.ScriptPostResponse = a.folderScriptPost.Buffer.String()
	}
	if a.folderTestsEd != nil {
		f.Bru.Tests = a.folderTestsEd.Buffer.String()
	}
	if a.folderDocsEd != nil {
		f.Bru.Docs = a.folderDocsEd.Buffer.String()
	}
	if err := a.coll.SaveFolderBru(f); err != nil {
		a.saveErr = "保存目录设置失败: " + err.Error()
		mygo.Dialog.Error("保存目录设置失败", err.Error())
		return
	}
}

func (a *app) folderSettingsModal(c *ui.Context) {
	ui.Modal(c, &a.folderSettingsOpen, func() {
		f := a.folderSettingsFol
		if f == nil || f.Bru == nil {
			return
		}
		t := c.Theme()
		dot := f.Bru
		title := "目录设置 — " + f.Name
		ui.Column(c).Width(560).Gap(10).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).Bold().FontSize(15).Grow(1)
			})
			ui.Tabs(c, &a.folderSettingsTab, "Headers", "Auth", "Vars", "Script", "Tests", "Docs")
			ui.Scroll(c).Height(340).Padding(6, 2).Children(func() {
				switch a.folderSettingsTab {
				case 0:
					a.dictTableCustom(c, &dot.Headers, "Folder Headers", "folder-hdr", func() {
						a.saveFolderSettings()
					}, nil)
				case 1:
					mode := orDefaultS(dot.Auth, "none")
					ui.Row(c).Gap(8).Padding(0, 0, 8, 0).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "目录认证").Bold().FontSize(12)
						if ui.Select(c.Key("folder-auth-mode"), &mode, collectionAuthKinds).Changed() {
							dot.Auth = mode
							a.saveFolderSettings()
						}
					})
					switch mode {
					case "none":
						ui.Text(c, "目录未配置认证；目录内的请求选择 inherit 时继续向上继承左集合设置").TextColor(t.TextMuted).FontSize(12)
					case "basic":
						a.authFields(c, nil, "folder", &dot.AuthBasic, func() { a.saveFolderSettings() }, "username", "password")
					case "bearer":
						a.authFields(c, nil, "folder", &dot.AuthBearer, func() { a.saveFolderSettings() }, "token")
					case "apikey":
						a.authAPIKeyFields(c, nil, "folder", &dot.AuthAPIKey, func() { a.saveFolderSettings() })
					case "digest":
						a.authFields(c, nil, "folder", &dot.AuthDigest, func() { a.saveFolderSettings() }, "username", "password")
					case "oauth2":
						a.oauth2Fields(c, nil, "folder", &dot.AuthOAuth2, func() { a.saveFolderSettings() })
					}
				case 2:
					a.dictTableCustom(c, &dot.Vars, "Folder Variables", "folder-vars", func() {
						a.saveFolderSettings()
					}, nil)
				case 3:
					ui.Text(c, "Pre Request").Bold().FontSize(12)
					editor.View(c.Key("folder-pre"), a.folderScriptPre).Height(130).FillWidth()
					ui.Text(c, "Post Response").Bold().FontSize(12).Padding(6, 0, 0, 0)
					editor.View(c.Key("folder-post"), a.folderScriptPost).Height(130).FillWidth()
				case 4:
					ui.Text(c, "Folder Tests").Bold().FontSize(12)
					editor.View(c.Key("folder-tests"), a.folderTestsEd).Height(260).FillWidth()
				case 5:
					ui.Text(c, "Folder Docs (Markdown)").Bold().FontSize(12)
					editor.View(c.Key("folder-docs"), a.folderDocsEd).Height(260).FillWidth()
				}
			})
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "保存并关闭").Clicked() {
					a.saveFolderSettings()
					a.folderSettingsOpen = false
				}
			})
		})
	})
}
