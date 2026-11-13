package ui

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gruno/bru"
	"gruno/internal/script"
)

func TestRunnerIteratesDataRows(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Row"))
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	a := openedApp(t)
	br, _ := a.coll.AddRequest("", "R")
	br.Req.URL = srv.URL
	br.Req.Headers = bru.Dict{{Key: "X-Row", Value: "{{row}}", Enabled: true}}
	a.openTab(br)
	a.runner = &runnerState{folder: "", delayIn: "0", sel: -1,
		dataRows: []map[string]string{{"row": "a"}, {"row": "b"}}}
	a.startRun()
	waitRun(t, a.runner)
	a.runner.mu.Lock()
	results := append([]runResult(nil), a.runner.results...)
	a.runner.mu.Unlock()
	if len(results) != 2 {
		t.Fatalf("results: %d %+v", len(results), results)
	}
	if results[0].Iteration != 1 || results[1].Iteration != 2 {
		t.Fatalf("iterations: %+v", results)
	}
	if strings.Join(seen, ",") != "a,b" {
		t.Fatalf("server saw: %v", seen)
	}
	if results[0].Outcome != runPassed || results[1].Outcome != runPassed {
		t.Fatalf("outcomes: %+v", results)
	}
}

func TestLoadRunDataCSVAndJSON(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(csvPath, []byte("name,id\nAda,1\nAlan,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := loadRunData(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["name"] != "Ada" || rows[1]["id"] != "2" {
		t.Fatalf("csv rows: %+v", rows)
	}
	jsonPath := filepath.Join(dir, "rows.json")
	if err := os.WriteFile(jsonPath, []byte(`[{"n":1,"ok":true},{"n":2}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err = loadRunData(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["n"] != "1" || rows[0]["ok"] != "true" {
		t.Fatalf("json rows: %+v", rows)
	}
}

func TestJUnitReport(t *testing.T) {
	results := []runResult{
		{Name: "List", Iteration: 1, Outcome: runPassed, DurationMs: 12,
			Script: script.Result{Tests: []script.TestResult{{Name: "ok", Passed: true}}}},
		{Name: "List", Iteration: 2, Outcome: runFailed, Err: "boom", DurationMs: 3,
			Script: script.Result{Tests: []script.TestResult{{Name: "bad", Passed: false, Error: "expected 1 got 2"}}}},
	}
	raw := junitReport("api", results)
	var doc struct {
		Suites []struct {
			Tests    int `xml:"tests,attr"`
			Failures int `xml:"failures,attr"`
			Cases    []struct {
				Name    string `xml:"name,attr"`
				Failure *struct {
					Message string `xml:"message,attr"`
				} `xml:"failure"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("report is not XML: %v\n%s", err, raw)
	}
	if len(doc.Suites) != 1 || doc.Suites[0].Tests != 2 || doc.Suites[0].Failures != 1 {
		t.Fatalf("suite: %+v\n%s", doc.Suites, raw)
	}
	if doc.Suites[0].Cases[1].Failure == nil || !strings.Contains(doc.Suites[0].Cases[1].Failure.Message, "expected") {
		t.Fatalf("failure detail: %+v", doc.Suites[0].Cases)
	}
}
