package ui

// Run is the application entry point: single instance, app menu, update
// window, main window, event loop.

import (
	"log"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updaterui "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

func Run() {
	if !mygo.App.RequestSingleInstanceLock() {
		return // the first instance was notified; exit
	}
	var first *mygo.Window
	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		if first != nil {
			first.Show()
			first.Focus()
		}
	})

	a := newApp()
	a.secrets = defaultSecretsStore()
	a.restoreLast()
	// Sparkle-style update window in native UI: background checks daily,
	// "Check for Updates…" in the app menu. A no-op in dev builds.
	mygo.Use(updaterui.Plugin)
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Label: "Gruno", Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleAbout},
				updater.MenuItem(),
				mygo.Separator(),
				{Role: mygo.RoleServices},
				mygo.Separator(),
				{Role: mygo.RoleHide},
				{Role: mygo.RoleHideOthers},
				{Role: mygo.RoleUnhide},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			}},
			{Role: mygo.RoleFileMenu},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleWindowMenu},
		}))
		// Give the windows the saved appearance before one is created.
		a.applyThemeSource()
		// GRUNO_AUTOPEN opens the first request on launch, for
		// screenshots and UI automation.
		if os.Getenv("GRUNO_AUTOPEN") != "" && a.coll != nil && len(a.coll.Requests) > 0 {
			a.openTab(a.coll.Requests[0])
		}
		first = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Gruno",
			Width:     1100,
			Height:    720,
			MinWidth:  720,
			MinHeight: 480,
			// Matches ui.Theme.Background so the macOS title bar is tinted
			// with the content's color in either appearance.
			BackgroundColor: "light-dark(#ffffff, #18181b)",
			StateKey:        "main",
			Content:         ui.View(a.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
