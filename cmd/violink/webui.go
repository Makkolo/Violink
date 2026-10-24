// webui.go - the built-in web page: status, input setup, simulation, monitor and log.
//
// The page itself (web/index.html, app.js, style.css) is built into the
// executable, so the program stays a single file. It talks to the small JSON
// API below; every endpoint is under /api/.
//
// By default it only listens on 127.0.0.1, because it can send commands to
// sensors and change the config. Set webui.listen to ":8470" to open it to
// the network, preferably only on a trusted one.

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web
var webFiles embed.FS

var (
	webServer *http.Server
	startTime = time.Now()
)

func startWebUI(cfg WebUIConfig) {
	if !cfg.Enabled {
		logInfo("web UI: disabled in config")
		return
	}
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(static)))

	mux.HandleFunc("/api/status", apiStatus)
	mux.HandleFunc("/api/recent", apiRecent)
	mux.HandleFunc("/api/log", apiLog)
	mux.HandleFunc("/api/ports", apiPorts)
	mux.HandleFunc("/api/inputs", apiInputs)
	mux.HandleFunc("/api/inputs/command", apiInputCommand)
	mux.HandleFunc("/api/inputs/inject", apiInputInject)
	mux.HandleFunc("/api/sims", apiSims)
	mux.HandleFunc("/api/sims/start", apiSimStart)
	mux.HandleFunc("/api/sims/stop", apiSimStop)
	mux.HandleFunc("/api/sims/preview", apiSimPreview)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logError("web UI: cannot listen on %s: %v", cfg.Listen, err)
		return
	}
	webServer = &http.Server{Handler: withHeaders(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := webServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			logError("web UI: %v", err)
		}
	}()
	logInfo("web UI: %s", webURL(cfg))
}

func stopWebUI() {
	if webServer != nil {
		webServer.Close()
	}
}

// webURL is the address to open in a browser (used by the tray menu too).
func webURL(cfg WebUIConfig) string {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return "http://" + cfg.Listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func withHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Auto-Memory-Doll", "at your service")
		h.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, format string, args ...interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf(format, args...)})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

type inputStatus struct {
	Config   InputConfig `json:"config"`
	Virtual  bool        `json:"virtual"`
	Status   string      `json:"status"`
	Last     string      `json:"last"`
	LastTime *time.Time  `json:"lastTime"`
	Count    uint64      `json:"count"`
	Dropped  int         `json:"dropped"`
	CanSend  bool        `json:"canSend"`
}

type outputStatus struct {
	Config   OutputConfig `json:"config"`
	Status   string       `json:"status"`
	Last     string       `json:"last"`
	LastTime *time.Time   `json:"lastTime"`
	Count    uint64       `json:"count"`
	Failed   uint64       `json:"failed"`
}

type scriptStatus struct {
	Name      string   `json:"name"`
	Inputs    []string `json:"inputs"`
	Output    string   `json:"output"`
	Interval  int64    `json:"intervalMs"`
	Status    string   `json:"status"`
	Calls     uint64   `json:"calls"`
	Errors    uint64   `json:"errors"`
	Dropped   uint64   `json:"dropped"`
	LastError string   `json:"lastError"`
	Busy      bool     `json:"busy"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	res := struct {
		Version     string         `json:"version"`
		Uptime      int64          `json:"uptimeSec"`
		Host        string         `json:"host"`
		Inputs      []inputStatus  `json:"inputs"`
		Outputs     []outputStatus `json:"outputs"`
		Scripts     []scriptStatus `json:"scripts"`
		Sims        []SimInfo      `json:"sims"`
		InputTypes  []string       `json:"inputTypes"`
		OutputTypes []string       `json:"outputTypes"`
	}{Version: version, Uptime: int64(time.Since(startTime).Seconds())}
	res.Host, _ = os.Hostname()
	res.InputTypes = strings.Split(driverNames(inputDrivers), ", ")
	res.OutputTypes = strings.Split(driverNames(outputDrivers), ", ")

	inputsLock.RLock()
	for _, name := range inputOrder {
		in := inputs[name]
		in.mu.Lock()
		res.Inputs = append(res.Inputs, inputStatus{
			Config: in.cfg, Virtual: in.virtual, Status: in.status, Last: in.last,
			LastTime: timePtr(in.lastTime), Count: in.count, Dropped: in.dropped, CanSend: in.writer != nil,
		})
		in.mu.Unlock()
	}
	inputsLock.RUnlock()

	outputsLock.RLock()
	for _, name := range outputOrder {
		o := outputs[name]
		o.mu.Lock()
		res.Outputs = append(res.Outputs, outputStatus{
			Config: o.cfg, Status: o.status, Last: o.last, LastTime: timePtr(o.lastTime),
			Count: o.count, Failed: o.failed,
		})
		o.mu.Unlock()
	}
	outputsLock.RUnlock()

	scriptsLock.RLock()
	for _, s := range scripts {
		s.mu.Lock()
		res.Scripts = append(res.Scripts, scriptStatus{
			Name: s.Name, Inputs: s.inputs, Output: s.output, Interval: s.interval.Milliseconds(),
			Status: s.status, Calls: s.calls, Errors: s.errors, Dropped: s.dropped,
			LastError: s.lastError, Busy: !s.busySince.IsZero() && time.Since(s.busySince) > 100*time.Millisecond,
		})
		s.mu.Unlock()
	}
	scriptsLock.RUnlock()

	res.Sims = listSims()
	writeJSON(w, res)
}

// /api/recent?kind=input|output&name=x  -> the last values, newest last
func apiRecent(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	var list []Recent
	switch r.URL.Query().Get("kind") {
	case "output":
		o := getOutput(name)
		if o == nil {
			writeError(w, 404, "no output %q", name)
			return
		}
		o.mu.Lock()
		list = append(list, o.recent...)
		o.mu.Unlock()
	default:
		in := getInput(name)
		if in == nil {
			writeError(w, 404, "no input %q", name)
			return
		}
		in.mu.Lock()
		list = append(list, in.recent...)
		in.mu.Unlock()
	}
	if list == nil {
		list = []Recent{}
	}
	writeJSON(w, list)
}

func apiLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, recentLog())
}

func apiPorts(w http.ResponseWriter, r *http.Request) {
	ports := listSerialPorts()
	if ports == nil {
		ports = []string{}
	}
	writeJSON(w, ports)
}

// ---------------------------------------------------------------------------
// Inputs: edit the config
// ---------------------------------------------------------------------------

// PUT /api/inputs  {original: "old name" or "", input: {...}}  -> add or change
// DELETE /api/inputs?name=x                                   -> remove
//
// The change is saved to violink.json and only the affected input restarts.
func apiInputs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var req struct {
			Original string          `json:"original"`
			Input    json.RawMessage `json:"input"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, 400, "bad request: %v", err)
			return
		}
		var c InputConfig // UnmarshalJSON fills in defaults for missing fields
		if err := json.Unmarshal(req.Input, &c); err != nil {
			writeError(w, 400, "bad input: %v", err)
			return
		}

		configLock.Lock()
		test := config
		test.Inputs = append([]InputConfig{}, config.Inputs...)
		found := false
		for i := range test.Inputs {
			if test.Inputs[i].Name == req.Original && req.Original != "" {
				test.Inputs[i] = c
				found = true
			}
		}
		if !found {
			test.Inputs = append(test.Inputs, c)
		}
		if err := checkConfig(&test); err != nil {
			configLock.Unlock()
			writeError(w, 400, "%v", err)
			return
		}
		config = test
		configLock.Unlock()

		if err := saveConfig(); err != nil {
			writeError(w, 500, "saved in memory but not to disk: %v", err)
			return
		}
		if req.Original != "" && req.Original != c.Name {
			removeInput(req.Original)
		} else {
			stopInput(c.Name)
		}
		for _, cc := range test.Inputs {
			if cc.Name == c.Name {
				startInput(cc)
			}
		}
		logInfo("web UI: input %s saved", c.Name)
		writeJSON(w, map[string]bool{"ok": true})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		configLock.Lock()
		kept := []InputConfig{}
		for _, c := range config.Inputs {
			if c.Name != name {
				kept = append(kept, c)
			}
		}
		config.Inputs = kept
		configLock.Unlock()
		if err := saveConfig(); err != nil {
			writeError(w, 500, "%v", err)
			return
		}
		removeInput(name)
		logInfo("web UI: input %s removed", name)
		writeJSON(w, map[string]bool{"ok": true})

	default:
		configLock.Lock()
		list := append([]InputConfig{}, config.Inputs...)
		configLock.Unlock()
		writeJSON(w, list)
	}
}

// POST /api/inputs/command {name, text}  -> send a command to the device
func apiInputCommand(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Text string }
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	in := getInput(req.Name)
	if in == nil {
		writeError(w, 404, "no input %q", req.Name)
		return
	}
	if err := in.write([]byte(req.Text)); err != nil {
		writeError(w, 409, "%v", err)
		return
	}
	logInfo("web UI: command sent to %s: %q", req.Name, req.Text)
	writeJSON(w, map[string]bool{"ok": true})
}

// POST /api/inputs/inject {name, text}  -> deliver a value as if it was received
func apiInputInject(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Text string }
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, 400, "no input name")
		return
	}
	virtualInput(req.Name).deliver(req.Text, "sim")
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Simulations
// ---------------------------------------------------------------------------

func apiSims(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var req struct {
			Original string     `json:"original"`
			Sim      Simulation `json:"sim"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, 400, "%v", err)
			return
		}
		if err := saveSim(req.Original, req.Sim); err != nil {
			writeError(w, 400, "%v", err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		if err := deleteSim(r.URL.Query().Get("name")); err != nil {
			writeError(w, 400, "%v", err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeJSON(w, listSims())
	}
}

func apiSimStart(w http.ResponseWriter, r *http.Request) {
	if err := startSim(r.URL.Query().Get("name")); err != nil {
		writeError(w, 404, "%v", err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiSimStop(w http.ResponseWriter, r *http.Request) {
	stopSim(r.URL.Query().Get("name"))
	writeJSON(w, map[string]bool{"ok": true})
}

func apiSimPreview(w http.ResponseWriter, r *http.Request) {
	var sim Simulation
	if err := readJSON(r, &sim); err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	lines, err := previewSim(sim, 5)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, lines)
}
