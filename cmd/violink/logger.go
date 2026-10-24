// logger.go - log files with size-based rotation, plus a small in-memory
// buffer of recent lines for the web UI.
//
// Files: logs/violink.log (current), violink.1.log (newest old) ... violink.N.log.
// When the current file passes log.maxSizeKB it is renamed to .1, the others
// shift up one, and the oldest falls off the end. Disk use is therefore never
// more than about maxSizeKB * (maxFiles + 1).
//
// Levels, ranked like the whistles of cave raiders:
//   debug = Red Whistle    (everything, for the curious)
//   info  = Blue Whistle   (normal operation)
//   warn  = Moon Whistle   (something is off, but we keep going)
//   error = Black Whistle  (something failed)
// There is no White Whistle level. Some depths are not meant for log files.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const (
	levelDebug = 0
	levelInfo  = 1
	levelWarn  = 2
	levelError = 3
)

var levelNames = []string{"DEBUG", "INFO", "WARN", "ERROR"}

type LogLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

var (
	logLock     sync.Mutex
	logFile     *os.File
	logPath     string
	logSize     int64
	logMaxSize  int64
	logMaxFiles int
	logLevel    = levelInfo
	logConsole  = true
	logRecent   []LogLine // last lines, shown in the web UI
	logLimited  = map[string]time.Time{}
)

const logRecentMax = 300

// initLog opens the log file. Until it is called, log lines only go to the console.
func initLog(cfg LogConfig) error {
	logLock.Lock()
	defer logLock.Unlock()

	logLevel = parseLevel(cfg.Level)
	logConsole = cfg.Console
	logMaxSize = int64(cfg.MaxSizeKB) * 1024
	if logMaxSize < 16*1024 {
		logMaxSize = 16 * 1024
	}
	logMaxFiles = cfg.MaxFiles
	if logMaxFiles < 0 {
		logMaxFiles = 0
	}

	folder := resolvePath(cfg.Folder)
	if err := os.MkdirAll(folder, 0755); err != nil {
		return err
	}
	logPath = filepath.Join(folder, "violink.log")
	return openLogFile()
}

func closeLog() {
	logLock.Lock()
	defer logLock.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

func parseLevel(s string) int {
	switch strings.ToLower(s) {
	case "debug":
		return levelDebug
	case "warn", "warning":
		return levelWarn
	case "error":
		return levelError
	}
	return levelInfo
}

// openLogFile must be called with logLock held.
func openLogFile() error {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	logFile = f
	logSize = 0
	if st != nil {
		logSize = st.Size()
	}
	return nil
}

// rotateLog must be called with logLock held.
func rotateLog() {
	logFile.Close()
	logFile = nil
	base := strings.TrimSuffix(logPath, ".log")
	if logMaxFiles == 0 {
		os.Remove(logPath)
	} else {
		os.Remove(fmt.Sprintf("%s.%d.log", base, logMaxFiles))
		for i := logMaxFiles - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d.log", base, i), fmt.Sprintf("%s.%d.log", base, i+1))
		}
		os.Rename(logPath, base+".1.log")
	}
	openLogFile()
}

func writeLog(level int, text string) {
	if level < logLevel {
		return
	}
	now := time.Now()
	line := fmt.Sprintf("%s %-5s %s\n", now.Format("2006-01-02 15:04:05.000"), levelNames[level], text)

	logLock.Lock()
	defer logLock.Unlock()

	logRecent = append(logRecent, LogLine{Time: now, Level: levelNames[level], Text: text})
	if len(logRecent) > logRecentMax {
		logRecent = logRecent[len(logRecent)-logRecentMax:]
	}
	if logConsole {
		os.Stdout.WriteString(line)
	}
	if logFile != nil {
		if logSize+int64(len(line)) > logMaxSize {
			rotateLog()
		}
		if logFile != nil {
			n, _ := logFile.WriteString(line)
			logSize += int64(n)
		}
	}
}

func logDebug(format string, args ...interface{}) { writeLog(levelDebug, fmt.Sprintf(format, args...)) }
func logInfo(format string, args ...interface{})  { writeLog(levelInfo, fmt.Sprintf(format, args...)) }
func logWarn(format string, args ...interface{})  { writeLog(levelWarn, fmt.Sprintf(format, args...)) }
func logError(format string, args ...interface{}) { writeLog(levelError, fmt.Sprintf(format, args...)) }

// logOnce logs a message at most once per minute for the same key. Used for
// things that repeat, like "COM3 not found" every few seconds, so a sensor
// that is unplugged for a weekend does not fill the disk with the same line.
func logOnce(key string, level int, format string, args ...interface{}) {
	logLock.Lock()
	last, seen := logLimited[key]
	now := time.Now()
	if seen && now.Sub(last) < time.Minute {
		logLock.Unlock()
		return
	}
	logLimited[key] = now
	logLock.Unlock()
	writeLog(level, fmt.Sprintf(format, args...))
}

// logClearOnce forgets a logOnce key, so the next problem is logged right away.
func logClearOnce(key string) {
	logLock.Lock()
	delete(logLimited, key)
	logLock.Unlock()
}

func recentLog() []LogLine {
	logLock.Lock()
	defer logLock.Unlock()
	out := make([]LogLine, len(logRecent))
	copy(out, logRecent)
	return out
}

// safeCall runs fn and turns a panic into a logged error, so one bad input or
// script can never take the whole program down.
// Whatever can go wrong, will (Murphy's law, the Interstellar kind: it just
// means "something that happens"). So we plan for it.
func safeCall(what string, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			logError("%s crashed: %v\n%s", what, r, debug.Stack())
			ok = false
		}
	}()
	fn()
	return true
}
