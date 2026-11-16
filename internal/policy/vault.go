// SPDX-License-Identifier: MIT

package policy

import (
	"regexp"
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
}

func NewVault() *Vault {
	return &Vault{byValue: map[string]string{}, byKey: map[string]string{}, next: map[string]int{}}
}

// Len is how many distinct values the vault holds.
func (v *Vault) Len() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.byKey)
}

// RedactReversible replaces every hit Redact would replace with <<KIND_N>>. One
// value gets one placeholder for the vault's life, so it reads the same each turn.
func RedactReversible(text string, v *Vault) string {
	spans, _ := redactionSpans(text)
	if len(spans) == 0 {
		return text
	}
	v.mu.Lock()
	defer v.mu.Unlock()
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
