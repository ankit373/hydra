// SPDX-License-Identifier: MIT

package workflow

import (
	"fmt"
	"slices"

	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/payload"
)

// BlockKind says what a context block holds.
type BlockKind string

const (
	BlockSystem   BlockKind = "system"
	BlockRequest  BlockKind = "request"
	BlockDecision BlockKind = "decision"
	BlockDocument BlockKind = "document"
	BlockFile     BlockKind = "file"
	BlockOutput   BlockKind = "output"
	BlockSummary  BlockKind = "summary"
)

var blockKinds = []BlockKind{BlockSystem, BlockRequest, BlockDecision, BlockDocument, BlockFile, BlockOutput, BlockSummary}

// Block is one piece of a request's context, addressed by payload.Hash so the
// same text has the same address everywhere in hydra.
type Block struct {
	Hash   string    `json:"hash"`
	Kind   BlockKind `json:"kind"`
	Label  string    `json:"label"`
	Text   string    `json:"text"`
	Tokens int       `json:"tokens"`
}

// Put stores text as a block and returns its hash. The same text twice is
// stored once. Lives in the record, so it is never evicted while the workflow exists.
func (w *Workflow) Put(kind BlockKind, label, text string) (string, error) {
	if !slices.Contains(blockKinds, kind) {
		return "", fmt.Errorf("unknown block kind %q", kind)
	}
	// Refused rather than cut: a truncated document reads as a whole one.
	if len(text) > payload.MaxBlobBytes {
		return "", fmt.Errorf("block %q is %d bytes, over the %d-byte cap: %w", label, len(text), payload.MaxBlobBytes, payload.ErrTooLarge)
	}
	h := payload.Hash(text)
	if _, ok := w.Blocks[h]; ok {
		return h, nil
	}
	if w.Blocks == nil {
		w.Blocks = map[string]Block{}
	}
	w.Blocks[h] = Block{Hash: h, Kind: kind, Label: label, Text: text, Tokens: entropy.TokenEstimate(text)}
	return h, nil
}

// checkReads refuses a step that reads a block the record does not hold.
func (w Workflow) checkReads() error {
	for _, s := range w.Steps {
		for _, h := range s.Reads {
			if _, ok := w.Blocks[h]; !ok {
				return fmt.Errorf("step %d reads block %s, which the record does not hold", s.N, h)
			}
		}
	}
	return nil
}
