// SPDX-License-Identifier: MIT

package sysinfo

import (
	"testing"
	"testing/fstest"
)

func TestParsePmsetBatt(t *testing.T) {
	for _, tc := range []struct {
		out       string
		on, known bool
	}{
		{"Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t99%; finishing charge; 0:00 remaining present: true\n", true, true},
		{"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t64%; discharging; 4:12 remaining present: true\n", false, true},
		{"Now drawing from 'AC Power'\n", true, true}, // a desktop Mac lists no battery
		{"", false, false},
	} {
		if on, known := parsePmsetBatt(tc.out); on != tc.on || known != tc.known {
			t.Errorf("%q: got (%v, %v), want (%v, %v)", tc.out, on, known, tc.on, tc.known)
		}
	}
}

func TestParsePowerSupply(t *testing.T) {
	for name, tc := range map[string]struct {
		fsys      fstest.MapFS
		on, known bool
	}{
		"mains online": {fstest.MapFS{
			"AC/type": {Data: []byte("Mains\n")}, "AC/online": {Data: []byte("1\n")},
			"BAT0/type": {Data: []byte("Battery\n")}, "BAT0/status": {Data: []byte("Charging\n")},
		}, true, true},
		"on battery": {fstest.MapFS{
			"AC/type": {Data: []byte("Mains\n")}, "AC/online": {Data: []byte("0\n")},
			"BAT0/type": {Data: []byte("Battery\n")}, "BAT0/status": {Data: []byte("Discharging\n")},
		}, false, true},
		"server, no battery": {fstest.MapFS{"ucsi/type": {Data: []byte("USB\n")}}, true, true},
		"battery unreadable": {fstest.MapFS{"BAT0/type": {Data: []byte("Battery\n")}}, false, false},
	} {
		if on, known := parsePowerSupply(tc.fsys); on != tc.on || known != tc.known {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", name, on, known, tc.on, tc.known)
		}
	}
	if on, known := parsePowerSupply(fstest.MapFS{}); !on || !known {
		t.Error("an empty power_supply class should read as mains")
	}
}

func TestParseLoadavg(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
		ok   bool
	}{
		{"{ 8.87 10.24 16.18 }\n", 8.87, true},       // darwin sysctl
		{"0.52 0.58 0.59 1/389 12345\n", 0.52, true}, // linux /proc
		{"", 0, false},
		{"{ x }", 0, false},
	} {
		if got, ok := parseLoadavg(tc.raw); got != tc.want || ok != tc.ok {
			t.Errorf("%q: got (%v, %v), want (%v, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}
