package main

// Gruno is a Bruno workbench in mygo's native UI: .bru
// collections on disk, edited live and sent over Go's HTTP stack.

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"log"
	"os"
)

func main() {
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
	a.restoreLast()
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu},
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
