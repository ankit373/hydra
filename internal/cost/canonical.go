// SPDX-License-Identifier: MIT

package cost

import (
	"strings"
	"sync"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/registry"
	"gopkg.in/yaml.v3"
)

// A cost row records the head under whatever name its writer used, and that
// name has changed three times: registry ids, display names, and the Gemini
// rename in #693. 106 of 117 rows on a real machine predate the Head field, so
// one head rendered as two groups and neither was its spend: Claude Code showed
// $0.004890 over 31 calls beside $0.077295 over 14 (#729).
//
// Resolved on read. Nothing rewrites a log the user did not ask to have edited.

// aliasTable maps every declared spelling of a head to its canonical ID, and
// remembers which IDs are canonical so a stale name can be told apart from a
// live head.
type aliasTable struct {
	byName map[string]string
	ids    map[string]bool
}

var aliasOnce struct {
	sync.Once
	table aliasTable
}

// perModelPrefixes inverts the display name a per-model provider builds
// (ID "ollama/X" is shown as "X (Ollama)"), so it resolves a name that provider
// built rather than guessing at one. Not only local ones: an allowlisted
// OpenRouter model is one head per model the same way (#752).
var perModelPrefixes = map[string]string{
	" (Ollama)":     "ollama/",
	" (LM Studio)":  "lmstudio/",
	" (OpenRouter)": "openrouter/",
}

type aliasModel struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	Provider  string `yaml:"provider"`
	ModelFlag string `yaml:"model_flag"`
}

type aliasFile struct {
	Models []aliasModel `yaml:"models"`
}

func aliases() aliasTable {
	aliasOnce.Do(func() { aliasOnce.table = buildAliases(config.ScriptHome()) })
	return aliasOnce.table
}

// CanonicalKey is the head a row should be counted under.
func CanonicalKey(r Row) string { return canonicalKey(r, aliases()) }

// ResolveHeadName returns the head ID a recorded name denotes, or "" when
// nothing on this machine declares it.
func ResolveHeadName(name string) string { return resolveName(name, aliases()) }

// Attributable reports whether a group key names a head this machine declares.
func Attributable(key string) bool { return attributable(key, aliases()) }

// canonicalKey prefers the Head the writer recorded. Otherwise Model is
// resolved through declared mappings only: an unrecognized name is returned
// unchanged rather than matched approximately, because a wrong match files one
// head's spend against another, which is worse than leaving it separate. Same
// reason registry.TokenPoolFor refuses to match fuzzily.
func canonicalKey(r Row, a aliasTable) string {
	if r.Head != "" {
		return r.Head
	}
	if r.Model == "" {
		return UnknownPoolKey
	}
	if id := resolveName(r.Model, a); id != "" {
		return id
	}
	return r.Model
}

func resolveName(name string, a aliasTable) string {
	if id, ok := a.byName[name]; ok {
		return id
	}
	for suffix, prefix := range perModelPrefixes {
		if model, cut := strings.CutSuffix(name, suffix); cut && model != "" {
			return prefix + model
		}
	}
	return ""
}

func attributable(key string, a aliasTable) bool {
	if key == UnknownPoolKey {
		return false
	}
	if a.ids[key] {
		return true
	}
	for _, prefix := range perModelPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func buildAliases(home string) aliasTable {
	a := aliasTable{byName: map[string]string{}, ids: map[string]bool{}}
	add := func(name, id string) {
		if name != "" && id != "" {
			a.byName[name] = id
			a.ids[id] = true
		}
	}

	if raw, err := registry.Read(home, "models.yaml"); err == nil {
		var f aliasFile
		if err := yaml.Unmarshal(raw, &f); err == nil {
			for _, m := range f.Models {
				add(m.ID, m.ID)
				add(m.Name, m.ID)
				// A port-discovered head is identified as provider/model_flag,
				// which is how TokenPoolFor already resolves one, so a row
				// logged under the bare model flag names that head by
				// declaration rather than by resemblance.
				if m.Provider != "" && m.ModelFlag != "" {
					id := m.Provider + "/" + m.ModelFlag
					add(m.ModelFlag, id)
					add(id, id)
				}
			}
		}
	}

	// Applied second so it wins: capabilities carries the ids the CLI and env
	// providers actually discover, while models.yaml has registry-local ids for
	// the same heads. Both declare the name "Claude Code", and only `claude` is
	// ever a live head, so letting `claude-core` win left 45 rows split across
	// two canonical keys instead of one.
	if db, err := capabilities.Load(capabilities.DefaultOverlayPath()); err == nil {
		for _, e := range db.Entries() {
			add(e.ID, e.ID)
			add(e.Name, e.ID)
		}
	}
	return a
}
