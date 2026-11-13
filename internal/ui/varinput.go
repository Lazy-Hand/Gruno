package ui

// varInput is a text input that treats {{variables}} as first-class:
//
//   - At rest, each {{name}} shows as a chip — accent when the name
//     resolves, red when it does not — whose tooltip is the value and
//     whose menu replaces it with another variable. Clicking the field
//     anywhere else starts editing.
//   - While editing, {{name}} runs are tinted, and typing "{{" opens a
//     list of variables under the field, filtered by what follows; ↑/↓
//     move, Enter or Tab inserts {{name}}, Esc closes.
//
// The input itself cannot draw chips inside its text, hence the two
// states; a field without variables is a plain input throughout.

import (
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"gruno/bru"
	"gruno/internal/send"
)

// varEntry is a variable a field can refer to, from the scopes a send
// resolves against.
type varEntry struct {
	Name, Value, Scope string
	Secret             bool
}

// varEntries lists the variables visible to br, highest precedence
// last, as the resolver reads them. The result is cached for the build
// pass: a table of inputs asks once, not once per row.
func (a *app) varEntries(br *BruRequest) []varEntry {
	key := ""
	if br != nil {
		key = br.Path
	}
	if v, ok := a.varPass[key]; ok {
		return v
	}
	v := a.collectVarEntries(br)
	if a.varPass == nil {
		a.varPass = map[string][]varEntry{}
	}
	a.varPass[key] = v
	return v
}

func (a *app) collectVarEntries(br *BruRequest) []varEntry {
	var out []varEntry
	add := func(scope string, d bru.Dict) {
		for _, e := range d {
			if e.Enabled && e.Key != "" {
				out = append(out, varEntry{Name: e.Key, Value: e.Value, Scope: scope})
			}
		}
	}
	if a.coll == nil {
		return nil
	}
	if a.coll.Dot != nil {
		add("集合", a.coll.Dot.Vars)
	}
	for _, f := range a.coll.folderChain(br) {
		add("目录", f.Bru.Vars)
	}
	if env := a.coll.activeEnvironment(); env != nil {
		add("环境", env.Vars)
		for _, p := range a.secretPairs(env) {
			if p.Enabled {
				out = append(out, varEntry{Name: p.Key, Value: p.Value, Scope: "环境", Secret: true})
			}
		}
	}
	if br != nil {
		add("请求", br.Req.VarsPreRequest)
	}
	add("运行时", a.runtimeDict())
	for _, name := range send.DynamicNames {
		if val, ok := send.DynamicVar(name); ok {
			out = append(out, varEntry{Name: name, Value: val, Scope: "动态"})
		}
	}
	return out
}

// lookupVar finds the entry that wins for name. process.env names are
// defined only when the variable exists; an unknown one shows red.
func (a *app) lookupVar(vars []varEntry, name string) (varEntry, bool) {
	for i := len(vars) - 1; i >= 0; i-- {
		if vars[i].Name == name {
			return vars[i], true
		}
	}
	if key, ok := strings.CutPrefix(name, "process.env."); ok && a.coll != nil {
		if val, found := a.coll.loadDotEnv()[key]; found {
			return varEntry{Name: name, Value: val, Scope: "process.env"}, true
		}
	}
	return varEntry{}, false
}

// uniqueVars keeps the winning entry per name, in first-seen order.
func uniqueVars(vars []varEntry) []varEntry {
	idx := map[string]int{}
	var out []varEntry
	for _, v := range vars {
		if i, ok := idx[v.Name]; ok {
			out[i] = v
			continue
		}
		idx[v.Name] = len(out)
		out = append(out, v)
	}
	return out
}

// varToken is a {{name}} run, in rune offsets of the text.
type varToken struct {
	Start, End int // [Start, End) covers the braces
	Name       string
}

// varTokens finds the complete {{name}} runs in s.
func varTokens(s string) []varToken {
	rs := []rune(s)
	var out []varToken
	for i := 0; i+1 < len(rs); i++ {
		if rs[i] != '{' || rs[i+1] != '{' {
			continue
		}
		for j := i + 2; j+1 < len(rs); j++ {
			if rs[j] == '}' && rs[j+1] == '}' {
				out = append(out, varToken{Start: i, End: j + 2, Name: strings.TrimSpace(string(rs[i+2 : j]))})
				i = j + 1
				break
			}
			if rs[j] == '{' || rs[j] == '\n' {
				break
			}
		}
	}
	return out
}

// openVarPrefix reports the "{{prefix" being typed just before caret:
// the rune offset of its "{{" and the prefix, when the run is still open.
func openVarPrefix(s string, caret int) (int, string, bool) {
	rs := []rune(s)
	if caret > len(rs) {
		caret = len(rs)
	}
	for i := caret - 1; i >= 1; i-- {
		r := rs[i]
		if r == '{' && rs[i-1] == '{' {
			return i - 1, string(rs[i+1 : caret]), true
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '$') {
			return 0, "", false
		}
	}
	return 0, "", false
}

// completeVar replaces the open "{{prefix" before caret with {{name}},
// swallowing a "}}" already after the caret, and returns the new text
// and caret.
func completeVar(s string, start, caret int, name string) (string, int) {
	rs := []rune(s)
	tail := rs[caret:]
	if len(tail) >= 2 && tail[0] == '}' && tail[1] == '}' {
		tail = tail[2:]
	}
	tok := []rune("{{" + name + "}}")
	out := append(append(append([]rune{}, rs[:start]...), tok...), tail...)
	return string(out), start + len(tok)
}

// replaceToken swaps the variable of tok for name.
func replaceToken(s string, tok varToken, name string) string {
	rs := []rune(s)
	return string(rs[:tok.Start]) + "{{" + name + "}}" + string(rs[tok.End:])
}

// matchVars filters vars by prefix (case-insensitive substring, prefix
// matches first), at most max.
func matchVars(vars []varEntry, prefix string, max int) []varEntry {
	p := strings.ToLower(prefix)
	var first, rest []varEntry
	for _, v := range uniqueVars(vars) {
		n := strings.ToLower(v.Name)
		switch {
		case strings.HasPrefix(n, p):
			first = append(first, v)
		case strings.Contains(n, p):
			rest = append(rest, v)
		}
	}
	out := append(first, rest...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// varFieldState is per-field transient state, keyed by the field key.
type varFieldState struct {
	editing  bool // showing the input rather than the chips
	focused  bool // the input has had the focus since editing began
	caret    int  // caret to restore next frame, -1 for none
	last     int  // caret as of the last frame, for keys and clicks
	suggest  bool // the suggestion list is open
	start    int  // rune offset of the "{{" being completed
	prefix   string
	selected int
	// overList is the pointer over the suggestion list as of the last
	// frame: a press there takes the focus from the input, which must
	// not close the list before the click lands.
	overList bool
	// pending is text a key handler chose, applied in the next build:
	// key handlers run after the build that returned changed, so the
	// change must surface in a build for the caller to sync and save.
	pending *string
}

func (a *app) varField(key string) *varFieldState {
	if a.varFields == nil {
		a.varFields = map[string]*varFieldState{}
	}
	st, ok := a.varFields[key]
	if !ok {
		st = &varFieldState{caret: -1}
		a.varFields[key] = st
	}
	return st
}

// varInputOpts tune one varInput.
type varInputOpts struct {
	Placeholder string
	Label       string
	Password    bool // masked: never shows chips
	Width       float32
	// OnKey sees keys the suggestion list does not take, as OnTextKey.
	OnKey func(k ui.TextKey) bool
}

// varInput renders the field for *v and reports whether the user
// changed it this frame (typing, completing or replacing a variable).
func (a *app) varInput(c *ui.Context, key string, br *BruRequest, v *string, o varInputOpts) bool {
	t := c.Theme()
	st := a.varField(key)
	vars := a.varEntries(br)
	changed := false
	if st.pending != nil {
		*v, st.pending = *st.pending, nil
		changed = true
	}
	tokens := varTokens(*v)

	sizeOf := func(e ui.Element) ui.Element {
		if o.Width > 0 {
			return e.Width(o.Width)
		}
		return e.Grow(1).MinWidth(0)
	}

	// At rest with variables: chips.
	if !st.editing && !o.Password && len(tokens) > 0 {
		field := sizeOf(ui.Row(c.Key(key+"-chips"))).Gap(2).AlignItems(ui.Center).
			Padding(4, 8).MinHeight(30).Radius(6).BorderWidth(1).BorderColor(t.Border).
			Background(t.Surface).Clip()
		field.Children(func() {
			rs := []rune(*v)
			at := 0
			for i, tok := range tokens {
				if tok.Start > at {
					ui.Text(c, string(rs[at:tok.Start])).FontSize(13).NoWrap()
				}
				a.varChip(c, key, i, br, vars, tok, v, &changed)
				at = tok.End
			}
			if at < len(rs) {
				ui.Text(c, string(rs[at:])).FontSize(13).NoWrap()
			}
			ui.Box(c).Grow(1)
		})
		if o.Label != "" {
			field.Label(o.Label)
		}
		if field.Clicked() {
			st.editing, st.focused, st.caret = true, false, len([]rune(*v))
		}
		return changed
	}

	// Editing (or a field without variables): the input.
	inp := sizeOf(ui.TextInput(c.Key(key), v)).Placeholder(o.Placeholder)
	if o.Label != "" {
		inp.Label(o.Label)
	}
	if o.Password {
		inp.Password()
	}
	if st.editing && !st.focused {
		inp.Focus()
	}
	if st.caret >= 0 {
		inp.SetTextSelection(st.caret, st.caret)
		st.caret = -1
	}
	if !o.Password {
		for _, tok := range tokens {
			col := t.Accent
			if _, ok := a.lookupVar(vars, tok.Name); !ok {
				col = t.Danger
			}
			inp.TextRanges(ui.TextRange{Start: tok.Start, End: tok.End, Color: col, Weight: 600})
		}
	}

	matches := []varEntry(nil)
	if st.suggest {
		matches = matchVars(vars, st.prefix, 8)
	}
	inp.OnTextKey(func(k ui.TextKey) bool {
		if st.suggest && len(matches) > 0 {
			switch k.Key {
			case ui.KeyDown:
				st.selected = (st.selected + 1) % len(matches)
				return true
			case ui.KeyUp:
				st.selected = (st.selected + len(matches) - 1) % len(matches)
				return true
			case ui.KeyEnter, ui.KeyTab:
				text, pos := completeVar(k.Text(), st.start, st.last, matches[st.selected].Name)
				st.pending = &text
				st.caret, st.suggest = pos, false
				st.editing, st.focused = true, false // stay in the input, focused
				return true
			case ui.KeyEscape:
				st.suggest = false
				return true
			}
		}
		if o.OnKey != nil {
			return o.OnKey(k)
		}
		return false
	})
	if inp.Changed() {
		changed = true
	}

	focused := inp.Focused()
	if focused {
		st.focused = true
		_, caret := inp.TextSelection()
		st.last = caret
		if start, prefix, ok := openVarPrefix(*v, caret); ok {
			if !st.suggest || st.prefix != prefix || st.start != start {
				st.selected = 0
			}
			st.suggest, st.start, st.prefix = true, start, prefix
		} else {
			st.suggest = false
		}
	} else if st.suggest && st.overList {
		// Clicking a suggestion: keep the list until the click lands.
	} else if st.focused || !st.editing {
		// Focus went elsewhere: back to chips.
		st.editing, st.focused, st.suggest = false, false, false
	}

	if st.suggest {
		matches = matchVars(vars, st.prefix, 8)
		open := len(matches) > 0
		over := false
		ui.Popover(c.Key(key+"-suggest"), inp, &open, func() {
			ui.Column(c).Gap(0).Width(320).Padding(4, 0).Children(func() {
				for i, m := range matches {
					i, m := i, m
					row := ui.Row(c.Key(key+"-sug-"+m.Name)).Gap(8).Padding(5, 10).Radius(4).AlignItems(ui.Center)
					if i == st.selected {
						row.Background(t.Accent.Alpha(0.14))
					}
					row.Children(func() {
						ui.Text(c, m.Name).FontSize(12.5).Bold().NoWrap()
						val := m.Value
						if m.Secret {
							val = "••••••"
						}
						ui.Text(c, val).FontSize(11.5).TextColor(t.TextMuted).Grow(1).MinWidth(0).MaxLines(1)
						ui.Text(c, m.Scope).FontSize(10.5).TextColor(t.TextMuted).NoWrap()
					})
					if row.Hovered() || row.Pressed() {
						over = true
					}
					if row.Clicked() {
						text, pos := completeVar(*v, st.start, st.last, m.Name)
						*v = text
						st.caret, st.suggest, st.editing = pos, false, true
						st.focused = false
						changed = true
					}
				}
			})
		})
		st.overList = over
		if !open {
			st.suggest, st.overList = false, false
		}
	}
	return changed
}

// varChip is one {{name}} at rest: its tooltip shows the value, its
// menu swaps the variable or starts editing.
func (a *app) varChip(c *ui.Context, key string, i int, br *BruRequest, vars []varEntry, tok varToken, v *string, changed *bool) {
	t := c.Theme()
	entry, ok := a.lookupVar(vars, tok.Name)
	col := t.Accent
	tip := "未定义的变量"
	if ok {
		tip = entry.Scope + " · " + entry.Value
		if entry.Secret {
			tip = entry.Scope + " · ••••••（Secret）"
		}
		if entry.Value == "" && !entry.Secret {
			tip = entry.Scope + " · （空值）"
		}
	} else {
		col = t.Danger
	}
	chip := ui.Button(c.Key(key+"-chip-"+itoa(i)), "").Padding(1, 6).Gap(0).Radius(4).
		Background(col.Alpha(0.14)).BorderWidth(0).Tooltip(tip).Label("变量 " + tok.Name)
	chip.Children(func() {
		ui.Text(c, tok.Name).FontSize(12.5).Bold().TextColor(col).NoWrap()
	})
	chip.Menu(func(m *ui.Menu) {
		m.Item(tip).Disabled(true)
		m.Separator()
		list := uniqueVars(vars)
		if len(list) == 0 {
			m.Item("没有可用的变量").Disabled(true)
		}
		for _, e := range list {
			e := e
			label := e.Name + "  ·  " + e.Scope
			if e.Name == tok.Name {
				label = "✓ " + label
			}
			if m.Item(label).Chosen() && e.Name != tok.Name {
				*v = replaceToken(*v, tok, e.Name)
				*changed = true
			}
		}
		m.Separator()
		if m.Item("编辑文本").Chosen() {
			st := a.varField(key)
			st.editing, st.focused, st.caret = true, false, tok.End
		}
	})
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// attachVarCompletion offers variables in a code editor whose text is
// interpolated (request bodies): typing after "{{" lists them, and
// choosing one completes {{name}}, closing the braces unless they are.
func (a *app) attachVarCompletion(st *editor.State, br *BruRequest) {
	st.OnCompletion = func(req editor.CompletionRequest) {
		start := req.Offset - len(req.Prefix)
		n := st.Buffer.Len()
		if start < 2 || req.Offset > n || st.Buffer.Slice(start-2, start) != "{{" {
			return
		}
		closed := req.Offset+2 <= n && st.Buffer.Slice(req.Offset, req.Offset+2) == "}}"
		var items []editor.Completion
		// Completions run outside the build pass: read the variables
		// fresh, never from the pass cache.
		for _, v := range uniqueVars(a.collectVarEntries(br)) {
			insert := v.Name
			if !closed {
				insert += "}}"
			}
			detail := v.Scope + " · " + v.Value
			if v.Secret {
				detail = v.Scope + " · ••••••"
			}
			items = append(items, editor.Completion{Label: v.Name, Detail: detail, Kind: editor.CompletionVariable, Insert: insert})
		}
		st.ShowCompletions(start, items)
	}
}
