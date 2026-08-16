// SPDX-License-Identifier: MIT

// Package capabilities provides model scoring data, embedded at compile time
// and extensible at runtime via a user overlay (~/.hydra/models.json), so users
// can add new models (e.g. Kimi K2) without editing source or recompiling.
package capabilities

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ankit373/hydra/internal/config"
)

//go:embed data.json
var defaultData []byte

// Entry is one model's capability record. Source is "builtin" or "user".
type Entry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	CapScore int    `json:"capScore"`
	Source   string `json:"source,omitempty"`
}

type ollamaFamily struct {
	Pattern string `json:"pattern"`
	Score   int    `json:"score"`
}

type data struct {
	Known          []Entry        `json:"known"`
	OllamaFamilies []ollamaFamily `json:"ollamaFamilies"`
	DefaultScore   int            `json:"defaultScore"`
}

// DB is a queryable capability database (built-in ⊕ user overlay).
type DB struct {
	d     data
	index map[string]Entry
}

var (
	loadCacheMu  sync.Mutex
	loadCacheKey string
	loadCacheDB  *DB
	loadCached   bool // only ever set true alongside a successful result — see Load
)

// loadCacheFingerprint is overlayPath plus its file state: mtime+size when it
// exists, a fixed marker otherwise. The embedded defaults never change at
// runtime, so the overlay is the only thing that can invalidate a cached DB.
func loadCacheFingerprint(overlayPath string) string {
	if overlayPath == "" {
		return "none"
	}
	info, err := os.Stat(overlayPath)
	if err != nil {
		return overlayPath + "|absent"
	}
	return fmt.Sprintf("%s|%s|%d", overlayPath, info.ModTime(), info.Size())
}

// Load builds a DB from the embedded defaults, then MERGES a user overlay if
// overlayPath is non-empty and present: overlay entries add new models and
// override built-ins by ID. Pass "" for built-ins only.
//
// Cached per fingerprint (path + mtime+size, so an overlay edit invalidates
// on the next call): probe.Run fans out to the cli/env/port providers
// concurrently, and each independently called Load with the identical
// overlay path, tripling the embedded-unmarshal + merge work for the same
// result every single probe.
func Load(overlayPath string) (*DB, error) {
	key := loadCacheFingerprint(overlayPath)

	loadCacheMu.Lock()
	if loadCached && loadCacheKey == key {
		db := loadCacheDB
		loadCacheMu.Unlock()
		return db, nil
	}
	loadCacheMu.Unlock()

	db, err := loadUncached(overlayPath)

	// Only a successful result is cached. A stale cached error (e.g. from a
	// malformed overlay JSON) is worse than a stale cached success: it can
	// never self-heal once the overlay is fixed, since a fix that happens to
	// land on the identical mtime+size (SaveOverlay doesn't use an atomic
	// temp-file+rename, so a same-length rewrite is realistic) would still
	// match the cached fingerprint and keep serving the old error forever.
	if err == nil {
		loadCacheMu.Lock()
		loadCacheKey, loadCacheDB, loadCached = key, db, true
		loadCacheMu.Unlock()
	}

	return db, err
}

func loadUncached(overlayPath string) (*DB, error) {
	var d data
	if err := json.Unmarshal(defaultData, &d); err != nil {
		return nil, err
	}
	for i := range d.Known {
		d.Known[i].Source = "builtin"
	}

	idx := make(map[string]Entry, len(d.Known))
	for _, m := range d.Known {
		idx[m.ID] = m
	}

	if overlayPath != "" {
		overlay, err := LoadOverlay(overlayPath)
		if err != nil {
			return nil, err
		}
		for _, e := range overlay {
			e.Source = "user"
			if _, existed := idx[e.ID]; !existed {
				d.Known = append(d.Known, e)
			} else {
				// Replace the built-in entry in the slice too, so Entries() is consistent.
				for i := range d.Known {
					if d.Known[i].ID == e.ID {
						d.Known[i] = e
					}
				}
			}
			idx[e.ID] = e
		}
	}

	return &DB{d: d, index: idx}, nil
}

// Score returns the capability score for a known model ID, or DefaultScore.
func (db *DB) Score(id string) int {
	if m, ok := db.index[id]; ok {
		return m.CapScore
	}
	return db.d.DefaultScore
}

// ScoreOllama scores an Ollama model by matching family name patterns.
func (db *DB) ScoreOllama(modelName string) int {
	lower := strings.ToLower(modelName)
	for _, f := range db.d.OllamaFamilies {
		if strings.Contains(lower, f.Pattern) {
			return f.Score
		}
	}
	return db.d.DefaultScore
}

// Name returns the display name for a known model ID, or the ID itself.
func (db *DB) Name(id string) string {
	if m, ok := db.index[id]; ok {
		return m.Name
	}
	return id
}

// Entry returns the full capability record for id — built-in or user overlay —
// and whether it was found. Score/Name/Source each expose one field of this;
// callers that need to know what an id already resolves to before changing it
// (e.g. `models add` warning it is about to shadow a curated built-in) want
// the whole record, not one field at a time.
func (db *DB) Entry(id string) (Entry, bool) {
	e, ok := db.index[id]
	return e, ok
}

// Source returns id's capability entry's provenance — "builtin" for the
// embedded, curated catalog, "user" for one added via the runtime overlay
// (`hyctl models add`), or "" if id matched neither and fell to DefaultScore.
// This is the "managed vs. discovered" distinction AI-BOM tooling is built
// around — a user-added model isn't malicious, it just wasn't vetted by
// whoever curated the embedded catalog, and that's worth being able to see.
func (db *DB) Source(id string) string {
	if m, ok := db.index[id]; ok {
		return m.Source
	}
	return ""
}

// SourceOllama returns "builtin" when modelName matched a curated family
// pattern, or "" when it fell to DefaultScore (an unrecognized local model) —
// the ScoreOllama analog of Source, since family-pattern matching has no
// per-model Entry to look up.
func (db *DB) SourceOllama(modelName string) string {
	lower := strings.ToLower(modelName)
	for _, f := range db.d.OllamaFamilies {
		if strings.Contains(lower, f.Pattern) {
			return "builtin"
		}
	}
	return ""
}

// Entries returns all models (built-in ⊕ user), sorted by capScore descending.
func (db *DB) Entries() []Entry {
	out := append([]Entry(nil), db.d.Known...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CapScore > out[j].CapScore })
	return out
}

// ── User overlay (~/.hydra/models.json): a flat JSON array of Entry ──────────

// DefaultOverlayPath is where user-added models live.
func DefaultOverlayPath() string {
	return filepath.Join(config.Dir(), "models.json")
}

// LoadOverlay reads the user overlay. A missing file yields no entries.
func LoadOverlay(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// SaveOverlay writes the overlay atomically-ish (dir created, 0600).
func SaveOverlay(path string, entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// AddModel upserts an entry in the overlay by ID and returns whether it replaced
// an existing overlay entry.
func AddModel(path string, e Entry) (replaced bool, err error) {
	entries, err := LoadOverlay(path)
	if err != nil {
		return false, err
	}
	e.Source = "user"
	for i := range entries {
		if entries[i].ID == e.ID {
			entries[i] = e
			return true, SaveOverlay(path, entries)
		}
	}
	entries = append(entries, e)
	return false, SaveOverlay(path, entries)
}

// RemoveModel deletes an entry from the overlay by ID; returns whether it existed.
func RemoveModel(path, id string) (removed bool, err error) {
	entries, err := LoadOverlay(path)
	if err != nil {
		return false, err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.ID == id {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return false, nil
	}
	return true, SaveOverlay(path, out)
}

// HeuristicCapScore estimates a provisional capScore from a model id/name, used
// by `hyctl models sync` when importing from OpenRouter. Deliberately rough —
// users can override with `hyctl models add`.
func HeuristicCapScore(id string) int {
	s := strings.ToLower(id)
	switch {
	case containsAny(s, "opus", "gpt-5", "o1", "o3", "gpt-4.1", "claude-3.7", "claude-4"):
		return 92
	case containsAny(s, "sonnet", "gpt-4o", "gpt-4", "gemini-2.5-pro", "gemini-1.5-pro", "deepseek-r1", "grok-4"):
		return 86
	case containsAny(s, "gemini-2", "flash", "haiku", "mistral-large", "qwen2.5-coder", "kimi", "llama-3.3", "llama-3.1-70b", "70b"):
		return 78
	case containsAny(s, "8b", "7b", "mini", "small", "gemma", "phi"):
		return 66
	case containsAny(s, "3b", "1b", "tiny", "nano"):
		return 55
	default:
		return 70
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
