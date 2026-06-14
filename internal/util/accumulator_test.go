package util

import (
	"strings"
	"testing"
)

func TestAccumulator_Normal(t *testing.T) {
	a := NewAccumulator(100)
	n, err := a.Write([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 11 {
		t.Fatalf("Write returned %d, want 11", n)
	}
	if a.String() != "hello world" {
		t.Fatalf("got %q", a.String())
	}
	if a.Truncated() {
		t.Fatal("should not be truncated")
	}
	if a.TotalBytes() != 11 {
		t.Fatalf("TotalBytes %d, want 11", a.TotalBytes())
	}
}

func TestAccumulator_Truncation(t *testing.T) {
	a := NewAccumulator(10)

	// Write 8 bytes — fits.
	_, _ = a.Write([]byte("12345678"))
	// Write 6 more — should truncate after 2.
	_, _ = a.Write([]byte("AAAAAA"))

	if !a.Truncated() {
		t.Fatal("expected truncated=true")
	}
	if a.TotalBytes() != 14 {
		t.Fatalf("TotalBytes %d, want 14", a.TotalBytes())
	}
	s := a.String()
	if !strings.HasSuffix(s, truncationMarker) {
		t.Fatalf("missing truncation marker, got %q", s)
	}
	// Stored bytes = 10 cap + len(truncationMarker).
	want := "12345678AA" + truncationMarker
	if s != want {
		t.Fatalf("got %q\nwant %q", s, want)
	}
}

func TestAccumulator_ExactFill(t *testing.T) {
	a := NewAccumulator(5)
	_, _ = a.Write([]byte("12345"))
	// Exactly at cap — no truncation yet.
	if a.Truncated() {
		t.Fatal("should not truncate at exact cap")
	}
	// One more byte triggers truncation.
	_, _ = a.Write([]byte("X"))
	if !a.Truncated() {
		t.Fatal("expected truncated after exceeding cap")
	}
}

func TestAccumulator_WritesAfterTruncation(t *testing.T) {
	a := NewAccumulator(5)
	_, _ = a.Write([]byte("123456789"))
	total1 := a.TotalBytes()
	_, _ = a.Write([]byte("more"))
	// Buffer content unchanged after first truncation.
	if a.TotalBytes() != total1+4 {
		t.Fatalf("TotalBytes should keep counting, got %d", a.TotalBytes())
	}
}

func TestAccumulator_Reset(t *testing.T) {
	a := NewAccumulator(5)
	_, _ = a.Write([]byte("123456789"))
	a.Reset()
	if a.Truncated() || a.Len() != 0 || a.TotalBytes() != 0 {
		t.Fatal("Reset did not clear state")
	}
}

func TestAccumulator_ImplementsWriter(t *testing.T) {
	a := NewAccumulator(0) // default max
	_, err := strings.NewReader("test").WriteTo(a)
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != "test" {
		t.Fatalf("got %q", a.String())
	}
}
