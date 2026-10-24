// input.go - inputs: where values come from.
//
// Every input runs in its own goroutine. A "driver" handles one protocol and
// runs one session (connect, read until the connection drops) at a time; the
// loop in runInput restarts it with a growing delay.
//
// Data path:
//   driver reads bytes -> Framer cuts them into values -> in.deliver(value)
//   -> last value is stored -> every script that listens to this input gets it.
//
// No queue between the input and the scripts' own small queues: an input only
// keeps its last value, which is what last("name") returns in a script.
//
// Adding a protocol (Modbus, MQTT, OPC UA, ...):
//   1. write  func runMyInput(in *Input) error   (in a new file, e.g. input_modbus.go)
//   2. add it to inputDrivers below
//   3. call in.feed(bytes) for byte streams, or in.deliver(value) for protocols
//      that already deliver whole values (registers, MQTT messages, ...)
//   4. call in.setWriter(fn) if the input can receive commands

package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// inputDrivers maps the "type" in the config to the function that runs it.
var inputDrivers = map[string]func(in *Input) error{
	"tcp-client": runTcpClientInput,
	"tcp-server": runTcpServerInput,
	"udp":        runUdpInput,
	"com":        runComInput,
}

const recentMax = 50 // values kept per input/output for the web UI monitor

type Recent struct {
	Time  time.Time `json:"time"`
	Value string    `json:"value"`
	Note  string    `json:"note,omitempty"` // "cmd" for commands sent, "sim" for simulated values
}

type Input struct {
	cfg     InputConfig
	virtual bool // created by a simulation, not in the config file

	mu       sync.Mutex
	last     string
	lastTime time.Time
	count    uint64
	dropped  int
	status   string
	recent   []Recent
	writer   func([]byte) error // sends a command to the device, nil when not connected

	stop chan struct{}
	done chan struct{}
}

var (
	inputsLock sync.RWMutex
	inputs     = map[string]*Input{}
	inputOrder []string // config order, for the web UI
)

// ---------------------------------------------------------------------------
// Start / stop
// ---------------------------------------------------------------------------

func startInputs(list []InputConfig) {
	for _, c := range list {
		startInput(c)
	}
}

func startInput(c InputConfig) *Input {
	in := &Input{cfg: c, stop: make(chan struct{}), done: make(chan struct{})}
	in.status = "starting"
	if !c.Enabled {
		in.status = "disabled"
		close(in.done)
	}
	inputsLock.Lock()
	if _, exists := inputs[c.Name]; !exists {
		inputOrder = append(inputOrder, c.Name)
	}
	inputs[c.Name] = in
	inputsLock.Unlock()

	if c.Enabled {
		go runInput(in)
		for _, p := range c.Poll {
			if p.Command != "" && p.IntervalMs > 0 {
				go runPoll(in, p)
			}
		}
		logInfo("input %s: started (%s %s)", c.Name, c.Type, describeInput(c))
	}
	return in
}

// stopInput stops an input and waits until its goroutine has finished.
func stopInput(name string) {
	inputsLock.RLock()
	in := inputs[name]
	inputsLock.RUnlock()
	if in == nil {
		return
	}
	select {
	case <-in.stop:
	default:
		close(in.stop)
	}
	select {
	case <-in.done:
	case <-time.After(5 * time.Second):
		logWarn("input %s: did not stop within 5 s", name)
	}
}

// removeInput stops an input and forgets it.
func removeInput(name string) {
	stopInput(name)
	inputsLock.Lock()
	delete(inputs, name)
	for i, n := range inputOrder {
		if n == name {
			inputOrder = append(inputOrder[:i], inputOrder[i+1:]...)
			break
		}
	}
	inputsLock.Unlock()
}

func stopAllInputs() {
	inputsLock.RLock()
	names := append([]string{}, inputOrder...)
	inputsLock.RUnlock()
	for _, n := range names {
		stopInput(n)
	}
}

func getInput(name string) *Input {
	inputsLock.RLock()
	defer inputsLock.RUnlock()
	return inputs[name]
}

// virtualInput returns the input with this name, and creates a virtual one
// (used by simulations) if there is none.
func virtualInput(name string) *Input {
	inputsLock.Lock()
	defer inputsLock.Unlock()
	if in, ok := inputs[name]; ok {
		return in
	}
	in := &Input{
		cfg:     InputConfig{Name: name, Type: "virtual", Enabled: true, Framing: defaultFraming()},
		virtual: true,
		status:  "virtual (simulation)",
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	close(in.done)
	inputs[name] = in
	inputOrder = append(inputOrder, name)
	return in
}

// runInput keeps the driver running until the input is stopped.
//
// Reconnect delay doubles on every failure (1, 2, 4 ... 30 s). Like the Curse
// of the Abyss, every attempt to come back up costs a little more, but here
// the curse lifts again once a connection holds for 30 seconds.
func runInput(in *Input) {
	defer close(in.done)
	driver := inputDrivers[in.cfg.Type]
	delay := time.Second
	for !in.stopped() {
		started := time.Now()
		var err error
		safeCall("input "+in.cfg.Name, func() { err = driver(in) })
		in.setWriter(nil)
		if in.stopped() {
			break
		}
		if time.Since(started) > 30*time.Second {
			delay = time.Second
		}
		msg := "connection closed"
		if err != nil {
			msg = err.Error()
		}
		in.setStatus("waiting: " + msg)
		logOnce("input:"+in.cfg.Name, levelWarn, "input %s: %s (retrying in %v)", in.cfg.Name, msg, delay)
		select {
		case <-in.stop:
		case <-time.After(delay):
		}
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
	in.setStatus("stopped")
}

// runPoll sends a command to the input at a fixed interval, for sensors that
// only answer when asked.
func runPoll(in *Input, p PollCommand) {
	t := time.NewTicker(time.Duration(p.IntervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-in.stop:
			return
		case <-t.C:
			if err := in.write([]byte(p.Command)); err != nil {
				logOnce("poll:"+in.cfg.Name, levelDebug, "input %s: poll not sent: %v", in.cfg.Name, err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers used by the drivers
// ---------------------------------------------------------------------------

func (in *Input) stopped() bool {
	select {
	case <-in.stop:
		return true
	default:
		return false
	}
}

func (in *Input) setStatus(s string) {
	in.mu.Lock()
	in.status = s
	in.mu.Unlock()
}

func (in *Input) setWriter(fn func([]byte) error) {
	in.mu.Lock()
	in.writer = fn
	in.mu.Unlock()
}

// write sends a command (poll, web UI, or command() in a script) to the device.
func (in *Input) write(data []byte) error {
	in.mu.Lock()
	w := in.writer
	in.mu.Unlock()
	if w == nil {
		if in.virtual {
			return errors.New("simulated input, nothing to send to")
		}
		return errors.New("not connected")
	}
	if err := w(data); err != nil {
		return err
	}
	in.addRecent(string(data), "cmd")
	return nil
}

// feed passes received bytes through a framer, value by value.
func (in *Input) feed(fr *Framer, data []byte) {
	fr.Feed(data, func(v string) { in.deliver(v, "") })
	if fr.dropped > 0 {
		in.mu.Lock()
		in.dropped += fr.dropped
		in.mu.Unlock()
		fr.dropped = 0
	}
}

// deliver stores a complete value and hands it to the scripts.
// note is "" for real data and "sim" for simulated/injected data.
func (in *Input) deliver(value string, note string) {
	now := time.Now()
	in.mu.Lock()
	in.last = value
	in.lastTime = now
	in.count++
	in.mu.Unlock()
	in.addRecent(value, note)
	dispatchToScripts(in.cfg.Name, value, now)
}

func (in *Input) addRecent(value, note string) {
	in.mu.Lock()
	in.recent = append(in.recent, Recent{Time: time.Now(), Value: value, Note: note})
	if len(in.recent) > recentMax {
		in.recent = in.recent[len(in.recent)-recentMax:]
	}
	in.mu.Unlock()
}

// closeOnStop closes c when the input stops or the session ends, so that a
// blocking Read or Accept returns.
func closeOnStop(stop <-chan struct{}, sessionDone <-chan struct{}, c io.Closer) {
	select {
	case <-stop:
	case <-sessionDone:
	}
	c.Close()
}

func describeInput(c InputConfig) string {
	if c.Type == "com" {
		return describeSerial(c.SerialSettings)
	}
	return c.Address
}

// describeSerial gives the usual short form, e.g. "COM3 9600 8N1".
func describeSerial(s SerialSettings) string {
	p := "N"
	if s.Parity != "" {
		p = strings.ToUpper(s.Parity[:1])
	}
	return fmt.Sprintf("%s %d %d%s%s", s.Port, s.Baud, s.DataBits, p, s.StopBits)
}

func driverNames[T any](m map[string]T) string {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Drivers
// ---------------------------------------------------------------------------

// tcp-client: connects to a device or serial server (Moxa, Lantronix, ...).
func runTcpClientInput(in *Input) error {
	in.setStatus("connecting to " + in.cfg.Address)
	conn, err := net.DialTimeout("tcp", in.cfg.Address, 5*time.Second)
	if err != nil {
		return err
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go closeOnStop(in.stop, sessionDone, conn)

	in.setStatus("connected to " + in.cfg.Address)
	logClearOnce("input:" + in.cfg.Name)
	logInfo("input %s: connected to %s", in.cfg.Name, in.cfg.Address)
	in.setWriter(func(b []byte) error {
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, err := conn.Write(b)
		return err
	})
	return readStream(in, conn, newFramer(in.cfg.Framing))
}

// readStream reads until the connection fails, the input stops, or nothing
// arrives within idleTimeoutSec.
func readStream(in *Input, conn net.Conn, fr *Framer) error {
	buf := make([]byte, 4096)
	lastData := time.Now()
	for !in.stopped() {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			lastData = time.Now()
			in.feed(fr, buf[:n])
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if in.cfg.IdleTimeoutSec > 0 && time.Since(lastData) > time.Duration(in.cfg.IdleTimeoutSec)*time.Second {
					return fmt.Errorf("no data for %d s", in.cfg.IdleTimeoutSec)
				}
				continue
			}
			if in.stopped() {
				return nil
			}
			return err
		}
	}
	return nil
}

// tcp-server: devices connect to us. Each client gets its own framer, so
// half lines from two clients are never mixed. Commands go to all clients.
func runTcpServerInput(in *Input) error {
	ln, err := net.Listen("tcp", in.cfg.Address)
	if err != nil {
		return err
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go closeOnStop(in.stop, sessionDone, ln)

	var clientsLock sync.Mutex
	clients := map[net.Conn]bool{}
	updateStatus := func() {
		clientsLock.Lock()
		n := len(clients)
		clientsLock.Unlock()
		in.setStatus(fmt.Sprintf("listening on %s, %d client(s)", in.cfg.Address, n))
	}
	updateStatus()
	logClearOnce("input:" + in.cfg.Name)
	logInfo("input %s: listening on %s", in.cfg.Name, in.cfg.Address)

	in.setWriter(func(b []byte) error {
		clientsLock.Lock()
		defer clientsLock.Unlock()
		if len(clients) == 0 {
			return errors.New("no clients connected")
		}
		for c := range clients {
			c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			c.Write(b)
		}
		return nil
	})

	defer func() {
		clientsLock.Lock()
		for c := range clients {
			c.Close()
		}
		clientsLock.Unlock()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if in.stopped() {
				return nil
			}
			return err
		}
		clientsLock.Lock()
		clients[conn] = true
		clientsLock.Unlock()
		updateStatus()
		logInfo("input %s: client %s connected", in.cfg.Name, conn.RemoteAddr())
		go func(c net.Conn) {
			readStream(in, c, newFramer(in.cfg.Framing))
			c.Close()
			clientsLock.Lock()
			delete(clients, c)
			clientsLock.Unlock()
			if !in.stopped() {
				updateStatus()
				logInfo("input %s: client %s disconnected", in.cfg.Name, c.RemoteAddr())
			}
		}(conn)
	}
}

// udp: listens for datagrams. Commands go to "remote", or to whoever sent the
// last datagram when remote is empty.
func runUdpInput(in *Input) error {
	addr, err := net.ResolveUDPAddr("udp", in.cfg.Address)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go closeOnStop(in.stop, sessionDone, conn)

	var target *net.UDPAddr
	var targetLock sync.Mutex
	if in.cfg.Remote != "" {
		target, err = net.ResolveUDPAddr("udp", in.cfg.Remote)
		if err != nil {
			return fmt.Errorf("remote %q: %v", in.cfg.Remote, err)
		}
	}
	in.setWriter(func(b []byte) error {
		targetLock.Lock()
		t := target
		targetLock.Unlock()
		if t == nil {
			return errors.New("no remote address yet (set \"remote\" or wait for a datagram)")
		}
		_, err := conn.WriteToUDP(b, t)
		return err
	})

	in.setStatus("listening on udp " + in.cfg.Address)
	logClearOnce("input:" + in.cfg.Name)
	logInfo("input %s: listening on udp %s", in.cfg.Name, in.cfg.Address)

	fr := newFramer(in.cfg.Framing)
	buf := make([]byte, 65536)
	for !in.stopped() {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if in.stopped() {
				return nil
			}
			return err
		}
		if in.cfg.Remote == "" {
			targetLock.Lock()
			target = from
			targetLock.Unlock()
		}
		in.feed(fr, buf[:n])
	}
	return nil
}

// com: a serial port (RS-232, or RS-485 through a USB converter).
func runComInput(in *Input) error {
	port, err := openSerial(in.cfg.SerialSettings)
	if err != nil {
		return err
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go closeOnStop(in.stop, sessionDone, port)

	port.SetReadTimeout(200 * time.Millisecond)
	in.setStatus("open " + describeInput(in.cfg))
	logClearOnce("input:" + in.cfg.Name)
	logInfo("input %s: opened %s", in.cfg.Name, describeInput(in.cfg))
	var writeLock sync.Mutex
	in.setWriter(func(b []byte) error {
		writeLock.Lock()
		defer writeLock.Unlock()
		_, err := port.Write(b)
		return err
	})

	fr := newFramer(in.cfg.Framing)
	buf := make([]byte, 4096)
	lastData := time.Now()
	for !in.stopped() {
		n, err := port.Read(buf)
		if err != nil {
			if in.stopped() {
				return nil
			}
			return err
		}
		if n > 0 {
			lastData = time.Now()
			in.feed(fr, buf[:n])
		} else if in.cfg.IdleTimeoutSec > 0 && time.Since(lastData) > time.Duration(in.cfg.IdleTimeoutSec)*time.Second {
			return fmt.Errorf("no data for %d s", in.cfg.IdleTimeoutSec)
		}
	}
	return nil
}

// openSerial is shared by COM inputs and outputs.
func openSerial(s SerialSettings) (serial.Port, error) {
	mode := &serial.Mode{BaudRate: s.Baud, DataBits: s.DataBits}
	switch strings.ToLower(s.Parity) {
	case "", "none", "n":
		mode.Parity = serial.NoParity
	case "odd", "o":
		mode.Parity = serial.OddParity
	case "even", "e":
		mode.Parity = serial.EvenParity
	case "mark", "m":
		mode.Parity = serial.MarkParity
	case "space", "s":
		mode.Parity = serial.SpaceParity
	default:
		return nil, fmt.Errorf("unknown parity %q", s.Parity)
	}
	switch s.StopBits {
	case "", "1":
		mode.StopBits = serial.OneStopBit
	case "1.5":
		mode.StopBits = serial.OnePointFiveStopBits
	case "2":
		mode.StopBits = serial.TwoStopBits
	default:
		return nil, fmt.Errorf("unknown stop bits %q", s.StopBits)
	}
	if s.Port == "" {
		return nil, errors.New("no port set")
	}
	return serial.Open(s.Port, mode)
}

// listSerialPorts is used by the web UI to suggest port names.
func listSerialPorts() []string {
	ports, err := serial.GetPortsList()
	if err != nil {
		return nil
	}
	sort.Strings(ports)
	return ports
}
