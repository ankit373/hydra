// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// systemPrefersDark reads macOS's global appearance, which is set only in dark mode.
// An absolute path, because launchd starts the app with a minimal PATH (#689), and a
// deadline, because this runs before the window opens.
func systemPrefersDark() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleInterfaceStyle").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Dark"
}
