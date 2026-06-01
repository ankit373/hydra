// Package capabilities provides model scoring data, embedded at compile time.
// To add or update scores: edit data.json — no code changes needed.
package capabilities

import (
	_ "embed"
	"encoding/json"
	"os"
	"strings"
)

//go:embed data.json
var defaultData []byte

type modelEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	CapScore int    `json:"capScore"`
}

type ollamaFamily struct {
	Pattern string `json:"pattern"`
	Score   int    `json:"score"`
}

type data struct {
	Known          []modelEntry   `json:"known"`
	OllamaFamilies []ollamaFamily `json:"ollamaFamilies"`
	DefaultScore   int            `json:"defaultScore"`
}

// DB is a queryable capability database.
type DB struct {
	d     data
	index map[string]modelEntry
}

// Load builds a DB from the embedded default, optionally overridden by a
// user file at path (pass "" to use embedded data only).
func Load(overridePath string) (*DB, error) {
	raw := defaultData
	if overridePath != "" {
		if b, err := os.ReadFile(overridePath); err == nil {
			raw = b
		}
	}
	var d data
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	idx := make(map[string]modelEntry, len(d.Known))
	for _, m := range d.Known {
		idx[m.ID] = m
	}
	return &DB{d: d, index: idx}, nil
}

// Score returns the capability score for a known model ID.
// Returns DefaultScore if the ID is not in the database.
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

// Name returns the display name for a known model ID.
func (db *DB) Name(id string) string {
	if m, ok := db.index[id]; ok {
		return m.Name
	}
	return id
}
