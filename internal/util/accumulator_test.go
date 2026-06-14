package util

import (
	"fmt"
	"strings"
	"sync"
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

func TestAccumulator_ConcurrentWrites(t *testing.T) {
	// Goroutine-safety: 50 goroutines each writing 100 bytes concurrently.
	// Must not race (run with -race), total must equal 50*100 = 5000.
	a := NewAccumulator(1 << 20) // 1 MB — large enough to not truncate
	var wg sync.WaitGroup
	for i := range 50 {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.Write([]byte(fmt.Sprintf("%0100d", i)))
		}()
	}
	wg.Wait()
	if a.TotalBytes() != 50*100 {
		t.Fatalf("TotalBytes %d, want %d", a.TotalBytes(), 50*100)
	}
	if a.Truncated() {
		t.Fatal("should not truncate")
	}
}

func TestAccumulator_ConcurrentTruncation(t *testing.T) {
	// Many goroutines writing into a tiny cap — only one must set the marker.
	a := NewAccumulator(10)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.Write([]byte("AAAAAAAAAA")) // 10 bytes each
		}()
	}
	wg.Wait()
	s := a.String()
	// Marker must appear exactly once.
	if count := strings.Count(s, truncationMarker); count != 1 {
		t.Fatalf("truncation marker appears %d times, want 1", count)
	}
	if a.TotalBytes() != 20*10 {
		t.Fatalf("TotalBytes %d, want 200", a.TotalBytes())
	}
}

func TestAccumulator_EnvOverride(t *testing.T) {
	t.Setenv("HYDRA_MAX_OUTPUT_BYTES", "20")
	a := NewAccumulator(0) // env should override
	_, _ = a.Write([]byte(strings.Repeat("X", 25)))
	if !a.Truncated() {
		t.Fatal("expected truncation from env override")
	}
	// Should have stored exactly 20 bytes + marker, not DefaultMaxBytes.
	if a.TotalBytes() != 25 {
		t.Fatalf("TotalBytes %d, want 25", a.TotalBytes())
	}
}

func TestAccumulator_EmptyWrite(t *testing.T) {
	a := NewAccumulator(10)
	n, err := a.Write([]byte{})
	if err != nil || n != 0 {
		t.Fatalf("empty write: n=%d err=%v", n, err)
	}
	if a.Truncated() || a.Len() != 0 {
		t.Fatal("empty write should not change state")
	}
}
