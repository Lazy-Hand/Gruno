package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"gruno/bru"
)

func TestVarTokenHelpers(t *testing.T) {
	toks := varTokens("{{host}}/u/{{ id }}?q={{x")
	if len(toks) != 2 || toks[0].Name != "host" || toks[1].Name != "id" || toks[1].Start != 11 || toks[1].End != 19 {
		t.Fatalf("tokens %+v", toks)
	}
	if s, p, ok := openVarPrefix("a {{ho", 6); !ok || s != 2 || p != "ho" {
		t.Fatalf("open prefix %d %q %v", s, p, ok)
	}
	if _, _, ok := openVarPrefix("a {{ho}} b", 10); ok {
		t.Fatal("closed run reported open")
	}
	if _, _, ok := openVarPrefix("a {{ho b", 8); ok {
		t.Fatal("space ends the prefix")
	}
	if s, c := completeVar("x{{ho}}/y", 1, 5, "host"); s != "x{{host}}/y" || c != 9 {
		t.Fatalf("complete %q %d", s, c)
	}
	if s, c := completeVar("中{{h", 1, 4, "host"); s != "中{{host}}" || c != 9 {
		t.Fatalf("complete runes %q %d", s, c)
	}
	if s := replaceToken("{{a}}/{{b}}", varTokens("{{a}}/{{b}}")[1], "c"); s != "{{a}}/{{c}}" {
		t.Fatalf("replace %q", s)
	}
	m := matchVars([]varEntry{{Name: "token"}, {Name: "host"}, {Name: "authHost"}, {Name: "host", Scope: "env"}}, "ho", 8)
	if len(m) != 2 || m[0].Name != "host" || m[0].Scope != "env" || m[1].Name != "authHost" {
		t.Fatalf("match %+v", m)
	}
}

// varInputApp hosts one varInput over value with a host variable defined.
func varInputApp(t *testing.T, value *string) (*app, *ui.Tester) {
	a := openedApp(t)
	a.coll.Dot.Vars = bru.Dict{{Key: "host", Value: "http://h", Enabled: true}, {Key: "hostname", Value: "h", Enabled: true}}
	view := func(c *ui.Context) {
		ui.Column(c).Width(500).Padding(20).Children(func() {
			a.varInput(c, "f", nil, value, varInputOpts{Placeholder: "URL", Label: "URL"})
			ui.Button(c, "外部按钮")
		})
	}
	return a, ui.NewTester(view, 600, 400)
}

func TestVarInputChipsAndCompletion(t *testing.T) {
	v := "{{host}}/users/{{missing}}"
	a, tt := varInputApp(t, &v)

	// At rest: chips, not raw braces.
	if !tt.HasText("host") || !tt.HasText("missing") || tt.HasText(v) {
		t.Fatalf("chips: %q", tt.Texts())
	}
	// A chip's menu swaps the variable.
	if err := tt.Click("host"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("hostname  ·  集合"); err != nil {
		t.Fatalf("menu %v: %q", err, tt.Menu())
	}
	tt.Frame()
	if v != "{{hostname}}/users/{{missing}}" {
		t.Fatalf("after swap %q", v)
	}

	// Clicking the rest of the field edits it, focused.
	if err := tt.Click("/users/"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.varField("f").editing || !tt.Focused("URL") {
		t.Fatalf("not editing: %+v focused=%v", a.varField("f"), tt.Focused("URL"))
	}

	// Typing {{ho opens suggestions; Enter completes.
	tt.Type("?q={{ho")
	tt.Frame()
	if !a.varField("f").suggest || !tt.HasText("hostname") {
		t.Fatalf("no suggestions: %+v %q", a.varField("f"), tt.Texts())
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if v != "{{hostname}}/users/{{missing}}?q={{hostname}}" {
		t.Fatalf("completed %q", v)
	}
	if a.varField("f").suggest {
		t.Fatal("suggestions still open")
	}

	// A suggestion can be clicked too.
	tt.Type("&r={{hostn")
	tt.Frame()
	// A real pointer moves onto the row before pressing it.
	r, ok := tt.Find("hostname")
	if !ok {
		t.Fatalf("no suggestion row: %q", tt.Texts())
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Move(x, y)
	tt.Frame()
	tt.Press(x, y)
	tt.Frame()
	tt.Release(x, y)
	tt.Frame()
	if v != "{{hostname}}/users/{{missing}}?q={{hostname}}&r={{hostname}}" {
		t.Fatalf("clicked %q", v)
	}

	// Leaving the field shows chips again.
	if err := tt.Click("外部按钮"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.varField("f").editing || !tt.HasText("missing") || tt.HasText(v) {
		t.Fatalf("not back to chips: %+v %q", a.varField("f"), tt.Texts())
	}
}

func TestBodyEditorCompletesVariables(t *testing.T) {
	a := openedApp(t)
	a.coll.Dot.Vars = bru.Dict{{Key: "userId", Value: "7", Enabled: true}}
	br, _ := a.coll.AddRequest("", "R")
	br.Req.Body = "text"
	a.openTab(br)
	ts := a.tabState(br)
	ts.section = 1

	tt := ui.NewTester(a.view, 1200, 800)
	r, ok := tt.Find("TEXT")
	if !ok {
		t.Fatalf("body header: %q", tt.Texts())
	}
	tt.ClickAt(r.X+40, r.Y+r.H+30) // into the editor below its header
	tt.Frame()
	tt.Type("id={{us")
	tt.Frame()
	if !tt.HasText("userId") {
		t.Fatalf("no completion: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := ts.bodyBuf.String(); got != "id={{userId}}" {
		t.Fatalf("body %q", got)
	}
}
