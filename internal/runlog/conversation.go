// SPDX-License-Identifier: MIT

package runlog

import "slices"

// MetaConversation is the task_started Meta key a served request's
// conversation is recorded under.
const MetaConversation = "conversation"

// Conversation lists the runs whose task_started carries conv as its
// conversation key, newest last. A full scan, as Runs is; no index yet.
func Conversation(conv string) ([]string, error) {
	if conv == "" {
		return nil, nil
	}
	ids, err := Runs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		events, err := Load(id)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(events, func(e Event) bool {
			return e.Kind == KindTaskStarted && e.Meta[MetaConversation] == conv
		}) {
			out = append(out, id)
		}
	}
	slices.Reverse(out)
	return out, nil
}
