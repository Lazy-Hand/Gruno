package ui

// Shared small widgets: the underlined section tabs used by the request
// and response panes, and compact icon buttons.

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// tabItem is one underlined tab: a label with an optional count badge
// or status dot. Badge text replaces the count (e.g. "3/4"); BadgeColor
// tints it.
type tabItem struct {
	Label      string
	Count      int
	Dot        bool
	Badge      string
	BadgeColor ui.Color
}

// sectionTabs draws Bruno-style underlined tabs into a horizontally
// scrolling strip, so a narrow pane scrolls them rather than cutting
// the last ones off.
func sectionTabs(c *ui.Context, key string, sel *int, items []tabItem) {
	t := c.Theme()
	ui.ScrollHorizontal(c.Key(key)).Padding(0, 8).Children(func() {
		tabs := ui.TabsBase(c, sel, len(items))
		tabs.List.Gap(0).Children(func() {
			for i, it := range items {
				i, it := i, it
				tabs.Tab(i).Padding(8, 7, 0, 7).Children(func() {
					active := i == *sel
					col := t.TextMuted
					if active {
						col = t.Text
					}
					ui.Column(c).Gap(6).Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							lbl := ui.Text(c, it.Label).FontSize(12).TextColor(col).NoWrap()
							if active {
								lbl.Bold()
							}
							switch {
							case it.Badge != "":
								bc := it.BadgeColor
								if bc == (ui.Color{}) {
									bc = t.TextMuted
								}
								ui.Box(c).Background(bc.Alpha(0.14)).Radius(8).Padding(0, 5).Children(func() {
									ui.Text(c, it.Badge).FontSize(10).Bold().TextColor(bc).NoWrap()
								})
							case it.Count > 0:
								ui.Box(c).Background(t.Surface).Radius(8).Padding(0, 5).Children(func() {
									ui.Text(c, fmt.Sprint(it.Count)).FontSize(10).TextColor(t.TextMuted).NoWrap()
								})
							case it.Dot:
								ui.Box(c).Width(6).Height(6).Radius(3).Background(t.Accent)
							}
						})
						under := ui.Box(c).Height(2).FillWidth().Radius(1)
						if active {
							under.Background(t.Accent)
						}
					})
				})
			}
		})
	})
}

// iconButton is a compact square button with a tooltip.
func iconButton(c *ui.Context, icon *ui.SVG, tip string) ui.Element {
	b := ui.Button(c, "").Padding(5, 6).Label(tip).Tooltip(tip)
	b.Children(func() { ui.Icon(c, icon).Size(14, 14) })
	return b
}

// shortMethod fits a method into the sidebar badge, as Bruno abbreviates.
func shortMethod(m string) string {
	switch m {
	case "DELETE":
		return "DEL"
	case "OPTIONS":
		return "OPT"
	}
	return m
}
