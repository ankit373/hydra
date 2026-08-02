// SPDX-License-Identifier: MIT

package api

import (
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
)

// The picker must not offer a routing key the router does not understand.
// EnumToTier is the authority; anything absent from it silently falls through
// to default routing while the UI claims otherwise.
func TestChatEnums_EveryKeyResolvesToATier(t *testing.T) {
	for _, enum := range New().ChatEnums() {
		if tier := dispatch.EnumToTier(enum); tier == "" {
			t.Errorf("ChatEnums offers %q, which EnumToTier does not resolve", enum)
		}
	}
}

// "auto" is the absence of an enum, not a member of the list — an empty string
// in a picker renders as a blank row.
func TestChatEnums_ContainsNoEmptyEntry(t *testing.T) {
	for _, enum := range New().ChatEnums() {
		if enum == "" {
			t.Error("ChatEnums contains an empty key; auto-routing is the absence of one")
		}
	}
}

// Ordering is what the picker shows. Weakest head first means the cheap option
// is the default reading direction, which is the whole premise of the router.
func TestChatEnums_OrderedWeakestFirst(t *testing.T) {
	enums := New().ChatEnums()
	prev := 0
	for i, enum := range enums {
		tier := dispatch.EnumToTier(enum)
		n := atoiTier(t, tier)
		if i > 0 && n > prev {
			t.Errorf("%q is tier %d after tier %d — the list must run weakest (highest tier) first",
				enum, n, prev)
		}
		prev = n
	}
}

func atoiTier(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("EnumToTier returned %q, which is not a tier number", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// An empty prompt is a user slip, not an exception. It comes back as a reply
// carrying the reason so the dock renders it as a message.
func TestChat_EmptyPromptIsAReplyNotAnError(t *testing.T) {
	sandbox(t)

	r, err := New().Chat("", "")
	if err != nil {
		t.Fatalf("an empty prompt must not error: %v", err)
	}
	if r.Error == "" {
		t.Error("no Error on the reply; the dock would render an empty message")
	}
	if r.Output != "" {
		t.Error("an empty prompt produced output")
	}
}

// A dispatch that cannot run is a normal outcome — no heads configured is the
// common first-launch case. It must not blank the window.
func TestChat_FailedDispatchReturnsAReply(t *testing.T) {
	sandbox(t)

	r, err := New().Chat("hello", "SIMPLE")
	if err != nil {
		t.Fatalf("a failed dispatch must come back as a reply, not a Go error: %v", err)
	}
	if r.RunID == "" {
		t.Error("no RunID; a failed chat still has a run the user can inspect")
	}
}
