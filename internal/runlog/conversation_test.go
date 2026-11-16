// SPDX-License-Identifier: MIT

package runlog

import (
	"slices"
	"testing"
)

func TestConversation_ListsRunsInOrder(t *testing.T) {
	tempHome(t)
	runs := []struct{ id, conv string }{
		{"20261115T100000Z-aaa", "chat-a"},
		{"20261115T110000Z-bbb", "chat-b"},
		{"20261115T120000Z-ccc", "chat-a"},
		{"20261115T130000Z-ddd", ""},
		{"20261115T140000Z-eee", "chat-a"},
	}
	for _, r := range runs {
		e := Event{Kind: KindTaskStarted, TaskID: "t"}
		if r.conv != "" {
			e.Meta = map[string]any{MetaConversation: r.conv, "messages": 1}
		}
		if err := New(r.id).Append(e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Conversation("chat-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20261115T100000Z-aaa", "20261115T120000Z-ccc", "20261115T140000Z-eee"}
	if !slices.Equal(got, want) {
		t.Fatalf("Conversation(chat-a) = %v, want %v, oldest first", got, want)
	}
	if got, _ := Conversation(""); got != nil {
		t.Fatalf("an empty key matched %v, want nothing", got)
	}
}
