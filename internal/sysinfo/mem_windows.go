// SPDX-License-Identifier: MIT

//go:build windows

package sysinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// GlobalMemoryStatusEx is not exported by golang.org/x/sys/windows (checked at
// v0.36.0), so it is bound here. NewLazySystemDLL rather than syscall.NewLazyDLL:
// it resolves out of the system directory only, so the lookup cannot be
// satisfied by a kernel32.dll dropped next to the binary.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX. Field order and widths are fixed by
// the Win32 ABI — the call writes into this layout, so it must not be reordered.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// windowsMemory returns (totalGB, availableGB).
//
// AvailPhys is what Windows itself shows as "Available" in Task Manager: free
// plus standby (cached-but-reclaimable) pages. That is the same notion as the
// free+inactive figure darwinMemoryState derives from vm_stat, and as the
// MemAvailable field linuxFreeRAM reads — so all three platforms feed
// EffectiveVRAMGB comparable numbers rather than three different definitions of
// "free".
//
// A (0, 0) return means detection failed. Callers must treat that as unknown
// rather than as an empty machine: reporting 0GB as a reading is what made
// Windows claim "0GB RAM · memory fully occupied" and "pressure: low" at the
// same time (#258).
func windowsMemory() (totalGB, availableGB float64) {
	if err := procGlobalMemoryStatusEx.Find(); err != nil {
		return 0, 0 // .Call would panic; an unreadable machine is "unknown"
	}
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	// Returns nonzero on success; the error is only meaningful when r == 0.
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, 0
	}
	const gb = 1 << 30
	return float64(m.TotalPhys) / gb, float64(m.AvailPhys) / gb
}
