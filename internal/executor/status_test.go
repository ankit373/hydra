// SPDX-License-Identifier: MIT

package executor

import (
	"net/http"
	"testing"
	"time"
)

// RFC 9110 allows both forms and real providers send both.
func TestParseRetryAfter_BothFormsTheSpecAllows(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		want         time.Duration
	}{
		{"delta seconds", "2", 2 * time.Second},
		{"http date", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 90 * time.Second},
		{"absent", "", 0},
		{"unparseable", "soon", 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"a date already past", now.Add(-time.Hour).UTC().Format(http.TimeFormat), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.header, now); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

// A 429 is the head saying it works and is busy. Anything else is not, however
// long it asks us to wait, because the wait is only actionable when we know
// the head is otherwise fine.
func TestStatusError_OnlyA429ReportsARateLimit(t *testing.T) {
	for _, tc := range []struct {
		code int
		want bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, false},
		{http.StatusNotFound, false},
		{http.StatusBadRequest, false},
	} {
		e := &StatusError{HeadID: "h", Code: tc.code, RetryAfter: time.Second}
		if _, ok := e.RateLimitRetryAfter(); ok != tc.want {
			t.Errorf("status %d reported rate-limited = %v, want %v", tc.code, ok, tc.want)
		}
	}
}

// A 429 that names no time is still a busy head, which is the part that
// decides the classification. The zero says nobody named a time.
func TestStatusError_A429WithNoHeaderIsStillARateLimit(t *testing.T) {
	e := &StatusError{HeadID: "h", Code: http.StatusTooManyRequests}
	d, ok := e.RateLimitRetryAfter()
	if !ok {
		t.Fatal("a 429 with no Retry-After must still read as a rate limit")
	}
	if d != 0 {
		t.Errorf("stated wait = %v, want 0 when the server named none", d)
	}
}

// Plenty of code reads the message rather than the type, so typing the error
// must not reword it.
func TestStatusError_MessageIsUnchanged(t *testing.T) {
	e := &StatusError{HeadID: "litellm/busy", Code: 429, Body: `{"error":"slow down"}`}
	want := `http exec litellm/busy: status 429, {"error":"slow down"}`
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
