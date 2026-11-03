// SPDX-License-Identifier: MIT

package executor

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// StatusError is a non-2xx from a head, keeping the parts a caller can act on
// rather than formatting them away. The code distinguishes "busy" from
// "broken", and Retry-After is the one thing the server volunteers about when
// to come back (#1096).
type StatusError struct {
	HeadID string
	Code   int
	Body   string
	// RetryAfter is what the server asked for, zero when it asked for nothing.
	RetryAfter time.Duration
}

// Error keeps the wording the string form had, so anything reading the message
// rather than the type is unaffected.
func (e *StatusError) Error() string {
	return fmt.Sprintf("http exec %s: status %d, %s", e.HeadID, e.Code, e.Body)
}

// RateLimitRetryAfter reports a rate limit and the wait the server stated.
//
// Named rather than exported as a field so internal/health can read it through
// an interface it declares itself, which keeps the two packages from importing
// each other. A 429 with no header still reports true: the head is busy rather
// than broken, which is the part health needs, and the zero duration says the
// server named no time.
func (e *StatusError) RateLimitRetryAfter() (time.Duration, bool) {
	if e.Code != http.StatusTooManyRequests {
		return 0, false
	}
	return e.RetryAfter, true
}

// parseRetryAfter reads the header in both forms RFC 9110 allows, delta
// seconds and an HTTP date. An unparseable or past value is no value: a
// negative wait would park a head until a time already gone, which reads as
// not parked at all and hides that the server did say something.
func parseRetryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
