// simulation.go - virtual inputs for testing scripts without real sensors.
//
// Practice at the Edge of the Abyss before the real descent: every .json file
// in the simulations folder describes one simulated data source. While it
// runs, its lines are delivered to an input exactly as if they had been
// received. If the input name exists in the config, the simulated values are
// mixed into that input; otherwise a virtual input with that name is created.
//
// Example file (simulations/wind.json):
//
//   {
//     "name": "wind",
//     "input": "wind",
//     "intervalMs": 1000,
//     "order": "sequence",
//     "checksum": "nmea",
//     "autoStart": false,
//     "lines": [ "$WIMWV,{walk:0:359:5:0},R,{wave:3:12:60:1},M,A" ]
//   }
//
// order:    "sequence" one line per tick, in turn | "all" every line each tick | "random" one random line per tick
// checksum: "none" | "nmea" (adds *HH to lines starting with $ or ! that have none)
//
// Patterns inside lines (numbers: min, max, decimals; times in seconds):
//   {rand:min:max:dec}          random value
//   {walk:min:max:step:dec}     random walk, moves at most `step` per tick (realistic sensor drift)
//   {wave:min:max:period:dec}   sine wave
//   {ramp:min:max:period:dec}   sawtooth, min to max then back to min
//   {count} {count:start:step}  counter
//   {pick:a|b|c}                one of the options, at random
//   {time} hh:mm:ss local   {nmeatime} hhmmss.00 UTC   {nmeadate} ddmmyy UTC   {iso} 2026-10-24T09:15:00Z

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Simulation struct {
	Name       string   `json:"name"`
	Input      string   `json:"input"`
	IntervalMs int      `json:"intervalMs"`
	Order      string   `json:"order"`
	Checksum   string   `json:"checksum"`
	AutoStart  bool     `json:"autoStart"`
	Note       string   `json:"note,omitempty"`
	Lines      []string `json:"lines"`
}

type simRunner struct {
	sim     Simulation
	file    string
	running bool
	sent    uint64
	stop    chan struct{}
	state   *simState
}

// simState holds what patterns remember between ticks (walk positions, counters).
type simState struct {
	start  time.Time
	step   int
	values map[string]float64
}

var (
	simsLock   sync.Mutex
	sims       = map[string]*simRunner{}
	simsFolder string
)

var simToken = regexp.MustCompile(`\{([a-z]+)(?::([^}]*))?\}`)
var safeName = regexp.MustCompile(`[^A-Za-z0-9_\-]+`)

func loadSimulations(cfg SimsConfig) {
	simsFolder = resolvePath(cfg.Folder)
	files, _ := filepath.Glob(filepath.Join(simsFolder, "*.json"))
	sort.Strings(files)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			logError("simulation %s: %v", filepath.Base(file), err)
			continue
		}
		var sim Simulation
		if err := json.Unmarshal(data, &sim); err != nil {
			logError("simulation %s: %v", filepath.Base(file), describeJSONError(data, err))
			continue
		}
		if err := checkSim(&sim); err != nil {
			logError("simulation %s: %v", filepath.Base(file), err)
			continue
		}
		simsLock.Lock()
		sims[sim.Name] = &simRunner{sim: sim, file: file}
		simsLock.Unlock()
		logInfo("simulation %s: loaded (input %s)", sim.Name, sim.Input)
		if sim.AutoStart {
			startSim(sim.Name)
		}
	}
}

func checkSim(sim *Simulation) error {
	sim.Name = strings.TrimSpace(sim.Name)
	if sim.Name == "" {
		return errors.New("no name")
	}
	if sim.Input == "" {
		sim.Input = sim.Name
	}
	if sim.IntervalMs < 10 {
		sim.IntervalMs = 1000
	}
	if sim.Order == "" {
		sim.Order = "sequence"
	}
	if sim.Checksum == "" {
		sim.Checksum = "none"
	}
	if len(sim.Lines) == 0 {
		return errors.New("no lines")
	}
	return nil
}

func startSim(name string) error {
	simsLock.Lock()
	defer simsLock.Unlock()
	r := sims[name]
	if r == nil {
		return fmt.Errorf("no simulation %q", name)
	}
	if r.running {
		return nil
	}
	r.running = true
	r.stop = make(chan struct{})
	r.state = &simState{start: time.Now(), values: map[string]float64{}}
	in := virtualInput(r.sim.Input)
	go runSim(r, in, r.sim, r.stop, r.state)
	logInfo("simulation %s: started -> input %s every %d ms", name, r.sim.Input, r.sim.IntervalMs)
	return nil
}

func stopSim(name string) {
	simsLock.Lock()
	defer simsLock.Unlock()
	if r := sims[name]; r != nil && r.running {
		close(r.stop)
		r.running = false
		logInfo("simulation %s: stopped", name)
	}
}

func stopAllSims() {
	simsLock.Lock()
	names := []string{}
	for n := range sims {
		names = append(names, n)
	}
	simsLock.Unlock()
	for _, n := range names {
		stopSim(n)
	}
}

func runSim(r *simRunner, in *Input, sim Simulation, stop chan struct{}, st *simState) {
	t := time.NewTicker(time.Duration(sim.IntervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			for _, line := range simTick(sim, st) {
				in.deliver(line, "sim")
				simsLock.Lock()
				r.sent++
				simsLock.Unlock()
			}
		}
	}
}

// simTick returns the line(s) for one tick.
func simTick(sim Simulation, st *simState) []string {
	var picked []int
	switch sim.Order {
	case "all":
		for i := range sim.Lines {
			picked = append(picked, i)
		}
	case "random":
		picked = []int{rand.Intn(len(sim.Lines))}
	default: // sequence
		picked = []int{st.step % len(sim.Lines)}
	}
	st.step++
	out := make([]string, 0, len(picked))
	for _, i := range picked {
		line := expandPattern(sim.Lines[i], st)
		if sim.Checksum == "nmea" && (strings.HasPrefix(line, "$") || strings.HasPrefix(line, "!")) && !strings.Contains(line, "*") {
			line += "*" + nmeaChecksum(nmeaBody(line))
		}
		out = append(out, line)
	}
	return out
}

// expandPattern replaces every {token} in a line.
func expandPattern(line string, st *simState) string {
	n := 0
	return simToken.ReplaceAllStringFunc(line, func(tok string) string {
		m := simToken.FindStringSubmatch(tok)
		name, args := m[1], strings.Split(m[2], ":")
		// State (walk position, counter) is shared by the same token at the
		// same position in every line, so a sequence of similar lines
		// drifts smoothly instead of jumping between separate walks.
		key := fmt.Sprintf("%d:%s", n, tok)
		n++
		num := func(i int, def float64) float64 {
			if i < len(args) {
				if v, err := strconv.ParseFloat(strings.TrimSpace(args[i]), 64); err == nil {
					return v
				}
			}
			return def
		}
		dec := func(i int) int { return int(num(i, 0)) }
		elapsed := time.Since(st.start).Seconds()
		now := time.Now()

		switch name {
		case "rand":
			lo, hi := num(0, 0), num(1, 100)
			return strconv.FormatFloat(lo+rand.Float64()*(hi-lo), 'f', dec(2), 64)
		case "walk":
			lo, hi, step := num(0, 0), num(1, 100), num(2, 1)
			v, seen := st.values[key]
			if !seen {
				v = lo + rand.Float64()*(hi-lo)
			}
			v += (rand.Float64()*2 - 1) * step
			v = math.Max(lo, math.Min(hi, v))
			st.values[key] = v
			return strconv.FormatFloat(v, 'f', dec(3), 64)
		case "wave":
			lo, hi, period := num(0, 0), num(1, 100), num(2, 60)
			if period <= 0 {
				period = 60
			}
			v := lo + (hi-lo)*(0.5+0.5*math.Sin(2*math.Pi*elapsed/period))
			return strconv.FormatFloat(v, 'f', dec(3), 64)
		case "ramp":
			lo, hi, period := num(0, 0), num(1, 100), num(2, 60)
			if period <= 0 {
				period = 60
			}
			frac := math.Mod(elapsed, period) / period
			return strconv.FormatFloat(lo+(hi-lo)*frac, 'f', dec(3), 64)
		case "count":
			start, step := num(0, 0), num(1, 1)
			v, seen := st.values[key]
			if !seen {
				v = start
			} else {
				v += step
			}
			st.values[key] = v
			return strconv.FormatFloat(v, 'f', -1, 64)
		case "pick":
			opts := strings.Split(m[2], "|")
			return opts[rand.Intn(len(opts))]
		case "time":
			return now.Format("15:04:05")
		case "nmeatime":
			return now.UTC().Format("150405") + fmt.Sprintf(".%02d", now.Nanosecond()/10000000)
		case "nmeadate":
			return now.UTC().Format("020106")
		case "iso":
			return now.UTC().Format(time.RFC3339)
		}
		return tok // unknown: leave as typed, so the mistake is visible in the output
	})
}

// previewSim generates a few ticks without sending them (for the web UI).
func previewSim(sim Simulation, ticks int) ([]string, error) {
	if err := checkSim(&sim); err != nil {
		return nil, err
	}
	st := &simState{start: time.Now(), values: map[string]float64{}}
	var out []string
	for i := 0; i < ticks; i++ {
		out = append(out, simTick(sim, st)...)
	}
	return out, nil
}

// saveSim writes a simulation to its file. oldName is set when it was renamed.
func saveSim(oldName string, sim Simulation) error {
	if err := checkSim(&sim); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sim, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(simsFolder, safeName.ReplaceAllString(sim.Name, "_")+".json")

	simsLock.Lock()
	if _, taken := sims[sim.Name]; taken && sim.Name != oldName {
		simsLock.Unlock()
		return fmt.Errorf("a simulation called %q already exists", sim.Name)
	}
	simsLock.Unlock()

	if err := writeFileAtomic(file, data); err != nil {
		return err
	}

	wasRunning := false
	if oldName != "" {
		simsLock.Lock()
		old := sims[oldName]
		simsLock.Unlock()
		if old != nil {
			wasRunning = old.running
			stopSim(oldName)
			if old.file != file {
				os.Remove(old.file)
			}
			simsLock.Lock()
			delete(sims, oldName)
			simsLock.Unlock()
		}
	}
	simsLock.Lock()
	sims[sim.Name] = &simRunner{sim: sim, file: file}
	simsLock.Unlock()
	logInfo("simulation %s: saved to %s", sim.Name, filepath.Base(file))
	if wasRunning {
		startSim(sim.Name)
	}
	return nil
}

func deleteSim(name string) error {
	stopSim(name)
	simsLock.Lock()
	r := sims[name]
	delete(sims, name)
	simsLock.Unlock()
	if r == nil {
		return fmt.Errorf("no simulation %q", name)
	}
	logInfo("simulation %s: deleted", name)
	return os.Remove(r.file)
}

type SimInfo struct {
	Simulation
	Running bool   `json:"running"`
	Sent    uint64 `json:"sent"`
	File    string `json:"file"`
}

func listSims() []SimInfo {
	simsLock.Lock()
	defer simsLock.Unlock()
	out := []SimInfo{}
	for _, r := range sims {
		out = append(out, SimInfo{Simulation: r.sim, Running: r.running, Sent: r.sent, File: filepath.Base(r.file)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
