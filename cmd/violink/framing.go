// framing.go - cuts a byte stream into separate values (strings).
//
// Serial ports and TCP deliver bytes in random chunks: half a line, three
// lines at once, a line split over two reads. The Framer collects the bytes
// and hands out one complete value at a time.
//
//   start = ""   end = "\r\n"   ->  normal lines (the default)
//   start = "$"  end = "\r\n"   ->  NMEA: anything before "$" is skipped
//   start = "\u0002" end = "\u0003"  ->  STX ... ETX telegrams
//
// Broken data is dropped instead of passed on:
//   - a value longer than maxLength (the rest up to the next end marker is skipped too)
//   - with a start marker: a new start before the end means the previous value was cut short
//   - with idleResetMs: a half value followed by silence (sensor reboot, cable pulled)

package main

import (
	"bytes"
	"time"
)

type Framer struct {
	start, end  []byte
	maxLength   int
	keepMarkers bool
	idleReset   time.Duration

	buf      []byte
	inValue  bool // true after the start marker (always true without a start marker)
	skipping bool // true after an overlong value, until the next end marker
	lastData time.Time
	dropped  int // number of broken values thrown away (for the web UI)
}

func newFramer(f Framing) *Framer {
	fr := &Framer{
		start:       []byte(f.Start),
		end:         []byte(f.End),
		maxLength:   f.MaxLength,
		keepMarkers: f.KeepMarkers,
		idleReset:   time.Duration(f.IdleResetMs) * time.Millisecond,
	}
	if fr.maxLength <= 0 {
		fr.maxLength = 4096
	}
	fr.inValue = len(fr.start) == 0
	return fr
}

func (f *Framer) reset() {
	f.buf = f.buf[:0]
	f.inValue = len(f.start) == 0
	f.skipping = false
}

// Feed adds received bytes and calls emit once for every complete value.
func (f *Framer) Feed(data []byte, emit func(value string)) {
	now := time.Now()
	if f.idleReset > 0 && len(f.buf) > 0 && now.Sub(f.lastData) > f.idleReset {
		f.dropped++
		f.reset()
	}
	f.lastData = now
	f.buf = append(f.buf, data...)

	for {
		// 1. Without a value in progress: look for the start marker.
		if !f.inValue {
			i := bytes.Index(f.buf, f.start)
			if i < 0 {
				// Keep only what could be the beginning of a start marker.
				keep := len(f.start) - 1
				if len(f.buf) > keep {
					f.buf = append(f.buf[:0], f.buf[len(f.buf)-keep:]...)
				}
				return
			}
			f.buf = f.buf[i:]
			f.inValue = true
			f.skipping = false
		}

		// 2. Inside a value: look for the end marker (or the next start when there is no end).
		bodyFrom := 0
		if len(f.start) > 0 {
			bodyFrom = len(f.start)
		}
		endAt, endLen := f.findEnd(bodyFrom)

		// A new start marker before the end means the current value was cut short.
		if len(f.start) > 0 && len(f.end) > 0 {
			next := bytes.Index(f.buf[bodyFrom:], f.start)
			if next >= 0 && (endAt < 0 || bodyFrom+next < endAt) {
				f.dropped++
				f.buf = f.buf[bodyFrom+next:]
				continue
			}
		}

		if endAt < 0 {
			if len(f.buf) > f.maxLength+len(f.start)+len(f.end) {
				// Too long without an end marker: throw it away and skip to the next end.
				f.dropped++
				f.skipping = true
				keep := len(f.end) - 1
				if keep < 0 {
					keep = 0
				}
				f.buf = append(f.buf[:0], f.buf[len(f.buf)-keep:]...)
				if len(f.start) > 0 {
					f.inValue = false
					f.skipping = false
				}
			}
			return
		}

		// Got a complete value.
		var value []byte
		if f.keepMarkers {
			value = f.buf[:endAt+endLen]
		} else {
			value = f.buf[bodyFrom:endAt]
		}
		wasSkipping := f.skipping
		f.skipping = false
		switch {
		case wasSkipping:
			// the tail of an overlong value, already counted
		case len(value) > f.maxLength+len(f.start)+len(f.end):
			f.dropped++ // arrived in one piece, but too long
		case len(value) > 0:
			emit(string(value))
		}
		f.buf = f.buf[endAt+endLen:]
		f.inValue = len(f.start) == 0
		if len(f.buf) == 0 {
			f.buf = f.buf[:0]
			return
		}
	}
}

// findEnd returns where the end of the current value is and how long the
// marker is. With a start marker but no end marker, a value ends where the
// next one starts (the start marker itself is not consumed).
func (f *Framer) findEnd(from int) (int, int) {
	if len(f.end) > 0 {
		i := bytes.Index(f.buf[from:], f.end)
		if i < 0 {
			return -1, 0
		}
		return from + i, len(f.end)
	}
	i := bytes.Index(f.buf[from:], f.start)
	if i < 0 {
		return -1, 0
	}
	return from + i, 0
}
