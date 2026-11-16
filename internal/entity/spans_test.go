// SPDX-License-Identifier: MIT

package entity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func answer(s string) Ask {
	return func(context.Context, string, string) (string, error) { return s, nil }
}

func TestSpans_KeepsOnlyTextFoundVerbatim(t *testing.T) {
	text := "Offer for Aisha Bello at 14 Elm Row."
	got, err := Spans(context.Background(), text, answer("Here: [\"Aisha Bello\", \"14 Elm Row\", \"Bob Smith\"]"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Aisha Bello", "14 Elm Row"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Spans = %v, want %v (an invented name is dropped)", got, want)
	}
}

func TestSpans_UnreadableIsAnError(t *testing.T) {
	if _, err := Spans(context.Background(), "Aisha Bello", answer("there is a name")); !errors.Is(err, ErrUnreadableSpans) {
		t.Errorf("err = %v, want ErrUnreadableSpans", err)
	}
}

func TestReports_SaveKeepsOtherHeads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ner.json")
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	ok := Report{Positives: 200, Recalled: 190, Negatives: 200}
	if err := SaveReport(path, "a", ok, at); err != nil {
		t.Fatal(err)
	}
	if err := SaveReport(path, "b", Report{}, at); err != nil {
		t.Fatal(err)
	}
	all, err := LoadReports(path)
	if err != nil {
		t.Fatal(err)
	}
	if !all["a"].Report.Eligible() || all["b"].Report.Eligible() || len(all) != 2 {
		t.Errorf("reports = %+v", all)
	}
}

func TestSpans_NoHeadAndEmptyText(t *testing.T) {
	if _, err := Spans(context.Background(), "Aisha Bello", nil); !errors.Is(err, ErrNoHead) {
		t.Errorf("nil ask: err = %v, want ErrNoHead", err)
	}
	got, err := Spans(context.Background(), "  ", answer("[\"x\"]"))
	if err != nil || got != nil {
		t.Errorf("empty text: %v, %v, want nothing asked", got, err)
	}
	failing := func(context.Context, string, string) (string, error) { return "", errors.New("down") }
	if _, err := Spans(context.Background(), "Aisha Bello", failing); err == nil {
		t.Error("a failed ask read as no spans")
	}
}

// A report file that will not parse is an error, never "no measurement".
func TestReports_CorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ner.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReports(path); err == nil {
		t.Error("a corrupt report file loaded")
	}
	if err := SaveReport(path, "a", Report{}, time.Now()); err == nil {
		t.Error("SaveReport overwrote a corrupt file it could not read")
	}
}
