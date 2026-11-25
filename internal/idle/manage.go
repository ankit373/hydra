// SPDX-License-Identifier: MIT

package idle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Unpinner drops what an owner pinned in the payload store.
type Unpinner interface {
	Unpin(owner string) error
}

// PackInfo is one staged pack as `hyctl context packs` lists it.
type PackInfo struct {
	ID       string
	Number   int
	Covers   Covers
	Verdict  Verdict
	Built    time.Time
	Head     string
	Used     int
	LastUsed time.Time
}

type usage struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

func usedPath(dir, conv string) string { return filepath.Join(convDir(dir, conv), "used.json") }

func readUsed(dir, conv string) map[string]usage {
	out := map[string]usage{}
	if raw, err := os.ReadFile(usedPath(dir, conv)); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// MarkUsed records that pack n of conv was sent in place of the turns it covers.
// The caller serialises calls, as the read and the write are separate.
func MarkUsed(dir, conv string, n int, at time.Time) error {
	used := readUsed(dir, conv)
	u := used[strconv.Itoa(n)]
	used[strconv.Itoa(n)] = usage{Count: u.Count + 1, Last: at.UTC()}
	return writeJSON(usedPath(dir, conv), used)
}

// Packs lists conv's staged packs, oldest first.
func Packs(dir, conv string) ([]PackInfo, error) {
	nums, err := packNumbers(dir, conv, false)
	if err != nil {
		return nil, err
	}
	used := readUsed(dir, conv)
	var out []PackInfo
	for _, n := range nums {
		raw, err := os.ReadFile(filepath.Join(convDir(dir, conv), fmt.Sprintf("pack-%d.json", n)))
		if err != nil {
			return nil, err
		}
		var st Staged
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, fmt.Errorf("idle: pack %d of %s: %w", n, conv, err)
		}
		u := used[strconv.Itoa(n)]
		out = append(out, PackInfo{ID: PackID(conv, n), Number: n, Covers: st.Pack.Covers, Verdict: st.Verdict,
			Built: st.Pack.Built, Head: st.Pack.Head, Used: u.Count, LastUsed: u.Last})
	}
	return out, nil
}

// ErrNoPack reports an id that names no staged pack.
var ErrNoPack = errors.New("idle: no such pack")

// FindPack reads the staged pack a PackID names.
func FindPack(dir, id string) (*Staged, error) {
	name, num, ok := strings.Cut(id, "/")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || name == "" || strings.ContainsAny(name, `/\`) || name == ".." {
		return nil, fmt.Errorf("%w: %q is not <conversation>/<number>", ErrNoPack, id)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name, fmt.Sprintf("pack-%d.json", n)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoPack, id)
	}
	if err != nil {
		return nil, err
	}
	var st Staged
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("idle: pack %s: %w", id, err)
	}
	return &st, nil
}

// Resolve finds the conversation a user named, by its key or by the directory
// name a pack id shows (a hashed key).
func Resolve(dir, arg string) (string, bool) {
	sums, _ := Summaries(dir)
	for _, s := range sums {
		if s.Key == arg || filepath.Base(convDir("", s.Key)) == arg {
			return s.Key, true
		}
	}
	return "", false
}

func restoredPath(dir, conv string) string { return filepath.Join(convDir(dir, conv), "restored") }

// Restored reports whether the user undid compaction for conv.
func Restored(dir, conv string) bool {
	_, err := os.Stat(restoredPath(dir, conv))
	return err == nil
}

// Restore makes every later request of conv go out as it came and stops new
// packs being built for it, then drops its pins. Another conversation's pack
// pinning the same originals keeps them: pins are per owner.
func Restore(dir, conv string, store Unpinner) error {
	if _, err := os.Stat(convDir(dir, conv)); err != nil {
		return fmt.Errorf("idle: nothing staged for %s: %w", conv, err)
	}
	if err := os.WriteFile(restoredPath(dir, conv), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	return store.Unpin(PinOwner(conv))
}
