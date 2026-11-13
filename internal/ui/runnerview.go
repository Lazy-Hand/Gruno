package ui

// runnerview.go draws the collection runner: a pseudo tab, a header
// with the run controls and totals, the executed requests and the
// selected one's tests, response and headers.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"gruno/internal/script"
)

// runnerTabItem is the runner's entry in the tab bar.
func (a *app) runnerTabItem(c *ui.Context) {
	t := c.Theme()
	el := ui.Row(c.Key("runner-tab")).Gap(5).Padding(6, 10).AlignItems(ui.Center).Radius(4, 4, 0, 0).Children(func() {
		ui.Icon(c, iconPlay).Size(11, 11).TextColor(t.Accent)
		ui.Text(c, "运行器").FontSize(12)
		closeBtn := ui.ButtonBase(c.Key("runner-tab-x")).Padding(2, 2).Radius(3).Label("关闭运行器")
		closeBtn.Children(func() {
			ui.Icon(c, iconX).Size(12, 12).TextColor(t.TextMuted)
		})
		if closeBtn.Clicked() {
			a.closeRunner()
		}
	})
	if a.active == runnerTab {
		el.Background(t.Background).BorderWidth(0, 0, 2, 0).BorderColor(t.Accent)
	} else {
		el.Background(t.Surface.Alpha(0.5))
	}
	if el.Clicked() {
		a.active = runnerTab
	}
}

func outcomeLabel(o runOutcome) string {
	switch o {
	case runRunning:
		return "运行中"
	case runPassed:
		return "通过"
	case runFailed:
		return "失败"
	case runError:
		return "错误"
	case runSkipped:
		return "跳过"
	}
	return ""
}

func outcomeColor(t *ui.Theme, o runOutcome) ui.Color {
	switch o {
	case runPassed:
		return t.Success
	case runFailed, runError:
		return t.Danger
	case runSkipped:
		return t.TextMuted
	}
	return t.Accent
}

func outcomeIcon(o runOutcome) *ui.SVG {
	switch o {
	case runPassed:
		return iconCheck
	case runFailed, runError:
		return iconX
	case runSkipped:
		return iconChevronRight
	}
	return iconLoader
}

// runnerView is the runner tab's content.
func (a *app) runnerView(c *ui.Context) {
	rs := a.runner
	t := c.Theme()
	results, running, planned, elapsed, note := rs.snapshot()
	counts := map[runOutcome]int{}
	for _, r := range results {
		counts[r.Outcome]++
	}
	if rs.sel >= len(results) {
		rs.sel = -1
	}

	ui.Column(c).Grow(1).MinWidth(0).Children(func() {
		// Header: scope, options, run/stop.
		ui.Row(c).Gap(10).Padding(12, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, "集合运行器").Bold().FontSize(15)
				scope := "集合 " + rs.title
				if rs.folder != "" {
					scope = "目录 " + rs.title
				}
				ui.Textf(c, "%s · %d 个请求", scope, len(a.runOrder(rs.folder))).FontSize(12).TextColor(t.TextMuted).MaxLines(1)
			})
			ui.Text(c, "请求间隔 (ms)").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c.Key("runner-delay"), &rs.delayIn).Width(70).Placeholder("0")
			dataBtn := ui.Button(c.Key("runner-data"), "").Gap(4).Padding(4, 8)
			dataBtn.Children(func() {
				ui.Icon(c, iconFileText).Size(13, 13)
				label := "数据文件…"
				if rs.dataPath != "" {
					label = filepath.Base(rs.dataPath)
				}
				ui.Text(c, label).FontSize(11)
			})
			if dataBtn.Clicked() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Title:   "选择运行数据文件",
					Filters: []mygo.FileFilter{{Name: "CSV / JSON", Extensions: []string{"csv", "json"}}},
				})
				if err == nil && len(paths) > 0 {
					rows, lerr := loadRunData(paths[0])
					if lerr != nil {
						rs.dataErr = lerr.Error()
						rs.dataRows, rs.dataPath = nil, ""
					} else {
						rs.dataPath, rs.dataRows, rs.dataErr = paths[0], rows, ""
					}
				}
			}
			if rs.dataPath != "" {
				if ui.Button(c.Key("runner-data-clear"), "清除数据").Padding(3, 8).Clicked() {
					rs.dataPath, rs.dataRows, rs.dataErr = "", nil, ""
				}
				ui.Text(c, dataSummary(rs.dataRows)).FontSize(11).TextColor(t.TextMuted).NoWrap()
			}
			if rs.dataErr != "" {
				ui.Text(c, rs.dataErr).FontSize(11).TextColor(t.Danger).MaxLines(1)
			}
			if running {
				stop := ui.Button(c, "").Gap(6).Label("停止运行")
				stop.Children(func() {
					ui.Icon(c, iconStop).Size(13, 13)
					ui.Text(c, "停止")
				})
				if stop.Clicked() {
					a.stopRun()
				}
			} else {
				label := "开始运行"
				if len(results) > 0 {
					label = "重新运行"
				}
				run := ui.PrimaryButton(c, "").Gap(6).Label(label)
				run.Children(func() {
					ui.Icon(c, iconPlay).Size(13, 13)
					ui.Text(c, label)
				})
				if run.Clicked() {
					a.startRun()
				}
			}
		})

		if len(results) == 0 && !running {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Icon(c, iconPlay).Size(30, 30).TextColor(t.TextMuted)
				ui.Text(c, "按顺序发送所有请求，运行脚本、测试和断言").FontSize(13).TextColor(t.TextMuted)
				ui.Text(c, "脚本里可用 bru.setNextRequest / bru.runner.skipRequest / bru.runner.stopExecution 控制流程").FontSize(12).TextColor(t.TextMuted)
			})
			return
		}

		// Totals.
		ui.Row(c).Gap(8).Padding(8, 16).AlignItems(ui.Center).Children(func() {
			pill := func(label string, n int, col ui.Color) {
				ui.Box(c).Background(col.Alpha(0.12)).Radius(10).Padding(2, 8).Children(func() {
					ui.Textf(c, "%s %d", label, n).FontSize(11.5).Bold().TextColor(col).NoWrap()
				})
			}
			total := fmt.Sprintf("%d", len(results))
			if running && planned > 0 {
				total = fmt.Sprintf("%d / %d", len(results), planned)
			}
			ui.Box(c).Background(t.Surface).Radius(10).Padding(2, 8).Children(func() {
				ui.Text(c, "已执行 "+total).FontSize(11.5).Bold().NoWrap()
			})
			pill("通过", counts[runPassed], t.Success)
			pill("失败", counts[runFailed], t.Danger)
			if counts[runError] > 0 {
				pill("错误", counts[runError], t.Danger)
			}
			if counts[runSkipped] > 0 {
				pill("跳过", counts[runSkipped], t.TextMuted)
			}
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconClock).Size(12, 12).TextColor(t.TextMuted)
				ui.Text(c, fmtDuration(float64(elapsed.Milliseconds()))).FontSize(11.5).TextColor(t.TextMuted).NoWrap()
			})
			if note != "" {
				ui.Text(c, note).FontSize(11.5).TextColor(t.Warning).Grow(1).MinWidth(0).MaxLines(1)
			}
			if len(results) > 0 && ui.Button(c.Key("runner-report"), "导出报告…").Padding(3, 8).Clicked() {
				path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "保存运行报告", DefaultPath: "gruno-report.xml"})
				if err == nil && path != "" {
					if werr := os.WriteFile(path, junitReport(rs.title, results), 0o644); werr != nil {
						mygo.Dialog.Error("保存报告失败", werr.Error())
					}
				}
			}
		})
		sectionTabs(c, "runner-filter", &rs.filter, []tabItem{
			{Label: "全部", Count: len(results)},
			{Label: "通过", Count: counts[runPassed]},
			{Label: "失败", Count: counts[runFailed] + counts[runError]},
			{Label: "跳过", Count: counts[runSkipped]},
		})
		ui.Box(c).Height(1).FillWidth().Background(t.Border)

		list := func() { a.runnerList(c, results) }
		if rs.sel < 0 {
			ui.Column(c).Grow(1).MinHeight(0).Children(list)
			return
		}
		ui.SplitPercent(c, &a.runnerSplit, list, func() {
			a.runnerDetail(c, &results[rs.sel])
		}).Grow(1)
	})
}

func (rs *runnerState) shows(o runOutcome) bool {
	switch rs.filter {
	case 1:
		return o == runPassed
	case 2:
		return o == runFailed || o == runError
	case 3:
		return o == runSkipped
	}
	return true
}

// runnerList lists the executed requests, in execution order.
func (a *app) runnerList(c *ui.Context, results []runResult) {
	rs := a.runner
	t := c.Theme()
	ui.Scroll(c).Grow(1).Padding(6, 8).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			for i, r := range results {
				i, r := i, r
				if !rs.shows(r.Outcome) {
					continue
				}
				row := ui.Row(c.Key(fmt.Sprintf("run-row-%d", i))).Gap(8).Padding(6, 8).AlignItems(ui.Center).Radius(5).Children(func() {
					col := outcomeColor(t, r.Outcome)
					ui.Icon(c, outcomeIcon(r.Outcome)).Size(13, 13).TextColor(col)
					ui.Box(c).Background(methodBgColor(t, r.Method)).Radius(3).Padding(1, 0).Width(40).AlignItems(ui.Center).Children(func() {
						ui.Text(c, shortMethod(r.Method)).FontSize(10).Bold().TextColor(methodColor(t, r.Method)).NoWrap()
					})
					ui.Column(c).Gap(1).Grow(1).MinWidth(0).Children(func() {
						name := r.Name
						if r.Iteration > 0 {
							name = fmt.Sprintf("#%d %s", r.Iteration, name)
						}
						ui.Text(c, name).FontSize(12.5).MaxLines(1)
						sub := r.Path
						if r.Err != "" {
							sub = r.Err
						}
						subCol := t.TextMuted
						if r.Err != "" {
							subCol = t.Danger
						}
						ui.Text(c, sub).FontSize(11).TextColor(subCol).MaxLines(1)
					})
					if p, n := r.passedTests(); n > 0 {
						tc := t.Success
						if p < n {
							tc = t.Danger
						}
						ui.Textf(c, "%d/%d", p, n).FontSize(11).Bold().TextColor(tc).NoWrap()
					}
					switch {
					case r.Status > 0:
						ui.Textf(c, "%d", r.Status).FontSize(11.5).Bold().TextColor(statusColor(r.Status)).NoWrap()
					case r.Outcome != runRunning:
						ui.Text(c, outcomeLabel(r.Outcome)).FontSize(11).TextColor(col).NoWrap()
					}
					if r.DurationMs > 0 {
						ui.Text(c, fmtDuration(r.DurationMs)).FontSize(11).TextColor(t.TextMuted).Width(58).NoWrap()
					}
				})
				if rs.sel == i {
					row.Background(t.Accent.Alpha(0.14))
				}
				if row.Clicked() {
					if rs.sel == i {
						rs.sel = -1
					} else {
						rs.sel = i
					}
				}
			}
		})
	})
}

// runnerDetail shows one executed request.
func (a *app) runnerDetail(c *ui.Context, r *runResult) {
	rs := a.runner
	t := c.Theme()
	ui.Column(c).Grow(1).MinWidth(0).Children(func() {
		ui.Row(c).Gap(8).Padding(10, 14).AlignItems(ui.Center).Children(func() {
			ui.Text(c, r.Method).FontSize(11).Bold().TextColor(methodColor(t, r.Method)).NoWrap()
			ui.Text(c, r.URL).FontSize(12).Grow(1).MinWidth(0).MaxLines(1).Selectable()
			if r.Status > 0 {
				ui.Textf(c, "%d %s", r.Status, r.StatusText).FontSize(12).Bold().TextColor(statusColor(r.Status)).NoWrap()
			}
			if r.DurationMs > 0 {
				ui.Text(c, fmtDuration(r.DurationMs)).FontSize(11.5).TextColor(t.TextMuted).NoWrap()
			}
			x := ui.ButtonBase(c.Key("runner-detail-x")).Padding(3, 3).Radius(3).Label("关闭详情")
			x.Children(func() { ui.Icon(c, iconX).Size(12, 12).TextColor(t.TextMuted) })
			if x.Clicked() {
				rs.sel = -1
			}
		})
		p, n := r.passedTests()
		testsTab := tabItem{Label: "测试", Count: n}
		if n > 0 {
			testsTab.Badge = fmt.Sprintf("%d/%d", p, n)
			testsTab.BadgeColor = t.Success
			if p < n {
				testsTab.BadgeColor = t.Danger
			}
		}
		sectionTabs(c, "runner-detail-tabs", &rs.detail, []tabItem{
			testsTab,
			{Label: "响应"},
			{Label: "Headers", Count: len(r.Headers)},
		})
		ui.Box(c).Height(1).FillWidth().Background(t.Border)
		switch rs.detail {
		case 0:
			res := r.Script
			if r.Err != "" && !slices.ContainsFunc(res.Errors, func(e script.ScriptError) bool { return e.Error() == r.Err }) {
				res.Errors = append([]script.ScriptError{{Phase: "请求", Label: "发送", Err: r.Err}}, res.Errors...)
			}
			a.testsPane(c, &res)
		case 1:
			ui.Scroll(c).Grow(1).Padding(10, 14).Children(func() {
				body := r.Body
				switch {
				case r.Outcome == runSkipped:
					body = "请求被跳过，未发送"
				case body == "" && r.Err != "":
					body = r.Err
				case r.BodyCut:
					body += "\n…（响应过长，只保留前 256 KB）"
				}
				ui.Text(c, body).FontSize(12).Selectable()
			})
		case 2:
			ui.Scroll(c).Grow(1).Padding(10, 14).Children(func() {
				ui.Column(c).Gap(4).Selectable().Children(func() {
					for _, h := range r.Headers {
						ui.Row(c).Gap(10).Children(func() {
							ui.Text(c, h.Key).FontSize(12).Bold().Width(180)
							ui.Text(c, h.Value).FontSize(12).Grow(1).MinWidth(0)
						})
					}
				})
			})
		}
	})
}
