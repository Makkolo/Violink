// processing.go - loads the scripts and runs them.
//
// Every .js file in the scripts folder is loaded once at startup (files whose
// name starts with "_" are skipped, handy for disabling one). Each script gets
// its own JavaScript engine (goja) and its own goroutine, so a slow script
// never holds up the others.
//
// A script describes itself in a `script` object and implements the event
// functions it needs:
//
//   const script = {
//       inputs:   ["wind", "baro"], // inputs to listen to
//       output:   "main",           // where returned values go
//       interval: 1000,             // ms between onTimer() calls (0 = no timer)
//   };
//
//   function onStart() { }                  // once, after loading
//   function onInput(input, value) { }      // for every value from one of the inputs
//   function onTimer() { }                  // every `interval` ms
//   function onStop() { }                   // once, at shutdown
//
// Whatever onInput/onTimer return is sent to the output: a string, a number,
// or an array of them (sent one by one). Return nothing to send nothing.
//
// Between an input and a script there is a short queue (scripts.queueLength).
// It only fills up if a script is slower than its input; then the oldest
// value is dropped, so the script always works on recent data.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

type scriptEvent struct {
	input string
	value string
	time  time.Time
}

type Script struct {
	Name     string
	file     string
	inputs   []string
	output   string
	interval time.Duration
	timeout  time.Duration

	vm      *goja.Runtime
	onStart goja.Callable
	onInput goja.Callable
	onTimer goja.Callable
	onStop  goja.Callable

	events chan scriptEvent
	stop   chan struct{}
	done   chan struct{}

	mu        sync.Mutex
	status    string
	calls     uint64
	errors    uint64
	dropped   uint64
	lastError string
	lastRun   time.Time
	busySince time.Time
}

var (
	scriptsLock    sync.RWMutex
	scripts        []*Script
	scriptsByInput = map[string][]*Script{}
)

// loadScripts loads every script in the folder. A script with an error is
// logged and skipped; the others still run.
func loadScripts(cfg ScriptsConfig) {
	folder := resolvePath(cfg.Folder)
	files, err := filepath.Glob(filepath.Join(folder, "*.js"))
	if err != nil || len(files) == 0 {
		logWarn("scripts: no .js files in %s", folder)
		return
	}
	sort.Strings(files)
	for _, file := range files {
		base := filepath.Base(file)
		if strings.HasPrefix(base, "_") {
			logInfo("script %s: skipped (name starts with _)", base)
			continue
		}
		s, err := loadScript(file, cfg)
		if err != nil {
			logError("script %s: %v", base, err)
			continue
		}
		if s == nil {
			continue // disabled
		}
		scriptsLock.Lock()
		scripts = append(scripts, s)
		for _, in := range s.inputs {
			scriptsByInput[in] = append(scriptsByInput[in], s)
		}
		scriptsLock.Unlock()
	}
}

func loadScript(file string, cfg ScriptsConfig) (*Script, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(filepath.Base(file), ".js")
	queue := cfg.QueueLength
	if queue < 1 {
		queue = 1
	}
	s := &Script{
		Name:    name,
		file:    file,
		timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
		vm:      goja.New(),
		events:  make(chan scriptEvent, queue),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		status:  "loaded",
	}
	if s.timeout <= 0 {
		s.timeout = time.Second
	}
	// Go struct fields and methods appear in JS with lower-case first letters.
	s.vm.SetFieldNameMapper(goja.UncapFieldNameMapper())
	registerScriptFunctions(s)

	// Run the top level of the file once, with a time limit (a stray
	// endless loop at the top level would otherwise hang the startup).
	timer := time.AfterFunc(5*time.Second, func() { s.vm.Interrupt("top level took longer than 5 s") })
	_, err = s.vm.RunScript(filepath.Base(file), string(src))
	timer.Stop()
	s.vm.ClearInterrupt()
	if err != nil {
		return nil, describeScriptError(err)
	}

	// Read the script object.
	// (`const script` lives in the global scope but is not a property of the
	// global object, so it is read by evaluating its name.)
	obj, _ := s.vm.RunString("typeof script === 'undefined' ? undefined : script")
	if obj == nil || goja.IsUndefined(obj) || goja.IsNull(obj) {
		return nil, fmt.Errorf("no `const script = { inputs: [...], output: \"...\" }` found")
	}
	o := obj.ToObject(s.vm)
	if v := o.Get("enabled"); v != nil && !goja.IsUndefined(v) && !v.ToBoolean() {
		logInfo("script %s: disabled (script.enabled = false)", name)
		return nil, nil
	}
	if v := o.Get("inputs"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		switch x := v.Export().(type) {
		case string:
			s.inputs = []string{x}
		case []interface{}:
			for _, item := range x {
				s.inputs = append(s.inputs, fmt.Sprint(item))
			}
		default:
			return nil, fmt.Errorf("script.inputs must be a name or a list of names")
		}
	}
	if v := o.Get("input"); v != nil && !goja.IsUndefined(v) { // allow `input: "x"` too
		s.inputs = append(s.inputs, v.String())
	}
	if v := o.Get("output"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		s.output = v.String()
	}
	if v := o.Get("interval"); v != nil && !goja.IsUndefined(v) {
		s.interval = time.Duration(v.ToInteger()) * time.Millisecond
	}

	s.onStart = getFunction(s.vm, "onStart")
	s.onInput = getFunction(s.vm, "onInput")
	s.onTimer = getFunction(s.vm, "onTimer")
	s.onStop = getFunction(s.vm, "onStop")

	// Point out likely mistakes; the script still runs.
	if len(s.inputs) > 0 && s.onInput == nil {
		logWarn("script %s: has inputs but no onInput() function", name)
	}
	if s.interval > 0 && s.onTimer == nil {
		logWarn("script %s: has an interval but no onTimer() function", name)
	}
	if s.output != "" && getOutput(s.output) == nil {
		logWarn("script %s: output %q does not exist in the config", name, s.output)
	}
	for _, in := range s.inputs {
		if getInput(in) == nil {
			logInfo("script %s: input %q is not in the config (fine if a simulation provides it)", name, in)
		}
	}
	logInfo("script %s: loaded (inputs: %s, output: %s, interval: %v)",
		name, strings.Join(s.inputs, ", "), orDash(s.output), s.interval)
	return s, nil
}

func getFunction(vm *goja.Runtime, name string) goja.Callable {
	// Evaluated by name so both `function onInput() {}` and
	// `const onInput = () => {}` are found.
	v, err := vm.RunString("typeof " + name + " === 'function' ? " + name + " : undefined")
	if err != nil || v == nil {
		return nil
	}
	fn, ok := goja.AssertFunction(v)
	if !ok {
		return nil
	}
	return fn
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func describeScriptError(err error) error {
	if ex, ok := err.(*goja.Exception); ok {
		return fmt.Errorf("%s", strings.TrimSpace(ex.String()))
	}
	return err
}

// ---------------------------------------------------------------------------
// Running
// ---------------------------------------------------------------------------

func startScripts() {
	scriptsLock.RLock()
	defer scriptsLock.RUnlock()
	for _, s := range scripts {
		go runScript(s)
	}
}

func stopScripts() {
	scriptsLock.RLock()
	list := append([]*Script{}, scripts...)
	scriptsLock.RUnlock()
	for _, s := range list {
		close(s.stop)
	}
	for _, s := range list {
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
			logWarn("script %s: did not stop in time", s.Name)
		}
	}
}

func runScript(s *Script) {
	defer close(s.done)
	s.setStatus("running")
	if s.onStart != nil {
		s.call("onStart", s.onStart)
	}

	var tick <-chan time.Time
	if s.interval > 0 && s.onTimer != nil {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		tick = t.C
	}

	for {
		select {
		case <-s.stop:
			if s.onStop != nil {
				s.call("onStop", s.onStop)
			}
			s.setStatus("stopped")
			return
		case ev := <-s.events:
			if s.onInput != nil {
				s.call("onInput", s.onInput, s.vm.ToValue(ev.input), s.vm.ToValue(ev.value))
			}
		case <-tick:
			s.call("onTimer", s.onTimer)
		}
	}
}

// call runs one script function with the time limit, and sends what it
// returns to the script's output.
func (s *Script) call(what string, fn goja.Callable, args ...goja.Value) {
	s.mu.Lock()
	s.calls++
	s.lastRun = time.Now()
	s.busySince = s.lastRun
	s.mu.Unlock()

	timer := time.AfterFunc(s.timeout, func() {
		s.vm.Interrupt(fmt.Sprintf("%s took longer than %v (scripts.timeoutMs)", what, s.timeout))
	})
	var result goja.Value
	var err error
	ok := safeCall("script "+s.Name, func() { result, err = fn(goja.Undefined(), args...) })
	timer.Stop()
	s.vm.ClearInterrupt()

	s.mu.Lock()
	s.busySince = time.Time{}
	s.mu.Unlock()

	if !ok {
		s.fail(what, "crashed (see log)")
		return
	}
	if err != nil {
		s.fail(what, describeScriptError(err).Error())
		return
	}
	s.sendResult(result)
}

func (s *Script) fail(what, msg string) {
	s.mu.Lock()
	s.errors++
	s.lastError = time.Now().Format("15:04:05 ") + what + ": " + msg
	s.mu.Unlock()
	logOnce("script:"+s.Name+":"+what+":"+msg, levelError, "script %s: %s: %s", s.Name, what, msg)
}

// sendResult sends a return value to the script's output.
func (s *Script) sendResult(v goja.Value) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return
	}
	var values []string
	switch x := v.Export().(type) {
	case []interface{}:
		for _, item := range x {
			if item != nil {
				values = append(values, fmt.Sprint(item))
			}
		}
	case bool:
		if !x {
			return // `return false` means "nothing to send"
		}
		values = []string{"true"}
	default:
		values = []string{v.String()}
	}
	if len(values) == 0 {
		return
	}
	if s.output == "" {
		logOnce("script:"+s.Name+":nooutput", levelWarn, "script %s: returned a value but has no output", s.Name)
		return
	}
	for _, val := range values {
		if err := sendToOutput(s.output, val); err != nil {
			logOnce("script:"+s.Name+":send", levelDebug, "script %s: output %s: %v", s.Name, s.output, err)
		}
	}
}

func (s *Script) setStatus(st string) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

// dispatchToScripts hands a new input value to every script that listens to it.
// It never blocks: if a script's queue is full, its oldest value is dropped.
func dispatchToScripts(input, value string, t time.Time) {
	scriptsLock.RLock()
	list := scriptsByInput[input]
	scriptsLock.RUnlock()
	ev := scriptEvent{input: input, value: value, time: t}
	for _, s := range list {
		for {
			select {
			case s.events <- ev:
			default:
				select {
				case <-s.events:
					s.mu.Lock()
					s.dropped++
					s.mu.Unlock()
					logOnce("script:"+s.Name+":drop", levelWarn, "script %s: too slow for its inputs, dropping old values", s.Name)
				default:
				}
				continue
			}
			break
		}
	}
}
