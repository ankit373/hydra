// SPDX-License-Identifier: MIT

package serve

import (
	"net/http"
	"strings"
	"testing"
)

func TestChat_ConversationHeader(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "ok"}}
	w := postWith(t, s, http.Header{ConversationHeader: {" chat-42 "}}, oneTurn)
	if w.Code != http.StatusOK || s.got.Conversation != "chat-42" {
		t.Fatalf("status %d, conversation %q, want 200 and chat-42", w.Code, s.got.Conversation)
	}
	long := strings.Repeat("x", maxConversationKey+1)
	if w := postWith(t, &stubRouter{}, http.Header{ConversationHeader: {long}}, oneTurn); w.Code != http.StatusBadRequest {
		t.Fatalf("an oversized key got %d, want 400", w.Code)
	}
}
