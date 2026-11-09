package ui

// Network settings at three levels, as in Bruno:
//   - app preferences (gruno.json): TLS verification, extra CA, default timeout;
//   - collection (bruno.json): proxy and client certificates;
//   - request (.bru settings block): timeout, redirects.

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"gruno/bru"
	"gruno/internal/send"
)

// netPrefs are the app-wide network preferences.
type netPrefs struct {
	InsecureTLS bool   `json:"insecureTls,omitempty"`
	CACertFile  string `json:"caCertFile,omitempty"`
	TimeoutMs   int    `json:"timeoutMs"` // 0: no timeout
	NoCookies   bool   `json:"noCookies,omitempty"`
}

var defaultNetPrefs = netPrefs{TimeoutMs: 30000}

// reqSetting reads an integer or boolean from a request's settings block.
func reqSetting(r *bru.Request, key string) (string, bool) {
	v, ok := r.Settings.Get(key)
	return strings.TrimSpace(v), ok
}

// sendSettings combines the three levels for one send. A request
// timeout above 0 overrides the app's; followRedirects: false stops at
// the first response; maxRedirects caps them.
func (a *app) sendSettings(br *BruRequest) send.Settings {
	st := send.Settings{
		InsecureTLS: a.net.InsecureTLS,
		CACertFile:  a.net.CACertFile,
		Timeout:     time.Duration(a.net.TimeoutMs) * time.Millisecond,
		NoCookies:   a.net.NoCookies,
	}
	if a.coll != nil {
		st.Proxy = a.coll.Proxy
		st.ClientCerts = a.coll.absCerts()
	}
	if br == nil {
		return st
	}
	if v, ok := reqSetting(br.Req, "timeout"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			st.Timeout = time.Duration(n) * time.Millisecond
		}
	}
	if v, ok := reqSetting(br.Req, "followRedirects"); ok && v == "false" {
		st.NoRedirects = true
	}
	if v, ok := reqSetting(br.Req, "maxRedirects"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			st.MaxRedirects = n
		}
	}
	return st
}

// requestSettingsSection edits the request's settings block.
func (a *app) requestSettingsSection(c *ui.Context, br *BruRequest) {
	t := c.Theme()
	r := br.Req
	ui.Column(c).Gap(10).Padding(4, 0).Children(func() {
		ui.Text(c, "请求设置").Bold().FontSize(12)

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "超时 (ms)").Width(authLabelWidth).FontSize(12)
			v, _ := reqSetting(r, "timeout")
			if ui.TextInput(c.Key(br.Path+"-set-timeout"), &v).Width(120).Placeholder("0").Changed() {
				dictSet(&r.Settings, "timeout", strings.TrimSpace(v))
				a.save(br)
			}
			ui.Textf(c, "0 或留空使用偏好设置（当前 %s）", timeoutLabel(a.net.TimeoutMs)).FontSize(11).TextColor(t.TextMuted)
		})

		follow := true
		if v, ok := reqSetting(r, "followRedirects"); ok && v == "false" {
			follow = false
		}
		if ui.Checkbox(c.Key(br.Path+"-set-follow"), &follow, "跟随重定向").Changed() {
			dictSet(&r.Settings, "followRedirects", strconv.FormatBool(follow))
			a.save(br)
		}
		if follow {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "最多重定向次数").Width(authLabelWidth).FontSize(12)
				v, _ := reqSetting(r, "maxRedirects")
				if ui.TextInput(c.Key(br.Path+"-set-maxredir"), &v).Width(120).Placeholder("10").Changed() {
					dictSet(&r.Settings, "maxRedirects", strings.TrimSpace(v))
					a.save(br)
				}
			})
		}
		ui.Text(c, "代理和客户端证书在「集合设置」里配置；TLS 校验和默认超时在「偏好设置」里。").
			FontSize(11).TextColor(t.TextMuted).Padding(6, 0, 0, 0)
	})
}

func timeoutLabel(ms int) string {
	if ms <= 0 {
		return "不限"
	}
	return strconv.Itoa(ms) + " ms"
}

// ---- collection: proxy and certificates ----

var proxyModes = []string{"跟随系统", "不使用代理", "自定义"}

func proxyModeLabel(m send.ProxyMode) string {
	switch m {
	case send.ProxyNone:
		return proxyModes[1]
	case send.ProxyCustom:
		return proxyModes[2]
	}
	return proxyModes[0]
}

func (a *app) collectionProxyTab(c *ui.Context) {
	t := c.Theme()
	p := &a.coll.Proxy
	save := func() {
		if err := a.coll.SaveNetworkSettings(); err != nil {
			mygo.Dialog.Error("保存 bruno.json 失败", err.Error())
		}
	}
	field := func(label, key string, v *string, secret bool) {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).Width(authLabelWidth).FontSize(12)
			if a.varInput(c, "coll-proxy-"+key, nil, v, varInputOpts{Placeholder: label, Label: label, Password: secret}) {
				save()
			}
		})
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "代理").Width(authLabelWidth).FontSize(12)
			mode := proxyModeLabel(p.Mode)
			if ui.Select(c.Key("coll-proxy-mode"), &mode, proxyModes).Changed() {
				switch mode {
				case proxyModes[1]:
					p.Mode = send.ProxyNone
				case proxyModes[2]:
					p.Mode = send.ProxyCustom
				default:
					p.Mode = send.ProxySystem
				}
				save()
			}
		})
		if p.Mode != send.ProxyCustom {
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "协议").Width(authLabelWidth).FontSize(12)
			proto := orDefaultS(p.Protocol, "http")
			if ui.Select(c.Key("coll-proxy-proto"), &proto, []string{"http", "https", "socks5"}).Changed() {
				p.Protocol = proto
				save()
			}
		})
		field("主机", "host", &p.Hostname, false)
		field("端口", "port", &p.Port, false)
		field("用户名", "user", &p.Username, false)
		field("密码", "pass", &p.Password, true)
		field("不走代理的主机", "bypass", &p.Bypass, false)
		ui.Text(c, "逗号分隔，支持 *.example.com；字段里可以用 {{变量}}").FontSize(11).TextColor(t.TextMuted)
	})
}

func (a *app) collectionCertsTab(c *ui.Context) {
	t := c.Theme()
	save := func() {
		if err := a.coll.SaveNetworkSettings(); err != nil {
			mygo.Dialog.Error("保存 bruno.json 失败", err.Error())
		}
	}
	pick := func(v *string, title string) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, DefaultPath: a.coll.Root})
		if err == nil && len(paths) > 0 {
			*v = paths[0]
			save()
		}
	}
	fileRow := func(i int, label, key string, v *string) {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).Width(90).FontSize(12)
			if ui.TextInput(c.Key(key), v).Grow(1).Placeholder("文件路径（可相对集合目录）").Changed() {
				save()
			}
			if ui.Button(c.Key(key+"-pick"), "选择…").Padding(3, 8).Clicked() {
				pick(v, "选择"+label)
			}
		})
	}
	ui.Column(c).Gap(10).Children(func() {
		if ui.Checkbox(c.Key("coll-certs-on"), &a.coll.ClientCertsOn, "启用客户端证书").Changed() {
			save()
		}
		ui.Text(c, "按域名匹配，例如 api.example.com、*.example.com 或 host:8443").FontSize(11).TextColor(t.TextMuted)
		for i := range a.coll.ClientCerts {
			i := i
			cc := &a.coll.ClientCerts[i]
			key := "coll-cert-" + strconv.Itoa(i)
			ui.Column(c).Gap(6).Padding(8, 10).Radius(6).BorderWidth(1).BorderColor(t.Border).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "域名").Width(90).FontSize(12)
					if ui.TextInput(c.Key(key+"-domain"), &cc.Domain).Grow(1).Placeholder("*.example.com").Changed() {
						save()
					}
					kind := "PEM"
					if cc.PFXFile != "" || a.certPFX[i] {
						kind = "PFX"
					}
					if ui.Select(c.Key(key+"-kind"), &kind, []string{"PEM", "PFX"}).Changed() {
						if a.certPFX == nil {
							a.certPFX = map[int]bool{}
						}
						a.certPFX[i] = kind == "PFX"
						if kind == "PFX" {
							cc.CertFile, cc.KeyFile = "", ""
						} else {
							cc.PFXFile = ""
						}
						save()
					}
					del := ui.Button(c.Key(key+"-del"), "").Padding(4, 6).Label("删除证书")
					del.Children(func() { ui.Icon(c, iconTrash).Size(13, 13) })
					if del.Clicked() {
						a.coll.ClientCerts = append(a.coll.ClientCerts[:i], a.coll.ClientCerts[i+1:]...)
						a.certPFX = nil
						save()
					}
				})
				if cc.PFXFile != "" || a.certPFX[i] {
					fileRow(i, "PFX 文件", key+"-pfx", &cc.PFXFile)
				} else {
					fileRow(i, "证书", key+"-cert", &cc.CertFile)
					fileRow(i, "私钥", key+"-key", &cc.KeyFile)
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "密码").Width(90).FontSize(12)
					if ui.TextInput(c.Key(key+"-pass"), &cc.Passphrase).Grow(1).Password().Placeholder("可选").Changed() {
						save()
					}
				})
			})
		}
		add := ui.Button(c.Key("coll-cert-add"), "").Gap(4)
		add.Children(func() {
			ui.Icon(c, iconPlus).Size(13, 13)
			ui.Text(c, "添加证书").FontSize(12)
		})
		if add.Clicked() {
			a.coll.ClientCerts = append(a.coll.ClientCerts, send.ClientCert{})
			a.coll.ClientCertsOn = true
		}
	})
}

// ---- preferences ----

func (a *app) prefsModal(c *ui.Context) {
	ui.Modal(c, &a.prefsOpen, func() {
		t := c.Theme()
		ui.Column(c).Width(480).Gap(10).Children(func() {
			ui.Text(c, "偏好设置").Bold().FontSize(15)
			ui.Text(c, "网络").Bold().FontSize(12).Padding(4, 0, 0, 0)

			verify := !a.prefsDraft.InsecureTLS
			if ui.Checkbox(c.Key("prefs-verify"), &verify, "校验 TLS/SSL 证书").Changed() {
				a.prefsDraft.InsecureTLS = !verify
			}
			if a.prefsDraft.InsecureTLS {
				ui.Text(c, "已关闭证书校验：自签名或过期的证书也会被接受，仅建议在本地调试时使用").FontSize(11).TextColor(t.Warning)
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "额外 CA 证书").Width(100).FontSize(12)
				ui.TextInput(c.Key("prefs-ca"), &a.prefsDraft.CACertFile).Grow(1).Placeholder("PEM 文件，加入系统信任列表")
				if ui.Button(c.Key("prefs-ca-pick"), "选择…").Padding(3, 8).Clicked() {
					if paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择 CA 证书"}); err == nil && len(paths) > 0 {
						a.prefsDraft.CACertFile = paths[0]
					}
				}
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "默认超时 (ms)").Width(100).FontSize(12)
				ui.TextInput(c.Key("prefs-timeout"), &a.prefsTimeoutIn).Width(120).Placeholder("0")
				ui.Text(c, "0 表示不限").FontSize(11).TextColor(t.TextMuted)
			})
			ui.Text(c, "Cookie").Bold().FontSize(12).Padding(4, 0, 0, 0)
			keep := !a.prefsDraft.NoCookies
			if ui.Checkbox(c.Key("prefs-cookies"), &keep, "自动保存响应中的 Cookie，并在后续请求中发送").Changed() {
				a.prefsDraft.NoCookies = !keep
			}
			ui.Row(c).Children(func() {
				if ui.Button(c.Key("prefs-cookies-manage"), "管理 Cookie…").Clicked() {
					a.prefsOpen = false
					a.cookiesOpen = true
				}
			})
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.prefsOpen = false
				}
				if ui.PrimaryButton(c, "保存").Clicked() {
					n, err := strconv.Atoi(strings.TrimSpace(a.prefsTimeoutIn))
					if err != nil || n < 0 {
						n = 0
					}
					a.prefsDraft.TimeoutMs = n
					a.prefsDraft.CACertFile = strings.TrimSpace(a.prefsDraft.CACertFile)
					a.net = a.prefsDraft
					dir := ""
					if a.coll != nil {
						dir = a.coll.Root
					}
					a.saveLastDir(dir)
					a.prefsOpen = false
				}
			})
		})
	})
}

func (a *app) openPrefs() {
	a.prefsDraft = a.net
	a.prefsTimeoutIn = strconv.Itoa(a.net.TimeoutMs)
	a.prefsOpen = true
}

// cookiesModal lists the jar's cookies by host, to delete one or all.
func (a *app) cookiesModal(c *ui.Context) {
	ui.Modal(c, &a.cookiesOpen, func() {
		t := c.Theme()
		jar := a.svc.Cookies
		list := jar.List()
		ui.Column(c).Width(560).Gap(10).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Cookie").Bold().FontSize(15).Grow(1)
				if len(list) > 0 && ui.Button(c.Key("cookies-clear"), "全部清除").Clicked() {
					jar.Clear()
				}
			})
			if a.net.NoCookies {
				ui.Text(c, "偏好设置中已关闭自动保存和发送 Cookie").FontSize(11).TextColor(t.Warning)
			}
			if len(list) == 0 {
				ui.Text(c, "还没有保存的 Cookie。响应中的 Set-Cookie 会自动保存在这里，直到退出 Gruno。").
					FontSize(12).TextColor(t.TextMuted).Padding(12, 0)
			}
			ui.Scroll(c).MaxHeight(360).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					host := ""
					for i, ck := range list {
						ck := ck
						if ck.Host != host {
							host = ck.Host
							ui.Text(c, host).Bold().FontSize(12).Padding(8, 0, 2, 0)
						}
						ui.Row(c).Gap(8).Padding(3, 6).AlignItems(ui.Center).Radius(4).Background(t.Surface.Alpha(0.5)).Children(func() {
							ui.Text(c, ck.Name).FontSize(12).Bold().Width(160).MaxLines(1)
							ui.Text(c, ck.Value).FontSize(12).Grow(1).MinWidth(0).MaxLines(1).Selectable()
							if iconButton(c.Key("cookie-del-"+itoa(i)), iconTrash, "删除").Clicked() {
								jar.Delete(ck.Host, ck.Name)
							}
						})
					}
				})
			})
			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, "关闭").Clicked() {
					a.cookiesOpen = false
				}
			})
		})
	})
}
