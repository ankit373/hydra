// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"image/color"
	"io"
	"os"
	"strconv"

	"github.com/charmbracelet/colorprofile"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/ankit373/hydra/internal/tui"
)

// stdout and stderr are the process's streams at the colour depth stdout can
// show. Lip Gloss v2 renders colour unconditionally and leaves downsampling to
// the writer, so every styled line goes through here or NO_COLOR is ignored.
var (
	stdout io.Writer = profileWriter{&os.Stdout}
	stderr io.Writer = profileWriter{&os.Stderr}
)

// profileWriter resolves the stream at each write, so a test that swaps
// os.Stdout still captures what is printed.
type profileWriter struct{ f **os.File }

func (w profileWriter) Write(b []byte) (int, error) {
	p := tui.OutputProfile()
	if p > colorprofile.ANSI {
		return (&colorprofile.Writer{Forward: *w.f, Profile: p}).Write(b)
	}
	// No colour (NO_COLOR, a pipe) drops SGR entirely, bold and reverse
	// included, and 16 colours take v1's pick; cursor movement still goes out.
	if _, err := (*w.f).Write(rewriteSGR(b, p == colorprofile.ANSI)); err != nil {
		return 0, err
	}
	return len(b), nil
}

// rewriteSGR keeps every sequence but SGR. With colour it keeps SGR too, each
// colour moved to tui.ANSI16's basic pick; without, it drops SGR.
func rewriteSGR(b []byte, colour bool) []byte {
	var out bytes.Buffer
	var state byte
	p := xansi.GetParser()
	defer xansi.PutParser(p)
	for len(b) > 0 {
		p.Reset()
		seq, _, n, next := xansi.DecodeSequence(b, state, p)
		b, state = b[n:], next
		switch {
		case !xansi.HasCsiPrefix(seq) || p.Command() != 'm':
			out.Write(seq)
		case colour:
			out.WriteString(basicColours(p.Params()))
		}
	}
	return out.Bytes()
}

// basicColours rewrites one SGR's 256 and truecolor colours to basic ones and
// keeps every other parameter as written, colon sub-parameters included.
func basicColours(params xansi.Params) string {
	var b bytes.Buffer
	b.WriteString("\x1b[")
	sep := ""
	for i := 0; i < len(params); i++ {
		b.WriteString(sep)
		v := params[i].Param(0)
		if v != 38 && v != 48 {
			b.WriteString(strconv.Itoa(v))
			if sep = ";"; params[i].HasMore() {
				sep = ":"
			}
			continue
		}
		var c color.Color
		if n := xansi.ReadStyleColor(params[i:], &c); n > 0 {
			i += n - 1
		}
		if c == nil {
			b.WriteString(strconv.Itoa(v)) // malformed: left for the terminal
		} else {
			b.WriteString(strconv.Itoa(basicSGR(tui.ANSI16(c), v == 48)))
		}
		sep = ";"
	}
	b.WriteString("m")
	return b.String()
}

// basicSGR is the SGR code that draws basic colour c: 30-37 and 90-97 for
// the foreground, 40-47 and 100-107 for the background.
func basicSGR(c xansi.BasicColor, background bool) int {
	n, code := int(c), 30
	if n >= 8 {
		n, code = n-8, 90
	}
	if background {
		code += 10
	}
	return code + n
}
