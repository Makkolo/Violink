// config.go - reads and writes violink.json, the config file next to the executable.
//
// Every struct has a default function. The UnmarshalJSON methods start from
// those defaults, so a field that is missing in the file keeps its default
// instead of becoming 0 / "" / false.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const configFileName = "violink.json"

// ---------------------------------------------------------------------------
// Config structs
// ---------------------------------------------------------------------------

type Config struct {
	Tray    bool           `json:"tray"`        // show the system tray icon (false = run headless)
	WebUI   WebUIConfig    `json:"webui"`       // built-in web page
	Log     LogConfig      `json:"log"`         // log files
	Persist PersistConfig  `json:"persist"`     // values and history kept between restarts
	Scripts ScriptsConfig  `json:"scripts"`     // script folder and limits
	Sims    SimsConfig     `json:"simulations"` // simulation folder
	Inputs  []InputConfig  `json:"inputs"`
	Outputs []OutputConfig `json:"outputs"`
}

type WebUIConfig struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"` // "127.0.0.1:8470" = this PC only, ":8470" = whole network
}

type LogConfig struct {
	Folder    string `json:"folder"`    // relative to the program folder
	Level     string `json:"level"`     // debug, info, warn, error
	MaxSizeKB int    `json:"maxSizeKB"` // rotate when the log file reaches this size
	MaxFiles  int    `json:"maxFiles"`  // number of old log files to keep
	Console   bool   `json:"console"`   // also print to the console (when there is one)
}

type PersistConfig struct {
	File             string `json:"file"`             // relative to the program folder
	SaveIntervalSec  int    `json:"saveIntervalSec"`  // how often changed data is written to disk
	MaxSeriesMinutes int    `json:"maxSeriesMinutes"` // history kept for average()/highest()/...
	MaxSeriesSamples int    `json:"maxSeriesSamples"` // hard cap per series, oldest dropped first
}

type ScriptsConfig struct {
	Folder      string `json:"folder"`      // relative to the program folder
	TimeoutMs   int    `json:"timeoutMs"`   // a single script call is stopped after this time
	QueueLength int    `json:"queueLength"` // values waiting per script before the oldest is dropped
}

type SimsConfig struct {
	Folder string `json:"folder"` // relative to the program folder
}

// SerialSettings is shared by COM inputs and outputs. It is embedded without a
// json tag, so the fields appear directly in the input/output object.
type SerialSettings struct {
	Port     string `json:"port,omitempty"`     // "COM3" on Windows, "/dev/ttyUSB0" on Linux
	Baud     int    `json:"baud,omitempty"`     // 9600, 19200, ...
	DataBits int    `json:"dataBits,omitempty"` // 7 or 8
	Parity   string `json:"parity,omitempty"`   // none, odd, even, mark, space
	StopBits string `json:"stopBits,omitempty"` // "1", "1.5" or "2"
}

// Framing describes how a byte stream is cut into values.
// Default: no start marker, CR LF as end marker.
type Framing struct {
	Start       string `json:"start"`       // optional start-of-string marker, e.g. "$" or "\u0002" (STX)
	End         string `json:"end"`         // end-of-string marker, e.g. "\r\n", "\r" or "\u0003" (ETX)
	MaxLength   int    `json:"maxLength"`   // longer strings are dropped as corrupt
	KeepMarkers bool   `json:"keepMarkers"` // true = start/end markers stay in the value
	IdleResetMs int    `json:"idleResetMs"` // drop a half-received string after this much silence (0 = never)
}

type PollCommand struct {
	Command    string `json:"command"`    // sent as-is, include line endings, e.g. "#01\r"
	IntervalMs int    `json:"intervalMs"` // how often
}

type InputConfig struct {
	Name           string        `json:"name"`
	Type           string        `json:"type"` // tcp-client, tcp-server, udp, com
	Enabled        bool          `json:"enabled"`
	Note           string        `json:"note,omitempty"`
	Address        string        `json:"address,omitempty"` // tcp-client: host:port, tcp-server/udp: listen address
	Remote         string        `json:"remote,omitempty"`  // udp: where commands/polls are sent (empty = last sender)
	SerialSettings               // com: port, baud, ...
	Framing        Framing       `json:"framing"`
	Poll           []PollCommand `json:"poll,omitempty"`
	IdleTimeoutSec int           `json:"idleTimeoutSec,omitempty"` // reconnect if nothing is received for this long (0 = never)
}

type OutputConfig struct {
	Name                string `json:"name"`
	Type                string `json:"type"` // tcp-server, tcp-client, udp, com
	Enabled             bool   `json:"enabled"`
	Note                string `json:"note,omitempty"`
	Address             string `json:"address,omitempty"` // tcp-server: listen address, tcp-client/udp: host:port
	SerialSettings             // com: port, baud, ...
	LineEnd             string `json:"lineEnd"`             // appended to every value sent, default "\r\n"
	ResendLastOnConnect bool   `json:"resendLastOnConnect"` // tcp: a newly connected client gets the last value at once
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func defaultConfig() Config {
	return Config{
		Tray:  true,
		WebUI: WebUIConfig{Enabled: true, Listen: "127.0.0.1:8470"},
		Log:   LogConfig{Folder: "logs", Level: "info", MaxSizeKB: 1024, MaxFiles: 5, Console: true},
		Persist: PersistConfig{
			File: "data/persist.json", SaveIntervalSec: 60,
			MaxSeriesMinutes: 180, MaxSeriesSamples: 100000,
		},
		Scripts: ScriptsConfig{Folder: "scripts", TimeoutMs: 1000, QueueLength: 64},
		Sims:    SimsConfig{Folder: "simulations"},
	}
}

func defaultFraming() Framing {
	return Framing{End: "\r\n", MaxLength: 4096}
}

func defaultSerial() SerialSettings {
	return SerialSettings{Baud: 9600, DataBits: 8, Parity: "none", StopBits: "1"}
}

func defaultInputConfig() InputConfig {
	return InputConfig{Enabled: true, Framing: defaultFraming(), SerialSettings: defaultSerial()}
}

func defaultOutputConfig() OutputConfig {
	return OutputConfig{Enabled: true, LineEnd: "\r\n", SerialSettings: defaultSerial()}
}

// The "plain" type has the same fields but no UnmarshalJSON method, so
// json.Unmarshal does not call us again in an endless loop.

func (c *Config) UnmarshalJSON(b []byte) error {
	type plain Config
	p := plain(defaultConfig())
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Config(p)
	return nil
}

func (c *InputConfig) UnmarshalJSON(b []byte) error {
	type plain InputConfig
	p := plain(defaultInputConfig())
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = InputConfig(p)
	return nil
}

func (c *OutputConfig) UnmarshalJSON(b []byte) error {
	type plain OutputConfig
	p := plain(defaultOutputConfig())
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = OutputConfig(p)
	return nil
}

func (f *Framing) UnmarshalJSON(b []byte) error {
	type plain Framing
	p := plain(defaultFraming())
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*f = Framing(p)
	return nil
}

// ---------------------------------------------------------------------------
// Load / save
// ---------------------------------------------------------------------------

var (
	configLock sync.Mutex
	config     Config // the loaded config, changed by the web UI
	configPath string
	baseDir    string // folder with the executable, config, scripts, ...
)

// loadConfig reads the config file. A missing file is an error: the program
// should not silently run with an empty setup.
func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %v", path, describeJSONError(data, err))
	}
	if err := checkConfig(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %v", path, err)
	}
	return cfg, nil
}

// checkConfig catches the mistakes that would otherwise show up as confusing
// runtime errors.
func checkConfig(cfg *Config) error {
	names := map[string]bool{}
	for i := range cfg.Inputs {
		in := &cfg.Inputs[i]
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			return fmt.Errorf("input #%d has no name", i+1)
		}
		if names[in.Name] {
			return fmt.Errorf("input name %q is used twice", in.Name)
		}
		names[in.Name] = true
		if _, ok := inputDrivers[in.Type]; !ok {
			return fmt.Errorf("input %q: unknown type %q (known: %s)", in.Name, in.Type, driverNames(inputDrivers))
		}
		clearUnusedFields(in.Type, &in.SerialSettings, &in.Address)
		if in.Type != "udp" {
			in.Remote = ""
		}
		if in.Framing.Start == "" && in.Framing.End == "" {
			return fmt.Errorf("input %q: framing needs at least a start or an end marker", in.Name)
		}
	}
	names = map[string]bool{}
	for i := range cfg.Outputs {
		out := &cfg.Outputs[i]
		out.Name = strings.TrimSpace(out.Name)
		if out.Name == "" {
			return fmt.Errorf("output #%d has no name", i+1)
		}
		if names[out.Name] {
			return fmt.Errorf("output name %q is used twice", out.Name)
		}
		names[out.Name] = true
		if _, ok := outputDrivers[out.Type]; !ok {
			return fmt.Errorf("output %q: unknown type %q (known: %s)", out.Name, out.Type, driverNames(outputDrivers))
		}
		clearUnusedFields(out.Type, &out.SerialSettings, &out.Address)
	}
	return nil
}

// clearUnusedFields empties the serial settings of network types and the
// address of COM types, so a saved config only shows what is used.
func clearUnusedFields(typ string, s *SerialSettings, address *string) {
	if typ == "com" {
		*address = ""
	} else if strings.HasPrefix(typ, "tcp") || typ == "udp" {
		*s = SerialSettings{}
	}
}

// saveConfig writes the config back to disk (used by the web UI). The previous
// file is kept as violink.json.bak, in case an edit goes wrong.
func saveConfig() error {
	configLock.Lock()
	data, err := json.MarshalIndent(config, "", "  ")
	configLock.Unlock()
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(configPath); err == nil {
		os.WriteFile(configPath+".bak", old, 0644)
	}
	return writeFileAtomic(configPath, data)
}

// writeFileAtomic writes to a temp file and renames it, so a crash or power
// cut never leaves a half-written file behind.
func writeFileAtomic(path string, data []byte) error {
	os.MkdirAll(filepath.Dir(path), 0755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// describeJSONError adds a line number to json syntax errors.
func describeJSONError(data []byte, err error) string {
	if se, ok := err.(*json.SyntaxError); ok {
		line := 1 + strings.Count(string(data[:se.Offset]), "\n")
		return fmt.Sprintf("line %d: %v", line, err)
	}
	return err.Error()
}

// resolvePath makes a config path absolute, relative to the program folder.
func resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}
