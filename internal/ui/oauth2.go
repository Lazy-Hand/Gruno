package ui

// OAuth2 auth editor for requests and collection.bru.

import (
	"github.com/egoist/mygo/ui"

	"gruno/bru"
)

// authLabelWidth fits the longest auth field name (access_token_url).
const authLabelWidth = 130

var oauth2Grants = []string{"client_credentials", "password", "authorization_code"}

// oauth2Fields edits an auth:oauth2 block. Only the fields the grant
// uses are shown; Bruno's defaults fill missing keys.
func (a *app) oauth2Fields(c *ui.Context, br *BruRequest, keyPrefix string, d *bru.Dict, onSave func()) {
	t := c.Theme()
	sel := func(key, def string, options []string) {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, key).Width(authLabelWidth).FontSize(12)
			v := orDefaultS(dictValue(*d, key), def)
			if ui.Select(c.Key(keyPrefix+"-oauth2-"+key), &v, options).Changed() {
				dictSet(d, key, v)
				onSave()
			}
		})
	}
	flag := func(key, label string) {
		v, ok := d.Get(key)
		on := !ok || v != "false"
		if ui.Checkbox(c.Key(keyPrefix+"-oauth2-"+key), &on, label).Changed() {
			if on {
				dictSet(d, key, "true")
			} else {
				dictSet(d, key, "false")
			}
			onSave()
		}
	}
	ui.Column(c).Gap(8).Padding(8, 0).Children(func() {
		sel("grant_type", "client_credentials", oauth2Grants)
		grant := orDefaultS(dictValue(*d, "grant_type"), "client_credentials")
		if grant == "authorization_code" {
			ui.Text(c, "authorization_code 需要浏览器登录回调，暂不支持；请改用 client_credentials 或 password").
				FontSize(12).TextColor(t.Warning)
		}
		a.authFieldRow(c, br, keyPrefix, d, "access_token_url", false, onSave)
		a.authFieldRow(c, br, keyPrefix, d, "client_id", false, onSave)
		a.authFieldRow(c, br, keyPrefix, d, "client_secret", true, onSave)
		if grant == "password" {
			a.authFieldRow(c, br, keyPrefix, d, "username", false, onSave)
			a.authFieldRow(c, br, keyPrefix, d, "password", true, onSave)
		}
		a.authFieldRow(c, br, keyPrefix, d, "scope", false, onSave)

		ui.Text(c, "高级").Bold().FontSize(12).Padding(6, 0, 0, 0)
		sel("credentials_placement", "body", []string{"body", "basic_auth_header"})
		sel("token_placement", "header", []string{"header", "url"})
		if orDefaultS(dictValue(*d, "token_placement"), "header") == "url" {
			a.authFieldRow(c, br, keyPrefix, d, "token_query_key", false, onSave)
		} else {
			if _, ok := d.Get("token_header_prefix"); !ok {
				dictSet(d, "token_header_prefix", "Bearer")
			}
			a.authFieldRow(c, br, keyPrefix, d, "token_header_prefix", false, onSave)
		}
		a.authFieldRow(c, br, keyPrefix, d, "refresh_token_url", false, onSave)
		ui.Row(c).Gap(16).Children(func() {
			flag("auto_fetch_token", "自动获取 token")
			flag("auto_refresh_token", "过期自动刷新")
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(4, 0, 0, 0).Children(func() {
			if ui.Button(c.Key(keyPrefix+"-oauth2-clear"), "清除缓存的 token").Clicked() {
				a.svc.ClearOAuth2Tokens()
				a.oauth2Cleared = true
			}
			if a.oauth2Cleared {
				ui.Text(c, "已清除，下次发送将重新获取").FontSize(11).TextColor(t.TextMuted)
			}
		})
	})
}
