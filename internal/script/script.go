// Package script runs Bruno's JavaScript hooks — pre-request and
// post-response scripts, tests, vars:post-response and assert blocks —
// on goja, with Bruno's req/res/bru/test/expect API.
//
// A Session carries the variables a send resolves against. Scripts read
// and change them through bru.*; the caller reads Vars back afterwards
// (runtime variables live on between sends, environment changes are
// applied to the collection).
package script

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"

	"gruno/internal/send"
)

//go:embed prelude.js
var preludeSrc string

var prelude = goja.MustCompile("prelude.js", preludeSrc, false)

// DefaultTimeout bounds one phase (all pre-request or all post-response
// scripts) so a runaway loop cannot hang a send.
const DefaultTimeout = 10 * time.Second

// Script is one script block and where it came from, for error labels.
type Script struct {
	Label  string // "集合", "目录 a/b", "请求"
	Source string
}

// RuntimeVar is a bru.setVar value. Value is what {{name}} expands to;
// JSON marks a non-string value stored as JSON so bru.getVar returns
// the original type.
type RuntimeVar struct {
	Key   string
	Value string
	JSON  bool
}

// EnvSet is one bru.setEnvVar call; Persist asks for the environment
// file to be written.
type EnvSet struct {
	Key     string
	Value   string
	Persist bool
}

// Vars are the variable scopes of one send, lowest precedence first in
// Scopes(). Env and Runtime are copies the scripts change.
type Vars struct {
	ProcessEnv map[string]string // collection .env, then the OS environment
	Collection []send.Pair
	Folder     []send.Pair // folder.bru vars, outermost first
	Env        []send.Pair
	Request    []send.Pair
	// Data is the current runner data-file row, highest precedence.
	Data           []send.Pair
	Runtime        []RuntimeVar
	EnvName        string
	CollectionName string

	// penv caches the process.env scope pairs; ProcessEnv never changes
	// during a session.
	penv []send.Pair

	// EnvSets lists every bru.setEnvVar of the session, in call order.
	EnvSets []EnvSet
}

// Scopes returns the resolution scopes for a send: process.env,
// collection, folders, environment, request, runtime.
func (v *Vars) Scopes() [][]send.Pair {
	if v.penv == nil {
		v.penv = make([]send.Pair, 0, len(v.ProcessEnv))
		for k, val := range v.ProcessEnv {
			v.penv = append(v.penv, send.Pair{Key: "process.env." + k, Value: val, Enabled: true})
		}
	}
	penv := v.penv
	rt := make([]send.Pair, 0, len(v.Runtime))
	for _, r := range v.Runtime {
		rt = append(rt, send.Pair{Key: r.Key, Value: r.Value, Enabled: true})
	}
	return [][]send.Pair{penv, v.Collection, v.Folder, v.Env, v.Request, rt, v.Data}
}

func (v *Vars) runtimeIndex(key string) int {
	for i, r := range v.Runtime {
		if r.Key == key {
			return i
		}
	}
	return -1
}

func (v *Vars) setRuntime(key, value string, isJSON bool) {
	if i := v.runtimeIndex(key); i >= 0 {
		v.Runtime[i] = RuntimeVar{Key: key, Value: value, JSON: isJSON}
		return
	}
	v.Runtime = append(v.Runtime, RuntimeVar{Key: key, Value: value, JSON: isJSON})
}

func (v *Vars) setEnv(key, value string, persist bool) {
	v.EnvSets = append(v.EnvSets, EnvSet{Key: key, Value: value, Persist: persist})
	for i := len(v.Env) - 1; i >= 0; i-- {
		if v.Env[i].Key == key && v.Env[i].Enabled {
			v.Env[i].Value = value
			return
		}
	}
	v.Env = append(v.Env, send.Pair{Key: key, Value: value, Enabled: true})
}

// TestResult is one test() or assert-block outcome.
type TestResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Error  string `json:"error"`
}

// LogEntry is one console.* call.
type LogEntry struct {
	Phase string // "pre-request", "post-response", "tests"
	Level string // log, info, warn, error, debug
	Text  string
}

// ScriptError is a script that threw, with its phase and origin.
type ScriptError struct {
	Phase string
	Label string
	Err   string
}

func (e ScriptError) Error() string {
	return fmt.Sprintf("%s脚本 (%s): %s", phaseName(e.Phase), e.Label, e.Err)
}

func phaseName(p string) string {
	switch p {
	case "pre-request":
		return "前置"
	case "post-response":
		return "后置"
	case "tests":
		return "测试"
	}
	return p
}

// Result is everything a session's scripts reported.
type Result struct {
	Tests   []TestResult
	Asserts []TestResult
	Logs    []LogEntry
	Errors  []ScriptError
}

// Ran reports whether there is anything to show.
func (r *Result) Ran() bool {
	return len(r.Tests)+len(r.Asserts)+len(r.Logs)+len(r.Errors) > 0
}

// Response is the response view post-response scripts see.
type Response struct {
	Status     int
	StatusText string
	URL        string
	Headers    []send.Pair
	Body       string
	Size       int64
	DurationMs float64
}

// RunControl is a collection run's flow, as the runner-only bru calls
// set it: the next request by name (NextSet; NextNull for null, which
// ends the run), skipping the current request, stopping the run.
type RunControl struct {
	NextSet  bool
	Next     string
	NextNull bool
	Skip     bool
	Stop     bool
}

// Session runs the scripts of one send against Vars.
type Session struct {
	Vars    *Vars
	Result  Result
	Timeout time.Duration
	// Run is set when the session belongs to a collection run; without
	// it the runner-only calls warn and do nothing.
	Run *RunControl

	// Ctx cancels a script's sleep when the send or run is stopped.
	Ctx context.Context

	phase string // current phase, for console log entries
}

// NewSession starts a session over vars.
func NewSession(vars *Vars) *Session {
	return &Session{Vars: vars, Timeout: DefaultTimeout}
}

func hasCode(scripts []Script) bool {
	for _, s := range scripts {
		if strings.TrimSpace(s.Source) != "" {
			return true
		}
	}
	return false
}

// PreRequest runs the pre-request scripts in order against req, which
// they may change. The first script that throws stops the send; its
// error is returned and recorded.
func (s *Session) PreRequest(req *send.RequestData, scripts []Script) error {
	if !hasCode(scripts) {
		return nil
	}
	vm, stop, err := s.newVM("pre-request", req, nil)
	if err != nil {
		return err
	}
	defer stop()
	for _, sc := range scripts {
		if strings.TrimSpace(sc.Source) == "" {
			continue
		}
		if err := runScript(vm, sc.Source); err != nil {
			se := ScriptError{Phase: "pre-request", Label: sc.Label, Err: err.Error()}
			s.Result.Errors = append(s.Result.Errors, se)
			return se
		}
	}
	s.collect(vm)
	return readReq(vm, req)
}

// PostResponse runs, in Bruno's order, vars:post-response, the
// post-response scripts, the assert block and the tests. A throwing
// script is recorded and the rest still run.
func (s *Session) PostResponse(req send.RequestData, res Response, post []Script, postVars []send.Pair, asserts []send.Pair, tests []Script) {
	if !hasCode(post) && !hasCode(tests) && !anyEnabled(postVars) && !anyEnabled(asserts) {
		return
	}
	vm, stop, err := s.newVM("post-response", &req, &res)
	if err != nil {
		s.Result.Errors = append(s.Result.Errors, ScriptError{Phase: "post-response", Label: "初始化", Err: err.Error()})
		return
	}
	defer stop()

	setVar, _ := goja.AssertFunction(vm.Get("__setPostVar"))
	for _, p := range postVars {
		if !p.Enabled || strings.TrimSpace(p.Key) == "" {
			continue
		}
		if _, err := setVar(goja.Undefined(), vm.ToValue(p.Key), vm.ToValue(p.Value)); err != nil {
			s.Result.Errors = append(s.Result.Errors, ScriptError{Phase: "post-response", Label: "Vars " + p.Key, Err: jsErr(err)})
		}
	}
	for _, sc := range post {
		if strings.TrimSpace(sc.Source) == "" {
			continue
		}
		if err := runScript(vm, sc.Source); err != nil {
			s.Result.Errors = append(s.Result.Errors, ScriptError{Phase: "post-response", Label: sc.Label, Err: err.Error()})
		}
	}
	runAssert, _ := goja.AssertFunction(vm.Get("__runAssert"))
	for _, a := range asserts {
		if !a.Enabled || strings.TrimSpace(a.Key) == "" {
			continue
		}
		rhs, err := send.Interpolate(a.Value, s.Vars.Scopes()...)
		if err != nil {
			rhs = a.Value
		}
		if _, err := runAssert(goja.Undefined(), vm.ToValue(a.Key), vm.ToValue(rhs)); err != nil {
			s.Result.Errors = append(s.Result.Errors, ScriptError{Phase: "tests", Label: "Assert", Err: jsErr(err)})
		}
	}
	s.phase = "tests"
	for _, sc := range tests {
		if strings.TrimSpace(sc.Source) == "" {
			continue
		}
		if err := runScript(vm, sc.Source); err != nil {
			s.Result.Errors = append(s.Result.Errors, ScriptError{Phase: "tests", Label: sc.Label, Err: err.Error()})
		}
	}
	s.collect(vm)
}

func anyEnabled(ps []send.Pair) bool {
	for _, p := range ps {
		if p.Enabled && strings.TrimSpace(p.Key) != "" {
			return true
		}
	}
	return false
}

// newVM builds a runtime with the host callbacks, the request (and
// response) data and the prelude. stop cancels the phase timeout.
func (s *Session) newVM(phase string, req *send.RequestData, res *Response) (*goja.Runtime, func(), error) {
	s.phase = phase
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	if err := vm.Set("__host", s.host(vm)); err != nil {
		return nil, nil, err
	}
	reqJSON, err := jsLiteral(req)
	if err != nil {
		return nil, nil, err
	}
	if _, err := vm.RunString("var __reqData = " + reqJSON + ";"); err != nil {
		return nil, nil, err
	}
	if res != nil {
		if err := vm.Set("__resData", resData(vm, res)); err != nil {
			return nil, nil, err
		}
	}
	if _, err := vm.RunProgram(prelude); err != nil {
		return nil, nil, fmt.Errorf("prelude: %w", err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	t := time.AfterFunc(timeout, func() { vm.Interrupt(errTimeout) })
	return vm, func() { t.Stop() }, nil
}

var errTimeout = errors.New("脚本执行超时")

// jsLiteral marshals v as a JavaScript literal. U+2028 and U+2029 are
// legal in JSON strings but end a line in older JavaScript parsers, so
// they are escaped before splicing.
func jsLiteral(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	s := strings.ReplaceAll(string(data), "\u2028", "\\u2028")
	s = strings.ReplaceAll(s, "\u2029", "\\u2029")
	return s, nil
}

// resData is the response object handed to the prelude: headers keyed
// by lowercase name, repeated headers joined (set-cookie as an array).
func resData(vm *goja.Runtime, r *Response) map[string]any {
	headers := map[string]any{}
	for _, h := range r.Headers {
		k := strings.ToLower(h.Key)
		prev, ok := headers[k]
		switch {
		case !ok && k == "set-cookie":
			headers[k] = []any{h.Value}
		case !ok:
			headers[k] = h.Value
		case k == "set-cookie":
			headers[k] = append(prev.([]any), h.Value)
		default:
			headers[k] = prev.(string) + ", " + h.Value
		}
	}
	return map[string]any{
		"status":       r.Status,
		"statusText":   r.StatusText,
		"url":          r.URL,
		"headers":      vm.ToValue(headers),
		"body":         r.Body,
		"size":         r.Size,
		"responseTime": r.DurationMs,
	}
}

// host is the __host object: the Go side of console and bru.
func (s *Session) host(vm *goja.Runtime) map[string]any {
	v := s.Vars
	scopeVar := func(scope, key string) goja.Value {
		var pairs []send.Pair
		switch scope {
		case "collection":
			pairs = v.Collection
		case "folder":
			pairs = v.Folder
		case "request":
			pairs = v.Request
		}
		for i := len(pairs) - 1; i >= 0; i-- {
			if pairs[i].Enabled && pairs[i].Key == key {
				return vm.ToValue(pairs[i].Value)
			}
		}
		return goja.Undefined()
	}
	return map[string]any{
		"log": func(level, text string) {
			s.Result.Logs = append(s.Result.Logs, LogEntry{Phase: s.phase, Level: level, Text: text})
		},
		"getVar": func(key string) goja.Value {
			if i := v.runtimeIndex(key); i >= 0 {
				return vm.ToValue(map[string]any{"value": v.Runtime[i].Value, "json": v.Runtime[i].JSON})
			}
			return goja.Null()
		},
		"setVar": func(key, value string, isJSON bool) { v.setRuntime(key, value, isJSON) },
		"deleteVar": func(key string) {
			if i := v.runtimeIndex(key); i >= 0 {
				v.Runtime = append(v.Runtime[:i], v.Runtime[i+1:]...)
			}
		},
		"deleteAllVars": func() { v.Runtime = nil },
		"allVars": func() map[string]any {
			out := map[string]any{}
			for _, r := range v.Runtime {
				var val any = r.Value
				if r.JSON {
					_ = json.Unmarshal([]byte(r.Value), &val)
				}
				out[r.Key] = val
			}
			return out
		},
		"getEnvVar": func(key string) goja.Value {
			for i := len(v.Env) - 1; i >= 0; i-- {
				if v.Env[i].Enabled && v.Env[i].Key == key {
					return vm.ToValue(v.Env[i].Value)
				}
			}
			return goja.Undefined()
		},
		"setEnvVar": func(key, value string, persist bool) {
			if v.EnvName == "" {
				panic(vm.NewGoError(errors.New("bru.setEnvVar: 未选择环境")))
			}
			v.setEnv(key, value, persist)
		},
		"deleteEnvVar": func(key string) {
			out := v.Env[:0]
			for _, p := range v.Env {
				if p.Key != key {
					out = append(out, p)
				}
			}
			v.Env = out
		},
		"envName": func() goja.Value {
			if v.EnvName == "" {
				return goja.Undefined()
			}
			return vm.ToValue(v.EnvName)
		},
		"collectionName": func() string { return v.CollectionName },
		"scopeVar":       scopeVar,
		"processEnv": func(key string) goja.Value {
			if val, ok := v.ProcessEnv[key]; ok {
				return vm.ToValue(val)
			}
			return goja.Undefined()
		},
		"interpolate": func(str string) string {
			out, err := send.Interpolate(str, v.Scopes()...)
			if err != nil {
				return str
			}
			return out
		},
		"inRunner": func() bool { return s.Run != nil },
		"runnerNext": func(name goja.Value) {
			if s.Run == nil {
				return
			}
			s.Run.NextSet = true
			if name == nil || goja.IsNull(name) || goja.IsUndefined(name) {
				s.Run.NextNull, s.Run.Next = true, ""
				return
			}
			s.Run.NextNull, s.Run.Next = false, name.String()
		},
		"runnerSkip": func() {
			if s.Run != nil {
				s.Run.Skip = true
			}
		},
		"runnerStop": func() {
			if s.Run != nil {
				s.Run.Stop = true
			}
		},
		"sleep": func(ms float64) {
			d := time.Duration(min(ms, float64(s.Timeout/time.Millisecond))) * time.Millisecond
			if d <= 0 {
				return
			}
			if s.Ctx == nil {
				time.Sleep(d)
				return
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-s.Ctx.Done():
			}
		},
	}
}

// runScript runs src as the body of an async function, then waits for
// it and any async test() bodies it started. Wrapping keeps each
// script's top-level let/const apart and allows top-level await; the
// prefix sits on line 1 so error positions match the editor.
func runScript(vm *goja.Runtime, src string) error {
	v, err := vm.RunString("(async function(){" + src + "\n})().then(function(){ return Promise.all(__pending.splice(0)); })")
	if err != nil {
		return errors.New(jsErr(err))
	}
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		return nil
	}
	switch p.State() {
	case goja.PromiseStateRejected:
		return errors.New(rejection(vm, p.Result()))
	case goja.PromiseStatePending:
		return errors.New("脚本中仍有未完成的异步操作（暂不支持定时器和网络请求）")
	}
	return nil
}

func rejection(vm *goja.Runtime, v goja.Value) string {
	msg := v.String()
	if f, ok := goja.AssertFunction(vm.Get("__errMsg")); ok {
		if s, err := f(goja.Undefined(), v); err == nil {
			msg = s.String()
		}
	}
	// The thrown error's stack names the script position; the first
	// frame in the user script (not the prelude) gives the line.
	if obj, ok := v.(*goja.Object); ok {
		if st := obj.Get("stack"); st != nil && !goja.IsUndefined(st) {
			if m := stackLine.FindStringSubmatch(st.String()); m != nil {
				msg += " (第 " + m[1] + " 行)"
			}
		}
	}
	return msg
}

var stackLine = regexp.MustCompile(`<eval>:(\d+):\d+`)

// jsErr renders a goja error as "message (line:col)" without the Go
// stack.
func jsErr(err error) string {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		msg := ex.Value().String()
		if obj, ok := ex.Value().(*goja.Object); ok {
			if m := obj.Get("message"); m != nil && !goja.IsUndefined(m) {
				name := obj.Get("name")
				if name != nil && name.String() != "Error" && name.String() != "AssertionError" {
					msg = name.String() + ": " + m.String()
				} else {
					msg = m.String()
				}
			}
		}
		if frames := ex.Stack(); len(frames) > 0 {
			pos := frames[0].Position()
			if pos.Line > 0 {
				msg += fmt.Sprintf(" (第 %d 行)", pos.Line)
			}
		}
		return msg
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return errTimeout.Error()
	}
	return err.Error()
}

func (s *Session) collect(vm *goja.Runtime) {
	var tests, asserts []TestResult
	_ = vm.ExportTo(vm.Get("__results"), &tests)
	_ = vm.ExportTo(vm.Get("__asserts"), &asserts)
	s.Result.Tests = append(s.Result.Tests, tests...)
	s.Result.Asserts = append(s.Result.Asserts, asserts...)
}

func readReq(vm *goja.Runtime, req *send.RequestData) error {
	v, err := vm.RunString("JSON.stringify(__reqData)")
	if err != nil {
		return err
	}
	var out send.RequestData
	if err := json.Unmarshal([]byte(v.String()), &out); err != nil {
		return err
	}
	*req = out
	return nil
}

// LoadDotEnv reads a collection's .env file (KEY=VALUE lines, # comments,
// optional quotes) over the OS environment, for process.env.
func LoadDotEnv(path string) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if k, val, ok := strings.Cut(kv, "="); ok {
			out[k] = val
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, val = strings.TrimSpace(k), strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		out[k] = val
	}
	return out
}
