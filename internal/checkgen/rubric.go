// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

// minrubricItems is the fewest items worth a rubric; fewer leaves the reply not checked.
const minrubricItems = 3

const rubricPrompt = "The request below asks for a reply or a piece of writing. List 5 to 10 yes/no requirements " +
	`that the request itself states, as a JSON array of {"text": string, "type": string, "phrase": string, "limit": number}. ` +
	`Type is "max_words" for a stated length cap (limit is the number of words), "must_mention" or "must_not_say" ` +
	`for a phrase the request quotes or names (phrase copied exactly from the request), or "judge" for anything else, ` +
	"with text a statement the reply must make true. Add nothing the request does not ask for.\n\n"

const rubricJudgeAsk = "Is the statement below true of the answer that follows? Judge only this statement.\n"

// rubricItem is one yes/no requirement. Code items carry a phrase or a limit; judge items only text.
type rubricItem struct {
	Text   string `json:"text"`
	Type   string `json:"type"`
	Phrase string `json:"phrase,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

var quotedPhrase = regexp.MustCompile(`"([^"]+)"|“([^”]+)”`)

// rubricGen asks the writer for the request's requirements. Items a program can
// decide become variant code; the rest become variant judge, one question each.
type rubricGen struct{}

func init() {
	if err := Register("writing", rubricGen{}); err != nil {
		panic(err)
	}
}

func (rubricGen) Kind() string { return "rubric" }

// Generate drops every item the request does not back, so the writer cannot add demands nobody made.
func (rubricGen) Generate(ctx context.Context, task Task) ([]Spec, error) {
	if task.Ask == nil {
		return nil, errors.New("checkgen rubric: no writer head to ask")
	}
	reply, err := task.Ask(ctx, rubricPrompt+util.WrapUntrusted("REQUEST", task.Text))
	if err != nil {
		return nil, err
	}
	var listed []rubricItem
	start, end := strings.Index(reply, "["), strings.LastIndex(reply, "]")
	if start < 0 || end < start || json.Unmarshal([]byte(reply[start:end+1]), &listed) != nil {
		return nil, fmt.Errorf("checkgen rubric: writer did not reply with a JSON list: %.80q", reply)
	}
	request := fold(task.Text)
	byVariant := map[string][]rubricItem{}
	kept := 0
	for _, it := range listed {
		v, ok := backed(it, request)
		if !ok {
			continue
		}
		byVariant[v] = append(byVariant[v], it)
		kept++
	}
	if kept < minrubricItems {
		return nil, nil
	}
	var out []Spec
	for _, v := range []string{"code", "judge"} {
		if len(byVariant[v]) == 0 {
			continue
		}
		s, err := NewSpec("rubric", v, byVariant[v], task, task.Writer)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// backed says which variant an item is and whether the folded request backs it:
// its phrase, its word limit, or every phrase its text quotes must appear there.
func backed(it rubricItem, request string) (variant string, ok bool) {
	switch it.Type {
	case "max_words":
		return "code", it.Limit > 0 && containsWord(request, strconv.Itoa(it.Limit))
	case "must_mention", "must_not_say":
		p := fold(it.Phrase)
		return "code", p != "" && strings.Contains(request, p)
	case "judge":
		if strings.TrimSpace(it.Text) == "" {
			return "", false
		}
		for _, m := range quotedPhrase.FindAllStringSubmatch(it.Text, -1) {
			if !strings.Contains(request, fold(m[1]+m[2])) {
				return "", false
			}
		}
		return "judge", true
	}
	return "", false
}

func (rubricGen) Oracle(s Spec, env Env) (oracle.Oracle, error) {
	var items []rubricItem
	if err := json.Unmarshal(s.Body, &items); err != nil {
		return nil, fmt.Errorf("checkgen rubric: spec %s: %w", s.ID, err)
	}
	switch s.Variant {
	case "code":
		return rubricOracle{items: items, check: codeItem}, nil
	case "judge":
		d, err := env.decider(s.Generator)
		if err != nil {
			return nil, err
		}
		return rubricOracle{items: items, check: func(ctx context.Context, it rubricItem, answer string) (bool, error) {
			p := &oracle.PredicateOracle{
				Template: rubricJudgeAsk + util.WrapUntrusted("STATEMENT", it.Text) + "\n{answer}",
				Decider:  d, Source: s.Check(),
			}
			v, err := p.Verify(ctx, answer, trust.Task{})
			return v.Passed, err
		}}, nil
	}
	return nil, fmt.Errorf("checkgen rubric: spec %s has unknown variant %q", s.ID, s.Variant)
}

func codeItem(_ context.Context, it rubricItem, answer string) (bool, error) {
	switch it.Type {
	case "max_words":
		return len(strings.Fields(answer)) <= it.Limit, nil
	case "must_mention":
		return strings.Contains(fold(answer), fold(it.Phrase)), nil
	case "must_not_say":
		return !strings.Contains(fold(answer), fold(it.Phrase)), nil
	}
	return false, fmt.Errorf("checkgen rubric: %q is not a code item", it.Type)
}

type rubricOracle struct {
	items []rubricItem
	check func(ctx context.Context, it rubricItem, answer string) (bool, error)
}

// Verify passes when every item passes. One item that could not be judged leaves the answer not checked.
func (o rubricOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	var failed []string
	for i, it := range o.items {
		ok, err := o.check(ctx, it, candidate)
		if err != nil {
			return oracle.Verdict{}, fmt.Errorf("item %d: %w", i+1, err)
		}
		if !ok {
			failed = append(failed, fmt.Sprintf("%d %q", i+1, it.Text))
		}
	}
	if len(failed) > 0 {
		return oracle.Verdict{Detail: "items failed: " + strings.Join(failed, "; ")}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("all %d items passed", len(o.items))}, nil
}
