// SPDX-License-Identifier: MIT

//go:build !darwin && !windows

package keystore

// platformRing is nil here, which selects the 0600 file: no Secret Service
// daemon runs on most headless and many desktop Linux machines.
var platformRing ring
