// SPDX-License-Identifier: MIT

package policy

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Vault maps numbered placeholders back to the values they replaced. It lives in
// memory for one request and is never sent, logged or written (#1880).
type Vault struct {
	mu      sync.Mutex
	byValue map[string]string // original -> placeholder key, e.g. EMAIL_1
	byKey   map[string]string // placeholder key -> original
	next    map[string]int    // per kind, the last number handed out
	// restored counts placeholders Restore put back, over the vault's life.
	restored int
	// extra are values no pattern finds, e.g. names a local model marked, by kind.
	extra map[string]string
}

func NewVault() *Vault {
	return &Vault{byValue: map[string]string{}, byKey: map[string]string{}, next: map[string]int{}}
}

// Len is how many distinct values the vault holds; 0 for a nil vault.
func (v *Vault) Len() int {
	if v == nil {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.byKey)
}

// Counts is how many distinct values the vault holds per detector kind, e.g.
// {"EMAIL": 2}. Counts only: nothing that could recover a value.
func (v *Vault) Counts() map[string]int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.byKey) == 0 {
		return nil
	}
	out := map[string]int{}
	for k := range v.byKey {
		out[k[:strings.LastIndexByte(k, '_')]]++
	}
	return out
}

// Restored is how many placeholders Restore has put back so far.
func (v *Vault) Restored() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.restored
}

// Keys lists the placeholder keys the vault issued, e.g. EMAIL_1, sorted.
func (v *Vault) Keys() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Sorted(maps.Keys(v.byKey))
}

// RedactReversible replaces every hit Redact would replace with <<KIND_N>>. One
// value gets one placeholder for the vault's life, so it reads the same each turn.
func RedactReversible(text string, v *Vault) string {
	spans, _ := redactionSpans(text)
	v.mu.Lock()
	defer v.mu.Unlock()
	spans = v.withExtra(text, spans)
	if len(spans) == 0 {
		return text
	}
	upper := strings.ToUpper(text)
	var b strings.Builder
	last := 0
	for _, s := range spans {
		b.WriteString(text[last:s.start])
		b.WriteString("<<" + v.keyFor(text[s.start:s.end], s.name, upper) + ">>")
		last = s.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// AddValues makes every later RedactReversible replace these values too, as
// kind. A value overlapping a pattern hit is left to the pattern (#1881).
func (v *Vault) AddValues(kind string, values []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.extra == nil {
		v.extra = map[string]string{}
	}
	for _, s := range values {
		v.extra[s] = kind
	}
}

// withExtra adds each occurrence of an added value that no span already
// touches, longest value first, and returns all spans in order.
func (v *Vault) withExtra(text string, spans []redactSpan) []redactSpan {
	if len(v.extra) == 0 {
		return spans
	}
	values := slices.SortedFunc(maps.Keys(v.extra), func(a, b string) int { return len(b) - len(a) })
	for _, val := range values {
		for from := 0; ; {
			i := strings.Index(text[from:], val)
			if i < 0 {
				break
			}
			s := redactSpan{start: from + i, end: from + i + len(val), name: v.extra[val]}
			if !slices.ContainsFunc(spans, func(o redactSpan) bool { return s.start < o.end && o.start < s.end }) {
				spans = append(spans, s)
			}
			from = s.end
		}
	}
	slices.SortFunc(spans, func(a, b redactSpan) int { return a.start - b.start })
	return spans
}

// keyFor skips a number whose key already appears in the text, so Restore can
// never mistake the user's own EMAIL_1 for a placeholder.
func (v *Vault) keyFor(value, detector, upperText string) string {
	if k, ok := v.byValue[value]; ok {
		return k
	}
	kind := placeholderKind(detector)
	for {
		v.next[kind]++
		k := kind + "_" + strconv.Itoa(v.next[kind])
		if !strings.Contains(upperText, k) {
			v.byValue[value], v.byKey[k] = k, value
			return k
		}
	}
}

// placeholderKind turns a detector name into a placeholder kind: "aws access key id"
// becomes AWS_ACCESS_KEY_ID.
func placeholderKind(detector string) string {
	return strings.ToUpper(strings.ReplaceAll(detector, " ", "_"))
}

// placeholderRe also matches the light edits a model makes: changed case and
// missing or single brackets. Anything looser is left alone.
var placeholderRe = regexp.MustCompile(`(?i)(<{1,2}\s*)?\b([a-z]+(?:_[a-z]+)*_\d+)\b(\s*>{1,2})?`)

// Restore puts the vault's values back. It returns, deduped, every bracketed
// placeholder the vault never issued: a model made it up, so it is reported.
func (v *Vault) Restore(text string) (string, []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var unknown []string
	seen := map[string]bool{}
	out := placeholderRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := placeholderRe.FindStringSubmatch(m)
		key := strings.ToUpper(sub[2])
		if val, ok := v.byKey[key]; ok {
			v.restored++
			return val
		}
		if (sub[1] != "" || sub[3] != "") && !seen[key] {
			seen[key] = true
			unknown = append(unknown, "<<"+key+">>")
		}
		return m
	})
	return out, unknown
}

// StreamRestorer restores an answer that arrives in pieces. A placeholder split
// across two pieces is held back until the next one decides it (#1883).
type StreamRestorer struct {
	v    *Vault
	held string
}

// Stream starts restoring one streamed answer.
func (v *Vault) Stream() *StreamRestorer { return &StreamRestorer{v: v} }

// Push returns the part of held+chunk that is safe to show, restored. It holds
// back at most the longest placeholder the vault could restore.
func (s *StreamRestorer) Push(chunk string) string {
	buf := s.held + chunk
	limit := s.v.longestPlaceholder()
	if limit == 0 {
		s.held = ""
		return buf
	}
	cut := max(trailingRunStart(buf), len(buf)-limit)
	for _, m := range placeholderRe.FindAllStringIndex(buf, -1) {
		if m[0] >= cut || m[1] <= cut {
			continue
		}
		// A match the cut would split: send it whole when it is closed or too
		// long to be one of ours, hold it whole when it may still grow.
		if m[1] < len(buf) || m[1]-m[0] > limit {
			cut = m[1]
		} else {
			cut = m[0]
		}
	}
	s.held = buf[cut:]
	out, _ := s.v.Restore(buf[:cut])
	return out
}

// Flush restores and returns whatever is still held, at the end of the answer.
func (s *StreamRestorer) Flush() string {
	if s.held == "" {
		return ""
	}
	out, _ := s.v.Restore(s.held)
	s.held = ""
	return out
}

// longestPlaceholder is the longest text Restore turns back into a value: the
// longest key with brackets and a space on each side. 0 for an empty vault.
func (v *Vault) longestPlaceholder() int {
	if v == nil {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for k := range v.byKey {
		n = max(n, len(k))
	}
	if n == 0 {
		return 0
	}
	return n + len("<< ") + len(" >>")
}

// trailingRunStart is where buf's tail of placeholder characters begins.
func trailingRunStart(buf string) int {
	i := len(buf)
	for i > 0 {
		c := buf[i-1]
		if !(isLetter(c) || isDigit(c) || c == '_' || c == '<' || c == '>' || c == ' ') {
			break
		}
		i--
	}
	return i
}
