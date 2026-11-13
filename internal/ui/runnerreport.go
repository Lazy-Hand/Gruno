package ui

// JUnit XML report of a collection run, for CI and test dashboards.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

func xmlText(b *bytes.Buffer, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

// junitReport renders the run's tests, asserts and request outcomes as
// one JUnit testsuite.
func junitReport(title string, results []runResult) []byte {
	var b bytes.Buffer
	failures := 0
	type tc struct {
		class, name, message string
		secs                 float64
		skipped              bool
	}
	var cases []tc
	for _, r := range results {
		class := r.Name
		if r.Iteration > 0 {
			class = fmt.Sprintf("#%d %s", r.Iteration, r.Name)
		}
		if len(r.Script.Tests)+len(r.Script.Asserts) == 0 {
			c := tc{class: class, name: "request", secs: r.DurationMs / 1000}
			switch r.Outcome {
			case runFailed:
				c.message = "测试或脚本失败"
			case runError:
				c.message = r.Err
			case runSkipped:
				c.skipped = true
			}
			cases = append(cases, c)
			continue
		}
		for _, t := range r.Script.Tests {
			cases = append(cases, tc{class: class, name: t.Name, message: t.Error, secs: r.DurationMs / 1000})
		}
		for _, a := range r.Script.Asserts {
			cases = append(cases, tc{class: class, name: a.Name, message: a.Error, secs: r.DurationMs / 1000})
		}
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString("<testsuites>\n")
	b.WriteString(`  <testsuite name="`)
	xmlText(&b, title)
	b.WriteString(`" tests="` + fmt.Sprint(len(cases)) + `"`)
	for _, c := range cases {
		if c.message != "" && !c.skipped {
			failures++
		}
	}
	b.WriteString(` failures="` + fmt.Sprint(failures) + `" errors="0">` + "\n")
	for _, c := range cases {
		b.WriteString(`    <testcase classname="`)
		xmlText(&b, c.class)
		b.WriteString(`" name="`)
		xmlText(&b, c.name)
		b.WriteString(`" time="` + fmt.Sprintf("%.3f", c.secs) + `"`)
		switch {
		case c.skipped:
			b.WriteString(">\n      <skipped/>\n    </testcase>\n")
		case c.message != "":
			b.WriteString(">\n      <failure message=\"")
			xmlText(&b, oneLine(c.message))
			b.WriteString("\"/>\n    </testcase>\n")
		default:
			b.WriteString("/>\n")
		}
	}
	b.WriteString("  </testsuite>\n</testsuites>\n")
	return b.Bytes()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
