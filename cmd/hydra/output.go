// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"image/color"
	"io"
	"os"
	"strconv"

	"charm.land/lipgloss/v2"
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
	p := lipgloss.Writer.Profile
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
		if !xansi.HasCsiPrefix(seq) || p.Command() != 'm' {
			out.Write(seq)
			continue
		}
		if !colour {
			continue
		}
		var st xansi.Style
		params := p.Params()
		for i := 0; i < len(params); i++ {
			switch v := params[i].Param(0); v {
			case 38, 48:
				var c color.Color
				if n := xansi.ReadStyleColor(params[i:], &c); n > 0 {
					i += n - 1
				}
				switch {
				case c == nil:
				case v == 38:
					st = st.ForegroundColor(tui.ANSI16(c))
				default:
					st = st.BackgroundColor(tui.ANSI16(c))
				}
			default:
				st = append(st, strconv.Itoa(v))
			}
		}
		out.WriteString(st.String())
	}
	return out.Bytes()
}
