// framing_test.go - run with: go test ./cmd/violink

package main

import (
	"reflect"
	"testing"
)

func TestFraming(t *testing.T) {
	tests := []struct {
		name    string
		framing Framing
		chunks  []string
		want    []string
		dropped int
	}{
		{"crlf lines in pieces", Framing{End: "\r\n", MaxLength: 100},
			[]string{"one\r", "\ntwo\r\nthr", "ee\r\n"}, []string{"one", "two", "three"}, 0},
		{"empty lines are skipped", Framing{End: "\r\n", MaxLength: 100},
			[]string{"a\r\n\r\nb\r\n"}, []string{"a", "b"}, 0},
		{"start marker skips junk", Framing{Start: "$", End: "\r\n", MaxLength: 100},
			[]string{"noise$GPGGA,1\r\n", "xx$GPVTG,2\r\n"}, []string{"GPGGA,1", "GPVTG,2"}, 0},
		{"keep markers", Framing{Start: "$", End: "\r\n", MaxLength: 100, KeepMarkers: true},
			[]string{"$A*00\r\n"}, []string{"$A*00\r\n"}, 0},
		{"new start before end drops the cut value", Framing{Start: "$", End: "\r\n", MaxLength: 100},
			[]string{"$BROK", "$GOOD\r\n"}, []string{"GOOD"}, 1},
		{"stx etx", Framing{Start: "\x02", End: "\x03", MaxLength: 100},
			[]string{"\x02 12.5 \x03\x02 13.0 \x03"}, []string{" 12.5 ", " 13.0 "}, 0},
		{"cr only (ADAM)", Framing{Start: ">", End: "\r", MaxLength: 100},
			[]string{">+01.234+05.000\r"}, []string{"+01.234+05.000"}, 0},
		{"overlong value dropped, next one fine", Framing{End: "\n", MaxLength: 5},
			[]string{"123456789", "0\nok\n"}, []string{"ok"}, 1},
		{"overlong in one piece", Framing{End: "\n", MaxLength: 5},
			[]string{"123456789\nok\n"}, []string{"ok"}, 1},
		{"start only: value ends at next start", Framing{Start: "#", MaxLength: 100},
			[]string{"#one#two#"}, []string{"one", "two"}, 0},
	}
	for _, tc := range tests {
		fr := newFramer(tc.framing)
		var got []string
		for _, c := range tc.chunks {
			fr.Feed([]byte(c), func(v string) { got = append(got, v) })
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if fr.dropped != tc.dropped {
			t.Errorf("%s: dropped %d, want %d", tc.name, fr.dropped, tc.dropped)
		}
	}
}

func TestSignificant(t *testing.T) {
	v, _ := aggSignificant([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9})
	if v != 8 { // mean of 7, 8, 9
		t.Errorf("significant = %v, want 8", v)
	}
}

func TestNmeaChecksum(t *testing.T) {
	if cs := nmeaChecksum(nmeaBody("$WIMWV,186,R,11.8,M,A*09")); cs != "09" {
		t.Errorf("checksum = %s, want 09", cs)
	}
}
