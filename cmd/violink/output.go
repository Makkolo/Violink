// output.go - outputs: where script results go.
//
// Like a letter from an Auto Memory Doll, every value a script returns is
// delivered to exactly one place: the output named in the script.
//
// There is no queue. Each output keeps its open connection (or listening
// socket, or COM port) and a script's send() writes straight to it. If the
// output is not connected at that moment, the value is dropped and only kept
// as "last value" for the web UI. Only the newest value matters to the end
// program, so old values piling up in a queue would only do harm.
//
// Adding a protocol works like for inputs: write func runMyOutput(o *Output) error,
// add it to outputDrivers, and call o.setWriter(fn) once connected.

package main

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

var outputDrivers = map[string]func(o *Output) error{
	"tcp-server": runTcpServerOutput,
	"tcp-client": runTcpClientOutput,
	"udp":        runUdpOutput,
	"com":        runComOutput,
}

type Output struct {
	cfg OutputConfig

	mu       sync.Mutex
	last     string
	lastTime time.Time
	count    uint64
	failed   uint64 // values that could not be sent (not connected / write error)
	status   string
	recent   []Recent
	writer   func([]byte) error

	stop chan struct{}
	done chan struct{}
}

var (
	outputsLock sync.RWMutex
	outputs     = map[string]*Output{}
	outputOrder []string
)

func startOutputs(list []OutputConfig) {
	for _, c := range list {
		o := &Output{cfg: c, stop: make(chan struct{}), done: make(chan struct{}), status: "starting"}
		outputsLock.Lock()
		outputs[c.Name] = o
		outputOrder = append(outputOrder, c.Name)
		outputsLock.Unlock()
		if !c.Enabled {
			o.status = "disabled"
			close(o.done)
			continue
		}
		go runOutput(o)
		logInfo("output %s: started (%s %s)", c.Name, c.Type, describeOutput(c))
	}
}

func stopAllOutputs() {
	outputsLock.RLock()
	list := []*Output{}
	for _, o := range outputs {
		list = append(list, o)
	}
	outputsLock.RUnlock()
	for _, o := range list {
		select {
		case <-o.stop:
		default:
			close(o.stop)
		}
	}
	for _, o := range list {
		select {
		case <-o.done:
		case <-time.After(5 * time.Second):
		}
	}
}

func getOutput(name string) *Output {
	outputsLock.RLock()
	defer outputsLock.RUnlock()
	return outputs[name]
}

func describeOutput(c OutputConfig) string {
	if c.Type == "com" {
		return describeSerial(c.SerialSettings)
	}
	return c.Address
}

// runOutput keeps the driver running, with the same growing retry delay as inputs.
func runOutput(o *Output) {
	defer close(o.done)
	driver := outputDrivers[o.cfg.Type]
	delay := time.Second
	for !o.stopped() {
		started := time.Now()
		var err error
		safeCall("output "+o.cfg.Name, func() { err = driver(o) })
		o.setWriter(nil)
		if o.stopped() {
			break
		}
		if time.Since(started) > 30*time.Second {
			delay = time.Second
		}
		msg := "connection closed"
		if err != nil {
			msg = err.Error()
		}
		o.setStatus("waiting: " + msg)
		logOnce("output:"+o.cfg.Name, levelWarn, "output %s: %s (retrying in %v)", o.cfg.Name, msg, delay)
		select {
		case <-o.stop:
		case <-time.After(delay):
		}
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
	o.setStatus("stopped")
}

func (o *Output) stopped() bool {
	select {
	case <-o.stop:
		return true
	default:
		return false
	}
}

func (o *Output) setStatus(s string) {
	o.mu.Lock()
	o.status = s
	o.mu.Unlock()
}

func (o *Output) setWriter(fn func([]byte) error) {
	o.mu.Lock()
	o.writer = fn
	o.mu.Unlock()
}

// Send delivers one value (the line end is added here).
func (o *Output) Send(value string) error {
	o.mu.Lock()
	o.last = value
	o.lastTime = time.Now()
	o.count++
	o.recent = append(o.recent, Recent{Time: o.lastTime, Value: value})
	if len(o.recent) > recentMax {
		o.recent = o.recent[len(o.recent)-recentMax:]
	}
	w := o.writer
	o.mu.Unlock()

	if !o.cfg.Enabled {
		return errors.New("output is disabled")
	}
	if w == nil {
		o.countFailed()
		return errors.New("not connected")
	}
	if err := w([]byte(value + o.cfg.LineEnd)); err != nil {
		o.countFailed()
		return err
	}
	return nil
}

func (o *Output) countFailed() {
	o.mu.Lock()
	o.failed++
	o.mu.Unlock()
}

func (o *Output) lastValue() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.last, !o.lastTime.IsZero()
}

// sendToOutput is what scripts call.
func sendToOutput(name, value string) error {
	o := getOutput(name)
	if o == nil {
		return fmt.Errorf("no output named %q", name)
	}
	return o.Send(value)
}

// ---------------------------------------------------------------------------
// Drivers
// ---------------------------------------------------------------------------

// tcp-server: the end program connects to us. Every value goes to all clients.
func runTcpServerOutput(o *Output) error {
	ln, err := net.Listen("tcp", o.cfg.Address)
	if err != nil {
		return err
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go closeOnStop(o.stop, sessionDone, ln)

	var clientsLock sync.Mutex
	clients := map[net.Conn]bool{}
	updateStatus := func() {
		clientsLock.Lock()
		n := len(clients)
		clientsLock.Unlock()
		o.setStatus(fmt.Sprintf("listening on %s, %d client(s)", o.cfg.Address, n))
	}
	removeClient := func(c net.Conn) {
		clientsLock.Lock()
		_, had := clients[c]
		delete(clients, c)
		clientsLock.Unlock()
		c.Close()
		if had && !o.stopped() {
			updateStatus()
			logInfo("output %s: client %s disconnected", o.cfg.Name, c.RemoteAddr())
		}
	}

	o.setWriter(func(b []byte) error {
		clientsLock.Lock()
		list := make([]net.Conn, 0, len(clients))
		for c := range clients {
			list = append(list, c)
		}
		clientsLock.Unlock()
		if len(list) == 0 {
			return errors.New("no clients connected")
		}
		for _, c := range list {
			c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := c.Write(b); err != nil {
				removeClient(c)
			}
		}
		return nil
	})
	updateStatus()
	logClearOnce("output:" + o.cfg.Name)
	logInfo("output %s: listening on %s", o.cfg.Name, o.cfg.Address)

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
			if o.stopped() {
				return nil
			}
			return err
		}
		clientsLock.Lock()
		clients[conn] = true
		clientsLock.Unlock()
		updateStatus()
		logInfo("output %s: client %s connected", o.cfg.Name, conn.RemoteAddr())

		if o.cfg.ResendLastOnConnect {
			if v, ok := o.lastValue(); ok {
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				conn.Write([]byte(v + o.cfg.LineEnd))
			}
		}
		// Read and ignore anything the client sends; this also notices when it leaves.
		go func(c net.Conn) {
			buf := make([]byte, 1024)
			for {
				if _, err := c.Read(buf); err != nil {
					break
				}
			}
			removeClient(c)
		}(conn)
	}
}

// tcp-client: we connect to the end program.
func runTcpClientOutput(o *Output) error {
	o.setStatus("connecting to " + o.cfg.Address)
	conn, err := net.DialTimeout("tcp", o.cfg.Address, 5*time.Second)
	if err != nil {
		return err
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go closeOnStop(o.stop, sessionDone, conn)

	broken := make(chan error, 1)
	o.setWriter(func(b []byte) error {
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, err := conn.Write(b)
		if err != nil {
			select {
			case broken <- err:
			default:
			}
		}
		return err
	})
	o.setStatus("connected to " + o.cfg.Address)
	logClearOnce("output:" + o.cfg.Name)
	logInfo("output %s: connected to %s", o.cfg.Name, o.cfg.Address)
	if o.cfg.ResendLastOnConnect {
		if v, ok := o.lastValue(); ok {
			conn.Write([]byte(v + o.cfg.LineEnd))
		}
	}

	// Read and ignore incoming data; a read error means the other side closed.
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := conn.Read(buf); err != nil {
				readErr <- err
				return
			}
		}
	}()
	select {
	case <-o.stop:
		return nil
	case err := <-broken:
		return err
	case err := <-readErr:
		if o.stopped() {
			return nil
		}
		return err
	}
}

// udp: every value is sent as one datagram to address.
func runUdpOutput(o *Output) error {
	conn, err := net.Dial("udp", o.cfg.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	o.setWriter(func(b []byte) error {
		_, err := conn.Write(b)
		return err
	})
	o.setStatus("sending to udp " + o.cfg.Address)
	logClearOnce("output:" + o.cfg.Name)
	<-o.stop
	return nil
}

// com: a serial port.
func runComOutput(o *Output) error {
	port, err := openSerial(o.cfg.SerialSettings)
	if err != nil {
		return err
	}
	defer port.Close()
	broken := make(chan error, 1)
	var writeLock sync.Mutex
	o.setWriter(func(b []byte) error {
		writeLock.Lock()
		defer writeLock.Unlock()
		_, err := port.Write(b)
		if err != nil {
			select {
			case broken <- err:
			default:
			}
		}
		return err
	})
	o.setStatus("open " + describeSerial(o.cfg.SerialSettings))
	logClearOnce("output:" + o.cfg.Name)
	logInfo("output %s: opened %s", o.cfg.Name, describeSerial(o.cfg.SerialSettings))
	select {
	case <-o.stop:
		return nil
	case err := <-broken:
		return err
	}
}
