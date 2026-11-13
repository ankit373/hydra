// SPDX-License-Identifier: MIT

//go:build !darwin && !windows

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// systemPrefersDark asks GNOME's colour-scheme setting, which most Linux desktops
// and the WebKitGTK webview follow. A deadline, because it runs before the window
// opens and gsettings can wait on D-Bus; anywhere it fails the light default stands.
func systemPrefersDark() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "color-scheme").Output()
	return err == nil && strings.Contains(string(out), "prefer-dark")
}
