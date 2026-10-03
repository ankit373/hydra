// SPDX-License-Identifier: MIT

package signals

import (
	"strings"
	"testing"
)

func TestDominantScript(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"english", "rotate the signing key", ScriptLatin},
		{"russian", "повернуть ключ подписи", ScriptCyrillic},
		{"japanese kana wins over han", "署名キーをローテーションする", ScriptKana},
		{"korean", "서명 키를 교체하십시오", ScriptHangul},
		{"chinese han only", "轮换签名密钥", ScriptHan},
		{"arabic", "تدوير مفتاح التوقيع", ScriptArabic},
		{"greek", "περιστροφή κλειδιού", ScriptGreek},
		{"hebrew", "סיבוב מפתח החתימה", ScriptHebrew},
		{"digits and punctuation are not letters", "1234 !@#$ 5678", ""},
		{"empty", "", ""},
		{"mixed, majority wins", "rotate the signing key 密钥", ScriptLatin},
	} {
		if got := DominantScript(tc.in); got != tc.want {
			t.Errorf("%s: DominantScript(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// A tie is decided by name, not by map order. Without the tie-break the same
// prompt scripts two ways across runs, which is the defect class that has hit
// this repo before: a map read with no total order.
func TestDominantScript_TieIsStableAcrossRuns(t *testing.T) {
	tied := "abc абв" // 3 Latin letters, 3 Cyrillic
	first := DominantScript(tied)
	for i := 0; i < 200; i++ {
		if got := DominantScript(tied); got != first {
			t.Fatalf("run %d gave %q, run 0 gave %q: the tie-break is not total", i, got, first)
		}
	}
	if first != ScriptCyrillic {
		t.Errorf("tie resolved to %q, want %q (lowest name wins)", first, ScriptCyrillic)
	}
}

func TestLanguageCode_OnlyWhereTheScriptDecides(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"kana means japanese", "署名キーをローテーション", "ja"},
		{"hangul means korean", "서명 키를 교체", "ko"},
		{"han alone means chinese", "轮换签名密钥", "zh"},
		{"english is not guessed", "rotate the signing key", ""},
		{"french is not guessed", "faire tourner la cle de signature", ""},
		{"cyrillic is not guessed", "повернуть ключ", ""},
		{"empty", "", ""},
	} {
		if got := LanguageCode(tc.in); got != tc.want {
			t.Errorf("%s: LanguageCode(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want Structure
	}{
		{"plain prose", "rotate the signing key", Structure{}},
		{"one question", "is this safe?", Structure{Questions: 1}},
		{"a run of marks is one question", "really???", Structure{Questions: 1}},
		{"two questions", "is it safe? should I ship?", Structure{Questions: 2}},
		{"bullets", "- one\n- two\n* three", Structure{ListItems: 3}},
		{
			"numbered list is ordered steps",
			"1. fetch\n2. verify\n3. apply",
			Structure{ListItems: 3, OrderedSteps: true},
		},
		{"a single numbered item is not a workflow", "1. fetch", Structure{ListItems: 1}},
		{"paren separator counts", "1) fetch\n2) verify", Structure{ListItems: 2, OrderedSteps: true}},
		{"a year is not a step", "2026 was the year", Structure{}},
		{"closed fence is one block", "```go\nx := 1\n```", Structure{CodeFences: 1}},
		{"unterminated fence is no block", "```go\nx := 1", Structure{}},
		{"two blocks", "```\na\n```\ntext\n```\nb\n```", Structure{CodeFences: 2}},
	} {
		if got := Shape(tc.in); got != tc.want {
			t.Errorf("%s: Shape(%q) = %+v, want %+v", tc.name, tc.in, got, tc.want)
		}
	}
}

// Every new signal must be in the schema, or a rule naming it fails to load
// and the signal is unreachable however well Collect fills it (#903).
func TestSchema_CarriesEveryHeuristicSignal(t *testing.T) {
	sc := SchemaFor(nil)
	want := map[string]Kind{
		SigLanguageScript:        KindString,
		SigLanguageCode:          KindString,
		SigStructureQuestions:    KindNumber,
		SigStructureListItems:    KindNumber,
		SigStructureCodeFences:   KindNumber,
		SigStructureOrderedSteps: KindBool,
		SigConversationTurns:     KindNumber,
		SigConversationToolLoop:  KindBool,
		SigContextPromptTokens:   KindNumber,
	}
	for name, kind := range want {
		got, ok := sc[name]
		if !ok {
			t.Errorf("%s is missing from the schema, so no rule can name it", name)
			continue
		}
		if got != kind {
			t.Errorf("%s is %v in the schema, want %v", name, got, kind)
		}
	}
}

// A count of zero is a reading and must be present; an unknown is not and must
// be absent. Conflating them is #1021, where an absent number read as 0 and
// fired every `< n` rule on every dispatch.
func TestCollect_StructureIsAlwaysPresentAndConversationIsNot(t *testing.T) {
	v := Collect(Input{Prompt: "rotate the signing key"}, nil)

	for _, name := range []string{
		SigStructureQuestions, SigStructureListItems,
		SigStructureCodeFences, SigStructureOrderedSteps,
	} {
		if _, ok := v[name]; !ok {
			t.Errorf("%s absent on a prose prompt; a count of zero is a reading, not an absence", name)
		}
	}
	for _, name := range []string{SigConversationTurns, SigConversationToolLoop} {
		if got, ok := v[name]; ok {
			t.Errorf("%s = %v with no conversation supplied, want absent: a single-shot "+
				"dispatch is not a conversation of length zero", name, got)
		}
	}
}

func TestCollect_LanguageAbsentWhenThePromptHasNoLetters(t *testing.T) {
	v := Collect(Input{Prompt: "1234 !@#$ 5678"}, nil)
	if got, ok := v[SigLanguageScript]; ok {
		t.Errorf("%s = %v for a prompt with no letters, want absent", SigLanguageScript, got)
	}
	if got, ok := v[SigLanguageCode]; ok {
		t.Errorf("%s = %v for a prompt with no letters, want absent", SigLanguageCode, got)
	}
}

func TestCollect_LanguageCodeAbsentForLatinButScriptPresent(t *testing.T) {
	v := Collect(Input{Prompt: "rotate the signing key"}, nil)
	if v[SigLanguageScript] != ScriptLatin {
		t.Errorf("%s = %v, want %q", SigLanguageScript, v[SigLanguageScript], ScriptLatin)
	}
	if got, ok := v[SigLanguageCode]; ok {
		t.Errorf("%s = %v for English, want absent: the script does not decide the language",
			SigLanguageCode, got)
	}
}

func TestCollect_ConversationAndTokensWhenSupplied(t *testing.T) {
	turns, loop, tokens := 3, true, 42
	v := Collect(Input{
		Prompt: "hello", Turns: &turns, ToolLoop: &loop, PromptTokens: &tokens,
	}, nil)
	if v[SigConversationTurns] != float64(3) {
		t.Errorf("%s = %v, want 3", SigConversationTurns, v[SigConversationTurns])
	}
	if v[SigConversationToolLoop] != true {
		t.Errorf("%s = %v, want true", SigConversationToolLoop, v[SigConversationToolLoop])
	}
	if v[SigContextPromptTokens] != float64(42) {
		t.Errorf("%s = %v, want 42", SigContextPromptTokens, v[SigContextPromptTokens])
	}
}

// The absence contract, end to end through the expression language: with no
// conversation, a `< 1` comparison must match nothing. Before #1021 an absent
// number read as zero, so this rule fired on every single-shot dispatch.
func TestRule_AbsentConversationMatchesNoComparison(t *testing.T) {
	src := `
version: 1
rules:
  - name: short-conversation
    priority: 10
    when: conversation.turns < 1
    action: {type: block, reason: "no"}
`
	eng, err := Parse([]byte(src), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d := eng.Evaluate(Input{Prompt: "rotate the signing key"}); d.Fired() {
		t.Fatalf("a rule on conversation.turns fired with no conversation: %+v", d)
	}
	turns := 0
	if d := eng.Evaluate(Input{Prompt: "x", Turns: &turns}); !d.Fired() {
		t.Fatal("conversation.turns < 1 did not fire for a real reading of zero turns")
	}
}

// A rule may name every new signal, which is the whole point of adding them.
func TestRule_EveryNewSignalIsNameableInARule(t *testing.T) {
	src := `
version: 1
rules:
  - name: uses-everything
    priority: 10
    when: >
      language.script == "han" && language.code == "zh"
      && structure.questions > 0 && structure.list_items >= 0
      && structure.code_fences == 0 && !structure.ordered_steps
      && conversation.turns > 1 && conversation.tool_loop
      && context.prompt_tokens > 10
    action: {type: route, tier: "8"}
`
	if _, err := Parse([]byte(src), nil); err != nil {
		t.Fatalf("a rule naming the new signals did not load: %v", err)
	}
}

// The load-time typo guard still works for the new namespaces (#903).
func TestRule_TypoInANewSignalStillFailsAtLoad(t *testing.T) {
	src := `
version: 1
rules:
  - name: typo
    priority: 10
    when: language.scrpt == "latin"
    action: {type: route, tier: "8"}
`
	_, err := Parse([]byte(src), nil)
	if err == nil {
		t.Fatal("language.scrpt loaded; a misspelled signal must fail at load, not match nothing forever")
	}
	if !strings.Contains(err.Error(), "language.scrpt") {
		t.Errorf("error does not name the bad signal: %v", err)
	}
}
