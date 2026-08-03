// SPDX-License-Identifier: MIT

//go:build !windows

package sysinfo

// windowsMemory is never called off Windows; it exists so Detect's switch
// compiles on every target rather than being wrapped in build tags of its own.
func windowsMemory() (totalGB, availableGB float64) { return 0, 0 }
