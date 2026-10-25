// Violink - scriptable I/O bridge.
//
//   COM · TCP · UDP  ->  scripts  ->  COM · TCP · UDP
//
// Reads values from instruments (inputs), runs them through JavaScript files
// (scripts), and sends the results on to other programs (outputs). Everything
// is set up in violink.json and the scripts folder next to the executable.
//
// Source files:
//   main.go         startup and shutdown
//   config.go       violink.json
//   logger.go       log files with rotation
//   framing.go      cutting byte streams into values (start/end markers)
//   input.go        inputs and their protocol drivers
//   output.go       outputs and their protocol drivers
//   processing.go   loading and running the scripts
//   scriptfuncs.go  Go functions available to scripts
//   persist.go      values and history kept between restarts
//   simulation.go   virtual inputs for testing
//   webui.go        the built-in web page and its API
//   tray.go         the system tray icon
//
// A bridge over the abyss, lit by a field of violets.
// MD
// Todo: Improve error handling

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// version is set at build time: go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	dirFlag := flag.String("dir", "", "program folder with violink.json, scripts/ and simulations/ (default: next to the executable)")
	configFlag := flag.String("config", "", "config file (default: violink.json in the program folder)")
	consoleFlag := flag.Bool("console", false, "run without tray icon, log to the console (for testing and servers without a desktop)")
	versionFlag := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Violink %s - scriptable I/O bridge\n", version)
		return
	}

	baseDir = findBaseDir(*dirFlag)
	configPath = filepath.Join(baseDir, configFileName)
	if *configFlag != "" {
		configPath, _ = filepath.Abs(*configFlag)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		// Write the problem where it can be found, also when there is no console.
		initLog(defaultConfig().Log)
		logError("cannot start: %v", err)
		closeLog()
		os.Exit(1)
	}
	if *consoleFlag {
		cfg.Tray = false
		cfg.Log.Console = true
	}
	config = cfg

	if err := initLog(cfg.Log); err != nil {
		fmt.Fprintf(os.Stderr, "cannot open log folder: %v\n", err)
	}
	logInfo("Violink %s starting in %s - the bridge is lit", version, baseDir)

	// Start everything. Outputs first, so the first script results have somewhere to go.
	loadPersist(cfg.Persist)
	go runPersist()
	startOutputs(cfg.Outputs)
	startInputs(cfg.Inputs)
	loadScripts(cfg.Scripts)
	startScripts()
	loadSimulations(cfg.Sims)
	startWebUI(cfg.WebUI)
	logInfo("running: %d input(s), %d output(s), %d script(s)", len(cfg.Inputs), len(cfg.Outputs), len(scripts))

	// Wait for Quit in the tray, Ctrl+C, or a stop signal from the system.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	if cfg.Tray {
		go func() {
			<-signals
			quitTray()
		}()
		runTray(func() {})
	} else {
		<-signals
	}

	shutdown()
}

// shutdown stops everything in the reverse order, so scripts can still send
// a last value in onStop() and nothing is lost from the persist file.
func shutdown() {
	logInfo("stopping")
	stopWebUI()
	stopAllSims()
	stopAllInputs()
	stopScripts()
	stopAllOutputs()
	stopPersist()
	logInfo("stopped.")
	closeLog()
}

// findBaseDir decides where violink.json, scripts/ and so on are.
// Normally that is the folder of the executable. With "go run" the executable
// is in a temp folder, so then the current folder is used if it has a config.
func findBaseDir(flagDir string) string {
	if flagDir != "" {
		abs, _ := filepath.Abs(flagDir)
		return abs
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		if fileExists(filepath.Join(dir, configFileName)) {
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil && fileExists(filepath.Join(wd, configFileName)) {
		return wd
	}
	exe, _ := os.Executable()
	return filepath.Dir(exe)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
