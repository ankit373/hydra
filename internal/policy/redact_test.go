// SPDX-License-Identifier: MIT

package policy

import (
	"strings"
	"testing"
)

// The property that matters: whatever DetectPII would flag, Redact removes.
// Testing them against each other rather than against a fixed expected string
// is what stops the two drifting as detectors are added.
func TestRedact_LeavesNothingDetectPIIWouldStillFlag(t *testing.T) {
	samples := []string{
		"deploy with AKIAIOSFODNN7EXAMPLE today",
		"mail ops@example.com and finance@corp.co.uk",
		"ssn 123-45-6789 on file",
		"card 4111 1111 1111 1111 expires soon",
		"token: ghp_" + strings.Repeat("a", 36),
		"xoxb-1234567890-abcdefghij is the slack token",
		"sk-" + strings.Repeat("Z", 32),
		"AIza" + strings.Repeat("b", 35),
		"Authorization: Bearer abc.def.ghi",
		"password = \"hunter2correct\"",
		"connect to 203.0.113.42 for the api",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----",
	}
	for _, in := range samples {
		redacted, names := Redact(in)
		if len(names) == 0 {
			t.Errorf("Redact(%q) reported no detections; DetectPII says %v",
				in, DetectPII(Request{Prompt: in}))
			continue
		}
		if left := DetectPII(Request{Prompt: redacted}); len(left) > 0 {
			t.Errorf("after redacting %q the result %q still trips %v", in, redacted, left)
		}
	}
}

// A redactor that mangles ordinary source is worse than none: every payload
// would be corrupted to remove a risk that was not there.
func TestRedact_LeavesCleanTextByteForByte(t *testing.T) {
	clean := []string{
		"func add(a, b int) int { return a + b }",
		"SELECT id FROM accounts WHERE tenant = $1",
		"the quick brown fox jumps over the lazy dog",
		"",
	}
	for _, in := range clean {
		got, names := Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want it unchanged", in, got)
		}
		if names != nil {
			t.Errorf("Redact(%q) reported detections %v on clean text", in, names)
		}
	}
}

// The label is the point: "this contained an AWS key" is a finding worth
// keeping, and the key itself is the liability.
func TestRedact_LabelsWhatItReplaced(t *testing.T) {
	got, names := Redact("key AKIAIOSFODNN7EXAMPLE here")
	if !strings.Contains(got, "[REDACTED:aws access key id]") {
		t.Errorf("redacted text = %q, want a labelled placeholder", got)
	}
	if len(names) != 1 || names[0] != "aws access key id" {
		t.Errorf("names = %v, want [aws access key id]", names)
	}
	// The surrounding text has to survive, or the payload is useless.
	if !strings.HasPrefix(got, "key ") || !strings.HasSuffix(got, " here") {
		t.Errorf("redaction ate the surrounding text: %q", got)
	}
}

// Detectors overlap by design, an "Authorization: Bearer eyJ..." header is
// also a jwt. Emitting both placeholders would interleave them and corrupt the
// text, so the wider span has to win.
func TestRedact_OverlappingDetectorsProduceOneCleanPlaceholder(t *testing.T) {
	in := "Authorization: Bearer eyJhbGciOi.eyJzdWIiOi.SflKxwRJSM"
	got, names := Redact(in)

	if strings.Count(got, "[REDACTED:") != 1 {
		t.Errorf("expected exactly one placeholder, got %q", got)
	}
	if strings.Contains(got, "eyJhbGciOi") {
		t.Errorf("part of the token survived: %q", got)
	}
	if len(names) < 2 {
		t.Errorf("names = %v, want both overlapping detectors reported", names)
	}
	if left := DetectPII(Request{Prompt: got}); len(left) > 0 {
		t.Errorf("redacted header still trips %v: %q", left, got)
	}
}

func TestRedact_HandlesSeveralSecretsInOneString(t *testing.T) {
	in := "mail a@b.com, key AKIAIOSFODNN7EXAMPLE, ssn 123-45-6789, done"
	got, names := Redact(in)

	if n := strings.Count(got, "[REDACTED:"); n != 3 {
		t.Errorf("got %d placeholders in %q, want 3", n, got)
	}
	if len(names) != 3 {
		t.Errorf("names = %v, want three detectors", names)
	}
	if !strings.HasSuffix(got, ", done") {
		t.Errorf("text after the last secret was lost: %q", got)
	}
}

// The same detector firing twice must replace both hits, not just the first.
func TestRedact_ReplacesEveryHitOfOneDetector(t *testing.T) {
	got, _ := Redact("mail a@b.com and also c@d.org please")
	if strings.Contains(got, "a@b.com") || strings.Contains(got, "c@d.org") {
		t.Errorf("a repeated detector left a hit behind: %q", got)
	}
	if n := strings.Count(got, "[REDACTED:email]"); n != 2 {
		t.Errorf("got %d email placeholders in %q, want 2", n, got)
	}
}

// Redact and ContainsPII must agree, since one gates routing and the other
// decides what is safe to write down.
func TestRedact_AgreesWithContainsPII(t *testing.T) {
	for _, in := range []string{
		"ordinary text with no secrets",
		"order 123456789 shipped",
		"mail ops@example.com",
		"version 1.2.3.4 released",
	} {
		_, names := Redact(in)
		if got, want := len(names) > 0, ContainsPII(Request{Prompt: in}); got != want {
			t.Errorf("Redact(%q) detected=%v but ContainsPII=%v", in, got, want)
		}
	}
}

// Redact then Restore gives back the original, for the samples above and for
// every line of the secret fixtures (#1880).
func TestRedactReversible_RoundTrip(t *testing.T) {
	texts := []string{
		"mail a@b.com, key AKIAIOSFODNN7EXAMPLE, ssn 123-45-6789, done",
		"Authorization: Bearer eyJhbGciOi.eyJzdWIiOi.SflKxwRJSM",
		"no secrets here at all",
	}
	for _, fx := range loadSecretFixtures(t) {
		texts = append(texts, fx.text)
	}
	for i, in := range texts {
		v := NewVault()
		red := RedactReversible(in, v)
		if red == in && len(DetectPII(Request{Prompt: in})) > 0 {
			t.Errorf("text %d: nothing replaced", i)
		}
		got, unknown := v.Restore(red)
		if got != in {
			t.Errorf("text %d: round trip changed the text", i)
		}
		if len(unknown) > 0 {
			t.Errorf("text %d: restore reported %v", i, unknown)
		}
	}
}

func TestRedactReversible_SameValueSamePlaceholder(t *testing.T) {
	v := NewVault()
	first := RedactReversible("write to a@b.com and c@d.org", v)
	second := RedactReversible("did a@b.com reply?", v)
	if first != "write to <<EMAIL_1>> and <<EMAIL_2>>" {
		t.Errorf("first = %q", first)
	}
	if second != "did <<EMAIL_1>> reply?" {
		t.Errorf("second turn = %q, want the same placeholder for the same value", second)
	}
	if v.Len() != 2 {
		t.Errorf("vault holds %d values, want 2", v.Len())
	}
}

func TestRestore_ReportsUnknownPlaceholder(t *testing.T) {
	v := NewVault()
	RedactReversible("mail a@b.com", v)
	got, unknown := v.Restore("sent to <<EMAIL_1>> and <<EMAIL_7>>, see FILE_2")
	if got != "sent to a@b.com and <<EMAIL_7>>, see FILE_2" {
		t.Errorf("restored = %q", got)
	}
	if len(unknown) != 1 || unknown[0] != "<<EMAIL_7>>" {
		t.Errorf("unknown = %v, want [<<EMAIL_7>>]; a bare word is not reported", unknown)
	}
}

func TestRestore_ToleratesCaseChange(t *testing.T) {
	v := NewVault()
	RedactReversible("mail a@b.com", v)
	for _, answer := range []string{"<<email_1>>", "<< EMAIL_1 >>", "<EMAIL_1>", "EMAIL_1", "Email_1"} {
		if got, unknown := v.Restore(answer); got != "a@b.com" || len(unknown) > 0 {
			t.Errorf("Restore(%q) = %q, %v", answer, got, unknown)
		}
	}
}

func TestRedactReversible_SecondDetectionPassFindsNothing(t *testing.T) {
	for i, fx := range loadSecretFixtures(t) {
		red := RedactReversible(fx.text, NewVault())
		if left := DetectPII(Request{Prompt: red}); len(left) > 0 {
			t.Errorf("fixture %d: placeholders still trip %v", i+1, left)
		}
	}
}

// A key that the text already uses is skipped, so the user's own EMAIL_1 is
// never overwritten by Restore.
func TestRedactReversible_SkipsKeysTheTextAlreadyUses(t *testing.T) {
	v := NewVault()
	in := "column EMAIL_1 holds a@b.com"
	red := RedactReversible(in, v)
	if red != "column EMAIL_1 holds <<EMAIL_2>>" {
		t.Errorf("redacted = %q", red)
	}
	if got, _ := v.Restore(red); got != in {
		t.Errorf("round trip = %q", got)
	}
}

// A placeholder split across pieces comes out whole and restored; text that
// cannot be one passes straight through.
func TestStreamRestorer_JoinsASplitPlaceholder(t *testing.T) {
	v := NewVault()
	RedactReversible("mail a@b.com", v)
	rs := v.Stream()
	var out strings.Builder
	for _, piece := range []string{"Sent to <<EMA", "IL_1", ">>, then <<EMAIL_9>> and", " done."} {
		out.WriteString(rs.Push(piece))
	}
	out.WriteString(rs.Flush())
	if want := "Sent to a@b.com, then <<EMAIL_9>> and done."; out.String() != want {
		t.Errorf("streamed %q, want %q", out.String(), want)
	}
	if rs.Flush() != "" {
		t.Error("a second Flush returned text")
	}
}

// An empty or nil vault has nothing to restore, so nothing is held back.
func TestStreamRestorer_EmptyVaultHoldsNothing(t *testing.T) {
	for _, v := range []*Vault{nil, NewVault()} {
		if got := v.Stream().Push("<<EMA"); got != "<<EMA" {
			t.Errorf("Push = %q, want it passed through", got)
		}
	}
	if (*Vault)(nil).Len() != 0 {
		t.Error("a nil vault reports values")
	}
}

// A run longer than any placeholder is released rather than held.
func TestStreamRestorer_ReleasesALongRun(t *testing.T) {
	v := NewVault()
	RedactReversible("mail a@b.com", v)
	rs := v.Stream()
	long := strings.Repeat("WORD_", 10) + "1"
	got := rs.Push(long + " ")
	if !strings.HasPrefix(long+" ", got) || len(got) < len(long)-len("<< EMAIL_1 >>") {
		t.Errorf("Push released %q of %q", got, long)
	}
}

// Values a pattern cannot find are replaced once added, and a value that
// overlaps a pattern hit is left to the pattern.
func TestVault_AddValuesRedactsWhatNoPatternFinds(t *testing.T) {
	v := NewVault()
	v.AddValues("personal", []string{"Aisha Bello", "aisha@example.com"})
	in := "Offer for Aisha Bello, mail aisha@example.com. Aisha Bello starts Monday."
	got := RedactReversible(in, v)
	if want := "Offer for <<PERSONAL_1>>, mail <<EMAIL_1>>. <<PERSONAL_1>> starts Monday."; got != want {
		t.Errorf("redacted = %q, want %q", got, want)
	}
	if back, _ := v.Restore(got); back != in {
		t.Errorf("round trip = %q", back)
	}
}
