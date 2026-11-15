// SPDX-License-Identifier: MIT

// Package checkgen turns a task into checks that fit its kind of work. Each
// generator writes a Spec; Run builds the Spec's oracle, judges one answer and logs the verdict.
package checkgen

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// SpecKinds are the shapes a generated check can take, one per generator.
var SpecKinds = []string{"labels", "fields", "claims", "rubric", "tests"}

// Task is what a generator reads: the request and its kind-of-work key.
type Task struct {
	Text string
	Kind string
}

// GenInfo names who wrote a check. Family sits beside the head because a
// check's false-pass rate depends on whether it shares the answer's family.
type GenInfo struct {
	Head   string `json:"head"`
	Family string `json:"family"`
	// SameFamily is PickGenerator's flag: no head of another family could write it.
	SameFamily bool `json:"same_family,omitempty"`
}

// Spec is one generated check. Body is its kind's JSON; ID hashes the rest.
type Spec struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body"`
	TaskHash  string          `json:"task_hash,omitempty"`
	Generator GenInfo         `json:"generator"`
}

// NewSpec builds a Spec, so every generator derives the task hash and ID one way.
func NewSpec(kind string, body any, task Task, gen GenInfo) (Spec, error) {
	if !slices.Contains(SpecKinds, kind) {
		return Spec{}, fmt.Errorf("checkgen: unknown spec kind %q", kind)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Spec{}, fmt.Errorf("checkgen: %s body: %w", kind, err)
	}
	s := Spec{Kind: kind, Body: raw, TaskHash: evalset.TaskHashFor(task.Text), Generator: gen}
	id, err := json.Marshal(s)
	if err != nil {
		return Spec{}, err
	}
	s.ID = evalset.Hash(string(id))
	return s, nil
}

// Generator writes the checks of one spec kind and turns them into oracles.
type Generator interface {
	Kind() string
	Generate(ctx context.Context, task Task) ([]Spec, error)
	Oracle(spec Spec) (oracle.Oracle, error)
}

var registry = struct {
	sync.RWMutex
	byWork map[string][]Generator
	byKind map[string]Generator
}{byWork: map[string][]Generator{}, byKind: map[string]Generator{}}

// Register files g under a kind of work from trust.Kinds. One generator owns a
// spec kind, so Run never has two ways to build the same spec's oracle.
func Register(work string, g Generator) error {
	if !slices.Contains(trust.Kinds, work) {
		return fmt.Errorf("checkgen: unknown kind of work %q: want one of %s", work, strings.Join(trust.Kinds, ", "))
	}
	kind := g.Kind()
	if !slices.Contains(SpecKinds, kind) {
		return fmt.Errorf("checkgen: generator has unknown spec kind %q", kind)
	}
	registry.Lock()
	defer registry.Unlock()
	if owner, ok := registry.byKind[kind]; ok && owner != g {
		return fmt.Errorf("checkgen: spec kind %q already has a generator", kind)
	}
	registry.byKind[kind] = g
	if !slices.Contains(registry.byWork[work], g) {
		registry.byWork[work] = append(registry.byWork[work], g)
	}
	return nil
}

// For returns the generators for a kind-of-work key, <kind> or <kind>.<area>.
func For(key string) []Generator {
	work, _, _ := strings.Cut(key, ".")
	registry.RLock()
	defer registry.RUnlock()
	return slices.Clone(registry.byWork[work])
}

func generatorOf(kind string) (Generator, bool) {
	registry.RLock()
	defer registry.RUnlock()
	g, ok := registry.byKind[kind]
	return g, ok
}
