// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/checkgen"
)

// groundBySentence prints the per-sentence trace and exits as oracle ground does: 0, 1, or 2 not checked.
func groundBySentence(ctx context.Context, prompt, answer string, jsonOut bool) error {
	vs, v, err := checkgen.GroundBySentence(ctx, prompt, answer)
	if jsonOut {
		if perr := printJSON(checkgen.NewReport(vs)); perr != nil {
			return perr
		}
	} else {
		printSentences(vs)
	}
	switch {
	case err != nil:
		if !jsonOut {
			printNotChecked("gen:claims-ground", err)
		}
		return exitStatus(groundNotChecked)
	case !v.Passed:
		return exitStatus(groundUnsupported)
	}
	return nil
}

// printSentences is the text trace: each sentence numbered, then where it is backed or what is not.
func printSentences(vs []checkgen.SentenceVerdict) {
	if len(vs) == 0 {
		return
	}
	fmt.Fprintln(stdout)
	for _, v := range vs {
		fmt.Fprintf(stdout, "  %2d  %s\n      %s\n", v.N, v.Text, sentenceStatus(v))
	}
	fmt.Fprintln(stdout)
}

func sentenceStatus(v checkgen.SentenceVerdict) string {
	switch v.Status {
	case checkgen.StatusSupported:
		var at []string
		for _, s := range v.Sources {
			lines := fmt.Sprintf("L%d", s.StartLine)
			if s.EndLine != s.StartLine {
				lines += fmt.Sprintf("-%d", s.EndLine)
			}
			at = append(at, s.Label+" "+lines)
		}
		return okStyle.Render("supported") + dimStyle.Render(" ["+strings.Join(at, ", ")+"]")
	case checkgen.StatusNotSupported:
		var what []string
		for _, c := range v.Unsupported {
			what = append(what, string(c.Kind)+" "+c.Text)
		}
		if len(what) == 0 {
			what = append(what, v.Why)
		}
		return warnStyle.Render("NOT SUPPORTED: " + strings.Join(what, ", "))
	}
	return dimStyle.Render("nothing to check")
}
