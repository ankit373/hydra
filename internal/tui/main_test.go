// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"testing"
)

// Frames are pinned in the Unicode glyph sets on every platform, so a golden
// reads the same on the Windows runner; markFor and glyphsFor have their own
// tests.
func TestMain(m *testing.M) {
	ckGlyphSet, ckMarkSet = glyphsUnicode, markUnicode
	os.Exit(m.Run())
}
