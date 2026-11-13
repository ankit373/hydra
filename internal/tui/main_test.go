// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"testing"
)

// Frames are pinned in the Unicode glyph set on every platform, so a golden
// reads the same on the Windows runner; glyphsFor has its own test.
func TestMain(m *testing.M) {
	ckGlyphSet = glyphsUnicode
	os.Exit(m.Run())
}
