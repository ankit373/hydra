// SPDX-License-Identifier: MIT

package suites

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/executor"
)

// gsm8kAnswerTokens bounds a worked answer; GSM8K's reference solutions run to about 300.
const gsm8kAnswerTokens = 512

var (
	numberRe   = regexp.MustCompile(`-?[\d,]*\.?\d+`)
	hashesRe   = regexp.MustCompile(`####\s*(-?[\d,]*\.?\d+)`)
	calcNoteRe = regexp.MustCompile(`<<([^=<>]+)=([^<>]+)>>`)
)

// gsm8k is OpenAI's GSM8K test split, graded on the final number. Ids are test/<row>.
type gsm8k struct{}

func (gsm8k) Name() string     { return "gsm8k" }
func (gsm8k) Class() string    { return "answer.gsm8k" }
func (gsm8k) Labels() []string { return nil }

func (s gsm8k) Rows(ds bench.Dataset, dataDir string) ([]bench.Row, error) {
	if err := ds.RequireFiles(s.Name(), "test.jsonl"); err != nil {
		return nil, err
	}
	raw, err := ds.Open(dataDir, "test.jsonl")
	if err != nil {
		return nil, err
	}
	var recs []struct{ Question, Answer string }
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var r struct{ Question, Answer string }
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("suites: gsm8k test.jsonl line %d: %w", len(recs)+1, err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("suites: gsm8k test.jsonl: %w", err)
	}
	w := width(len(recs))
	rows := make([]bench.Row, len(recs))
	for i, r := range recs {
		gold, strict := finalNumber(r.Answer)
		if !strict || strings.TrimSpace(r.Question) == "" {
			return nil, fmt.Errorf("suites: gsm8k test.jsonl line %d has no question or no #### answer", i+1)
		}
		rows[i] = bench.Row{ID: fmt.Sprintf("test/%0*d", w, i), Text: r.Question, Label: gold}
	}
	return rows, nil
}

// Prompt asks for worked steps with calculator notes, so the arithmetic check has
// something to recompute, and a final #### line, so the format check has a target.
func (gsm8k) Prompt(it bench.Item) (executor.Request, error) {
	q, err := it.Text()
	if err != nil {
		return executor.Request{}, err
	}
	zero := 0.0
	return executor.Request{
		System: "Solve the math word problem. Work step by step and write each calculation as " +
			"<<expression=result>>, for example <<3*4=12>>. End with a last line of the form #### <number>.",
		Prompt:      q,
		Temperature: &zero,
		MaxTokens:   gsm8kAnswerTokens,
	}, nil
}

// Grade compares the final number with the gold one: the #### line if there is
// one, else the last number. An answer with no number at all is a format fail.
func (gsm8k) Grade(_ context.Context, it bench.Item, answer string) (bench.Grade, error) {
	gold, err := it.Label()
	if err != nil {
		return bench.Grade{}, err
	}
	got, _ := finalNumber(answer)
	switch {
	case got == "":
		return bench.Grade{Format: true, Detail: "no number in the answer"}, nil
	case got != gold:
		return bench.Grade{Detail: fmt.Sprintf("answered %s, gold %s", got, gold)}, nil
	}
	return bench.Grade{Passed: true}, nil
}

// finalNumber is the answer's final number, normalised; strict reports that it came
// from a #### line rather than from the last number in the text.
func finalNumber(text string) (n string, strict bool) {
	if m := hashesRe.FindStringSubmatch(text); m != nil {
		return normNumber(m[1]), true
	}
	all := numberRe.FindAllString(text, -1)
	if len(all) == 0 {
		return "", false
	}
	return normNumber(all[len(all)-1]), false
}

// GSM8KFormatCheck passes an answer that ends in the asked-for #### line.
func GSM8KFormatCheck(answer string) bool {
	_, strict := finalNumber(answer)
	return strict
}

// GSM8KArithmeticCheck passes when every <<expression=result>> note recomputes.
// No notes is a fail: a check that read nothing must not count as a pass.
func GSM8KArithmeticCheck(answer string) bool {
	notes := calcNoteRe.FindAllStringSubmatch(answer, -1)
	if len(notes) == 0 {
		return false
	}
	for _, n := range notes {
		got, ok := evalArithmetic(n[1])
		want, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(n[2]), ",", ""), 64)
		if !ok || err != nil || math.Abs(got-want) > 1e-4*math.Max(1, math.Abs(want)) {
			return false
		}
	}
	return true
}

// normNumber reads a numberRe match without its commas; whole values print with
// no decimal point, so "1,000.00" and "1000" are one answer.
func normNumber(s string) string {
	s = strings.ReplaceAll(s, ",", "")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// evalArithmetic evaluates + - * / and brackets over number literals, in float64.
// "//" and "**" are refused, since Go would read them as a comment and a pointer.
func evalArithmetic(expr string) (float64, bool) {
	expr = strings.ReplaceAll(expr, ",", "")
	if strings.Contains(expr, "//") || strings.Contains(expr, "**") {
		return 0, false
	}
	e, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, false
	}
	return evalNode(e)
}

func evalNode(n ast.Expr) (float64, bool) {
	switch n := n.(type) {
	case *ast.BasicLit:
		if n.Kind != token.INT && n.Kind != token.FLOAT {
			return 0, false
		}
		v, err := strconv.ParseFloat(n.Value, 64)
		return v, err == nil
	case *ast.ParenExpr:
		return evalNode(n.X)
	case *ast.UnaryExpr:
		v, ok := evalNode(n.X)
		switch n.Op {
		case token.SUB:
			return -v, ok
		case token.ADD:
			return v, ok
		}
	case *ast.BinaryExpr:
		a, okA := evalNode(n.X)
		b, okB := evalNode(n.Y)
		if !okA || !okB {
			return 0, false
		}
		switch n.Op {
		case token.ADD:
			return a + b, true
		case token.SUB:
			return a - b, true
		case token.MUL:
			return a * b, true
		case token.QUO:
			return a / b, b != 0
		}
	}
	return 0, false
}
