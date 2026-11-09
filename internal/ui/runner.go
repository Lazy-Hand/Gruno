package ui

// runner.go is the collection runner: it sends every request of the
// collection or of one folder in sidebar order, runs their scripts,
// tests and asserts, and carries runtime and environment variables
// from one request to the next. The runner-only script calls steer
// it: bru.setNextRequest / bru.runner.setNextRequest jump to a request
// by name (null ends the run), bru.runner.skipRequest skips the current
// request, bru.runner.stopExecution ends the run.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"gruno/internal/script"
	"gruno/internal/send"
)

// runnerTab is the pseudo tab path of the runner view.
const runnerTab = "\x00runner"

// maxRunSteps bounds a run whose setNextRequest calls loop forever.
const maxRunSteps = 10000

// runBodyLimit is how much of each response body a run keeps.
const runBodyLimit = 256 << 10

// runStep is one request of a run, prepared on the UI thread.
type runStep struct {
	path     string
	name     string
	method   string
	input    send.SendInput
	vars     script.Vars // Runtime, Env and EnvSets are replaced per execution
	scripts  requestScripts
	postVars []send.Pair
	asserts  []send.Pair
}

type runOutcome int

const (
	runRunning runOutcome = iota
	runPassed
	runFailed  // a test, assert or script failed
	runError   // the request could not be sent
	runSkipped // bru.runner.skipRequest / stopExecution before sending
)

// runResult is one executed request of a run.
type runResult struct {
	Path, Name, Method string
	URL                string
	Outcome            runOutcome
	Status             int
	StatusText         string
	DurationMs         float64
	Size               int64
	Headers            []send.Pair
	Body               string
	BodyCut            bool
	Err                string
	Script             script.Result
}

// passedTests counts passing and all tests plus asserts.
func (r *runResult) passedTests() (passed, total int) {
	for _, l := range [][]script.TestResult{r.Script.Tests, r.Script.Asserts} {
		for _, tr := range l {
			total++
			if tr.Passed {
				passed++
			}
		}
	}
	return
}

// runnerState is the runner view: its scope, options and the current
// or last run. The run goroutine writes results under mu.
type runnerState struct {
	folder  string // "" = whole collection
	title   string
	delayIn string
	sel     int // selected result index, -1 none
	filter  int // 0 all, 1 passed, 2 failed, 3 skipped
	detail  int // detail tab: tests, response, headers

	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	planned   int
	results   []runResult
	startedAt time.Time
	elapsed   time.Duration
	note      string
	// pending carries the run's variable changes to the UI thread.
	pending *script.Vars
}

// snapshot copies what the view shows.
func (rs *runnerState) snapshot() (results []runResult, running bool, planned int, elapsed time.Duration, note string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	results = append([]runResult(nil), rs.results...)
	elapsed = rs.elapsed
	if rs.running {
		elapsed = time.Since(rs.startedAt)
	}
	return results, rs.running, rs.planned, elapsed, rs.note
}

func (rs *runnerState) put(i int, r runResult) {
	rs.mu.Lock()
	if i == len(rs.results) {
		rs.results = append(rs.results, r)
	} else {
		rs.results[i] = r
	}
	rs.mu.Unlock()
}

// runOrder lists the requests under folder ("" = collection) as the
// sidebar shows them: subfolders first, depth first, then the folder's
// own requests by seq.
func (a *app) runOrder(folder string) []*BruRequest {
	var out []*BruRequest
	var walk func(string)
	walk = func(f string) {
		for _, sub := range a.folderChildren(f) {
			walk(sub.Path)
		}
		out = append(out, a.requestsIn(f)...)
	}
	walk(folder)
	return out
}

// runSteps prepares every request of the run with the current editor
// state, environment and secrets.
func (a *app) runSteps(folder string) []runStep {
	for _, br := range a.open {
		if ts, ok := a.tabs[br.Path]; ok {
			a.syncFromUI(br, ts)
		}
	}
	secrets := a.secretPairs(a.coll.activeEnvironment())
	var steps []runStep
	for _, br := range a.runOrder(folder) {
		input := a.coll.sendInputOf(br)
		input.Settings = a.sendSettings(br)
		name := br.Req.Meta.Name
		if name == "" {
			name = br.FileName
		}
		steps = append(steps, runStep{
			path:     br.Path,
			name:     name,
			method:   strings.ToUpper(orDefaultS(br.Req.Method, "get")),
			input:    input,
			vars:     *a.coll.scriptVarsOf(br, nil, secrets),
			scripts:  a.coll.scriptsOf(br),
			postVars: dictPairs(br.Req.VarsPostResponse),
			asserts:  dictPairs(br.Req.Assert),
		})
	}
	return steps
}

// openRunner shows the runner for folder ("" = collection).
func (a *app) openRunner(folder string) {
	title := a.coll.Name
	if f := a.coll.folderByPath(folder); f != nil {
		title = f.Name
	}
	if a.runner != nil && a.runner.folder != folder {
		a.closeRunner()
	}
	if a.runner == nil {
		a.runner = &runnerState{folder: folder, title: title, delayIn: "0", sel: -1}
	}
	a.active = runnerTab
}

// closeRunner stops any run and hides the runner.
func (a *app) closeRunner() {
	if a.runner == nil {
		return
	}
	a.runner.mu.Lock()
	if a.runner.cancel != nil {
		a.runner.cancel()
	}
	a.runner.mu.Unlock()
	a.applyRunVars()
	a.runner = nil
	if a.active == runnerTab {
		a.active = ""
		if len(a.open) > 0 {
			a.active = a.open[len(a.open)-1].Path
		}
	}
}

// startRun begins a run in the background.
func (a *app) startRun() {
	rs := a.runner
	if rs == nil || a.coll == nil {
		return
	}
	rs.mu.Lock()
	if rs.running {
		rs.mu.Unlock()
		return
	}
	rs.mu.Unlock()
	a.applyRunVars()
	steps := a.runSteps(rs.folder)
	delayMs, _ := strconv.Atoi(strings.TrimSpace(rs.delayIn))
	ctx, cancel := context.WithCancel(context.Background())
	rs.mu.Lock()
	rs.running = true
	rs.cancel = cancel
	rs.planned = len(steps)
	rs.results = nil
	rs.note = ""
	rs.pending = nil
	rs.startedAt = time.Now()
	rs.elapsed = 0
	rs.mu.Unlock()
	rs.sel = -1
	svc, services := a.svc, a.services
	runtime := append([]script.RuntimeVar(nil), a.runtimeVars...)
	envName := a.coll.ActiveEnv
	go func() {
		vars, note := executeRun(ctx, svc, steps, runtime, envName, time.Duration(max(delayMs, 0))*time.Millisecond, rs, services.Invalidate)
		rs.mu.Lock()
		rs.running = false
		rs.cancel = nil
		rs.elapsed = time.Since(rs.startedAt)
		rs.note = note
		rs.pending = vars
		rs.mu.Unlock()
		cancel()
		services.Invalidate()
	}()
}

// stopRun cancels the run in flight.
func (a *app) stopRun() {
	if a.runner == nil {
		return
	}
	a.runner.mu.Lock()
	if a.runner.cancel != nil {
		a.runner.cancel()
	}
	a.runner.mu.Unlock()
}

// applyRunVars hands a finished run's variable changes to the app on
// the UI thread, as applyScriptVars does for one send.
func (a *app) applyRunVars() {
	rs := a.runner
	if rs == nil {
		return
	}
	rs.mu.Lock()
	v := rs.pending
	rs.pending = nil
	rs.mu.Unlock()
	if v == nil || a.coll == nil {
		return
	}
	a.runtimeVars = v.Runtime
	if err := a.applySecretEnvSets(v); err != nil {
		mygo.Dialog.Error("保存 Secret 失败", err.Error())
	}
	if err := a.coll.applyEnvSets(v); err != nil {
		mygo.Dialog.Error("保存环境失败", err.Error())
	}
}

// executeRun sends steps in order, honouring the runner-only script
// calls, and records each result in rs. It returns the run's variable
// changes (runtime variables and every bru.setEnvVar, in order) and a
// note on why it ended early, if it did.
func executeRun(ctx context.Context, svc *send.HTTPService, steps []runStep, runtime []script.RuntimeVar, envName string, delay time.Duration, rs *runnerState, invalidate func()) (*script.Vars, string) {
	out := &script.Vars{EnvName: envName, Runtime: runtime}
	if len(steps) == 0 {
		return out, ""
	}
	byName := map[string]int{}
	for i, st := range steps {
		if _, ok := byName[st.name]; !ok {
			byName[st.name] = i
		}
	}
	env := append([]send.Pair(nil), steps[0].vars.Env...)
	note := ""
	for i, n := 0, 0; i < len(steps); n++ {
		if ctx.Err() != nil {
			note = "运行已停止"
			break
		}
		if n >= maxRunSteps {
			note = fmt.Sprintf("已执行 %d 次请求，可能是 setNextRequest 造成了循环，运行已结束", maxRunSteps)
			break
		}
		st := steps[i]
		res := runResult{Path: st.path, Name: st.name, Method: st.method, Outcome: runRunning}
		rs.put(n, res)
		invalidate()

		vars := st.vars
		vars.Runtime = out.Runtime
		vars.Env = env
		vars.EnvSets = nil
		ctl := &script.RunControl{}
		sess := script.NewSession(&vars)
		sess.Run = ctl
		runOne(ctx, svc, st, &vars, sess, &res)
		res.Script = sess.Result
		out.Runtime = vars.Runtime
		env = vars.Env
		out.EnvSets = append(out.EnvSets, vars.EnvSets...)
		rs.put(n, res)
		invalidate()

		if ctl.Stop {
			note = "bru.runner.stopExecution() 结束了运行"
			break
		}
		next := i + 1
		if ctl.NextSet {
			if ctl.NextNull {
				note = "bru.setNextRequest(null) 结束了运行"
				break
			}
			j, ok := byName[ctl.Next]
			if !ok {
				note = fmt.Sprintf("找不到 setNextRequest 指定的请求 %q，运行已结束", ctl.Next)
				break
			}
			next = j
		}
		i = next
		if delay > 0 && i < len(steps) {
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
		}
	}
	return out, note
}

// runOne sends one step and runs its scripts into res.
func runOne(ctx context.Context, svc *send.HTTPService, st runStep, vars *script.Vars, sess *script.Session, res *runResult) {
	input := st.input
	if err := sess.PreRequest(&input.Request, st.scripts.Pre); err != nil {
		res.Outcome, res.Err = runError, err.Error()
		return
	}
	if sess.Run.Skip || sess.Run.Stop {
		res.Outcome = runSkipped
		return
	}
	input.Scopes = vars.Scopes()
	var head send.ResponseHead
	result, err := svc.Send(ctx, input, func(h send.ResponseHead) error {
		head = h
		return nil
	})
	res.URL = head.Sent.URL
	if res.URL == "" {
		res.URL = input.Request.URL
	}
	if err != nil {
		res.Outcome, res.Err = runError, err.Error()
		return
	}
	res.DurationMs, res.Size = result.DurationMs, result.SizeBytes
	if result.ErrorCode != send.ErrNone {
		res.Outcome, res.Err = runError, result.ErrorMessage
		return
	}
	res.Status, res.StatusText, res.Headers = head.StatusCode, head.StatusText, head.Headers
	body := ""
	if result.BodyKind == send.BodyKindText {
		body = result.BodyText
	}
	res.Body = respDisplayText(&result)
	if len(res.Body) > runBodyLimit {
		res.Body, res.BodyCut = res.Body[:runBodyLimit], true
	}
	sess.PostResponse(input.Request, script.Response{
		Status: head.StatusCode, StatusText: head.StatusText, URL: head.URL,
		Headers: head.Headers, Body: body, Size: result.SizeBytes, DurationMs: result.DurationMs,
	}, st.scripts.Post, st.postVars, st.asserts, st.scripts.Tests)
	res.Outcome = runPassed
	if len(sess.Result.Errors) > 0 {
		res.Outcome = runFailed
	}
	if p, t := (&runResult{Script: sess.Result}).passedTests(); p < t {
		res.Outcome = runFailed
	}
}
