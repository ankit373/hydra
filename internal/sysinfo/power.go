// SPDX-License-Identifier: MIT

package sysinfo

import (
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// OnACPower reports whether the machine runs on mains power. A machine with no
// battery is on mains. known is false only when a battery's state could not be read.
func OnACPower() (on, known bool) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("pmset", "-g", "batt").Output()
		if err != nil {
			return false, false
		}
		return parsePmsetBatt(string(out))
	case "linux":
		return parsePowerSupply(os.DirFS("/sys/class/power_supply"))
	}
	return false, false
}

// LoadAverage is the one-minute load average, false where it cannot be read.
func LoadAverage() (float64, bool) {
	var raw []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		raw, err = exec.Command("sysctl", "-n", "vm.loadavg").Output()
	case "linux":
		raw, err = os.ReadFile("/proc/loadavg")
	default:
		return 0, false
	}
	if err != nil {
		return 0, false
	}
	return parseLoadavg(string(raw))
}

// parsePmsetBatt reads `pmset -g batt`, whose first line names the source.
func parsePmsetBatt(out string) (on, known bool) {
	switch {
	case strings.Contains(out, "'AC Power'"):
		return true, true
	case strings.Contains(out, "'Battery Power'"):
		return false, true
	}
	return false, false
}

// parsePowerSupply reads /sys/class/power_supply: any mains supply online, or a
// battery that is not discharging, means mains power.
func parsePowerSupply(fsys fs.FS) (on, known bool) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return true, true // no power_supply class: nothing runs on a battery
	}
	read := func(dir, name string) string {
		b, _ := fs.ReadFile(fsys, dir+"/"+name)
		return strings.TrimSpace(string(b))
	}
	battery, discharging := false, false
	for _, e := range entries {
		switch read(e.Name(), "type") {
		case "Mains":
			if read(e.Name(), "online") == "1" {
				return true, true
			}
		case "Battery":
			battery = true
			switch read(e.Name(), "status") {
			case "Discharging":
				discharging = true
			case "":
				return false, false
			}
		}
	}
	if !battery {
		return true, true
	}
	return !discharging, true
}

// parseLoadavg takes the first number of /proc/loadavg or `sysctl vm.loadavg`.
func parseLoadavg(raw string) (float64, bool) {
	fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(raw), "{ "))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
