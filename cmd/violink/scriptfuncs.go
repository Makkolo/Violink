// scriptfuncs.go - the Go functions that scripts can call.
//
// Scripts are normal JavaScript (ES5 plus most of ES6: let/const, arrow
// functions, template strings, classes). On top of that, every script gets
// the functions registered below.
//
// To add your own: write a Go function and add one vm.Set line in
// registerScriptFunctions. A function taking (call goja.FunctionCall) reads
// its arguments with argString/argNumber; a function with normal Go
// parameters (string, float64, ...) also works, goja converts automatically.
//
//   log(a, b, ...)               write to the log (also warn() and error(), console.log())
//   send(output, value)          send to any output, besides the script's own return value
//   last(input)                  last value of an input (string) or null
//   lastTime(input)              when it arrived (ms since 1970) or null
//   age(input)                   seconds since the last value, or null
//   command(input, text)         send a command to the device behind an input
//
//   save(key, value)             keep a value, also after a restart
//   load(key, fallback)          get it back (fallback if never saved)
//   forget(key)                  remove it
//
//   record(key, number)          add a number to a history series
//   average(key, minutes)        mean of the last X minutes (null if no data)
//   highest(key, minutes)        max
//   lowest(key, minutes)         min
//   significant(key, minutes)    mean of the highest third (H1/3, as in Hs)
//   samples(key, minutes)        how many values are in the window
//
//   nmea.checksum(text)          "4F" style checksum of a sentence
//   nmea.wrap(body)              "$" + body + "*" + checksum
//   nmea.valid(sentence)         true if the checksum is present and right
//   nmea.fields(sentence)        the comma-separated fields, checksum removed
//
//   adam.read(address, channel)  command string for an ADAM-4000 analog read, e.g. "#01\r"
//   adam.values(response)        numbers from an ADAM reply, ">+01.234-00.500" -> [1.234, -0.5]

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

func registerScriptFunctions(s *Script) {
	vm := s.vm

	// --- logging -----------------------------------------------------------
	logFn := func(level int) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			writeLog(level, "["+s.Name+"] "+joinArgs(call.Arguments))
			return goja.Undefined()
		}
	}
	vm.Set("log", logFn(levelInfo))
	vm.Set("warn", logFn(levelWarn))
	vm.Set("error", logFn(levelError))
	console := vm.NewObject()
	console.Set("log", logFn(levelInfo))
	console.Set("info", logFn(levelInfo))
	console.Set("debug", logFn(levelDebug))
	console.Set("warn", logFn(levelWarn))
	console.Set("error", logFn(levelError))
	vm.Set("console", console)

	// --- inputs and outputs -------------------------------------------------
	vm.Set("send", func(call goja.FunctionCall) goja.Value {
		out := argString(call, 0)
		err := sendToOutput(out, argString(call, 1))
		if err != nil {
			logOnce("script:"+s.Name+":send:"+out, levelDebug, "script %s: send(%q): %v", s.Name, out, err)
		}
		return vm.ToValue(err == nil)
	})

	vm.Set("last", func(name string) interface{} {
		in := getInput(name)
		if in == nil {
			return nil
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.lastTime.IsZero() {
			return nil
		}
		return in.last
	})

	vm.Set("lastTime", func(name string) interface{} {
		in := getInput(name)
		if in == nil {
			return nil
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.lastTime.IsZero() {
			return nil
		}
		return in.lastTime.UnixMilli()
	})

	vm.Set("age", func(name string) interface{} {
		in := getInput(name)
		if in == nil {
			return nil
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.lastTime.IsZero() {
			return nil
		}
		return time.Since(in.lastTime).Seconds()
	})

	vm.Set("command", func(name, text string) bool {
		in := getInput(name)
		if in == nil {
			logOnce("script:"+s.Name+":cmd:"+name, levelWarn, "script %s: command(): no input named %q", s.Name, name)
			return false
		}
		if err := in.write([]byte(text)); err != nil {
			logOnce("script:"+s.Name+":cmd:"+name, levelDebug, "script %s: command(%q): %v", s.Name, name, err)
			return false
		}
		return true
	})

	// --- persistent values --------------------------------------------------
	key := func(k string) string { return s.Name + "/" + k }

	vm.Set("save", func(call goja.FunctionCall) goja.Value {
		k := argString(call, 0)
		v := call.Argument(1).Export()
		// Store a plain JSON copy, so later changes to the JS object do not
		// leak into the saved value.
		data, err := json.Marshal(v)
		if err != nil {
			panic(vm.NewTypeError("save(%q): value cannot be stored: %v", k, err))
		}
		var plain interface{}
		json.Unmarshal(data, &plain)
		persistSet(key(k), plain)
		return goja.Undefined()
	})

	vm.Set("load", func(call goja.FunctionCall) goja.Value {
		if v, ok := persistGet(key(argString(call, 0))); ok {
			return vm.ToValue(v)
		}
		return call.Argument(1) // the fallback (undefined if not given)
	})

	vm.Set("forget", func(k string) {
		persistDelete(key(k))
	})

	// --- history and aggregates --------------------------------------------
	vm.Set("record", func(call goja.FunctionCall) goja.Value {
		v := call.Argument(1).ToFloat()
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return vm.ToValue(false) // not a number: ignored
		}
		t := time.Now()
		if len(call.Arguments) > 2 && !goja.IsUndefined(call.Argument(2)) {
			t = time.UnixMilli(call.Argument(2).ToInteger())
		}
		seriesAdd(key(argString(call, 0)), v, t)
		return vm.ToValue(true)
	})

	aggregate := func(fn func([]float64) (float64, bool)) func(string, float64) interface{} {
		return func(k string, minutes float64) interface{} {
			if v, ok := fn(seriesWindow(key(k), minutes)); ok {
				return v
			}
			return nil
		}
	}
	vm.Set("average", aggregate(aggAverage))
	vm.Set("highest", aggregate(aggHighest))
	vm.Set("lowest", aggregate(aggLowest))
	vm.Set("significant", aggregate(aggSignificant))
	vm.Set("samples", func(k string, minutes float64) int {
		return len(seriesWindow(key(k), minutes))
	})

	// --- NMEA 0183 helpers ---------------------------------------------------
	nmea := vm.NewObject()
	nmea.Set("checksum", func(text string) string {
		return nmeaChecksum(nmeaBody(text))
	})
	nmea.Set("wrap", func(body string) string {
		lead := "$"
		if strings.HasPrefix(body, "$") || strings.HasPrefix(body, "!") {
			lead = body[:1]
		}
		b := nmeaBody(body)
		return lead + b + "*" + nmeaChecksum(b)
	})
	nmea.Set("valid", func(sentence string) bool {
		sentence = strings.TrimSpace(sentence)
		star := strings.LastIndex(sentence, "*")
		if star < 0 || len(sentence) < star+3 {
			return false
		}
		return strings.EqualFold(sentence[star+1:star+3], nmeaChecksum(nmeaBody(sentence)))
	})
	nmea.Set("fields", func(sentence string) []string {
		return strings.Split(nmeaBody(strings.TrimSpace(sentence)), ",")
	})
	vm.Set("nmea", nmea)

	// --- Advantech ADAM-4000 helpers ------------------------------------------
	// ADAM-4000 modules talk a simple ASCII protocol on RS-485:
	//   "#AA\r"  read all analog inputs of module AA (hex address)
	//   "#AAN\r" read channel N only
	// and answer like ">+01.234+05.678...\r". Use them as a COM input with
	// framing end "\r" and a poll command. (ADAM-6000 modules use Modbus TCP,
	// which needs a Modbus input driver.)
	adam := vm.NewObject()
	adam.Set("read", func(call goja.FunctionCall) goja.Value {
		cmd := fmt.Sprintf("#%02X", int(argNumber(call, 0)))
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) {
			cmd += strconv.Itoa(int(argNumber(call, 1)))
		}
		return vm.ToValue(cmd + "\r")
	})
	adam.Set("values", func(response string) []float64 {
		out := []float64{}
		for _, m := range adamNumber.FindAllString(response, -1) {
			if v, err := strconv.ParseFloat(m, 64); err == nil {
				out = append(out, v)
			}
		}
		return out
	})
	vm.Set("adam", adam)
}

var adamNumber = regexp.MustCompile(`[+-]\d+(\.\d+)?`)

// nmeaBody strips the leading $ or ! and everything from * on.
func nmeaBody(s string) string {
	s = strings.TrimLeft(s, "$!")
	if i := strings.Index(s, "*"); i >= 0 {
		s = s[:i]
	}
	return s
}

func nmeaChecksum(body string) string {
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return fmt.Sprintf("%02X", cs)
}

// --- argument helpers ---------------------------------------------------------

func argString(call goja.FunctionCall, i int) string {
	v := call.Argument(i)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

func argNumber(call goja.FunctionCall, i int) float64 {
	return call.Argument(i).ToFloat()
}

// joinArgs formats log arguments like console.log does: strings as they are,
// objects and arrays as JSON.
func joinArgs(args []goja.Value) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if obj, ok := a.(*goja.Object); ok && obj.ClassName() != "Function" && obj.ClassName() != "Error" {
			if b, err := json.Marshal(a.Export()); err == nil {
				parts = append(parts, string(b))
				continue
			}
		}
		parts = append(parts, a.String())
	}
	return strings.Join(parts, " ")
}
