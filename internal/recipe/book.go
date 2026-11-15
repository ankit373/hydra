// SPDX-License-Identifier: MIT

package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/registry"
)

// File is the registry file the recipes and menus live in.
const File = "recipes.yaml"

// Single is the built-in one-call recipe every menu may name and every kind
// without a menu gets.
const Single = "single"

// CheckKinds are the kinds of check a recipe can ask the request to bring.
var CheckKinds = []string{"ground", "match", "cmd"}

// Menu is what one kind of work may try, in order, and which it tries first.
type Menu struct {
	Default string   `yaml:"default"`
	Recipes []string `yaml:"recipes"`
}

// Skip names a recipe left off a request's menu, and why.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Book is a loaded recipes.yaml.
type Book struct {
	recipes map[string]Recipe
	menus   map[string]Menu
}

type bookFile struct {
	Recipes []Recipe        `yaml:"recipes"`
	Menus   map[string]Menu `yaml:"menus"`
}

// Load reads recipes.yaml, home's copy first, else the one in the binary.
func Load(home string) (Book, error) {
	raw, err := registry.Read(home, File)
	if err != nil {
		return Book{}, fmt.Errorf("cannot read %s: %w", File, err)
	}
	return Parse(raw)
}

// Parse refuses a file that names an unknown field, kind, check or recipe, or
// a recipe that cannot work, with the line at fault.
func Parse(raw []byte) (Book, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f bookFile
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return Book{}, fmt.Errorf("%s is empty", File)
		}
		return Book{}, fmt.Errorf("%s: %w", File, err)
	}
	// Decoded a second time as nodes, only to name the line a bad entry is on.
	// It cannot fail: the strict decode above read the same bytes.
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	recipeLines, menuLines := entryLines(&doc)

	b := Book{recipes: map[string]Recipe{}, menus: f.Menus}
	for i, r := range f.Recipes {
		if err := checkRecipe(r, b.recipes); err != nil {
			return Book{}, fmt.Errorf("%s line %d: %w", File, recipeLines[i], err)
		}
		b.recipes[r.Name] = r
	}
	for _, kind := range b.Kinds() {
		if err := b.checkMenu(kind, f.Menus[kind]); err != nil {
			return Book{}, fmt.Errorf("%s line %d: menu %q: %w", File, menuLines[kind], kind, err)
		}
	}
	return b, nil
}

// entryLines is the line each recipe and each menu starts on. Only called once
// the strict decode passed, so the document has the shape bookFile describes.
func entryLines(doc *yaml.Node) (recipes []int, menus map[string]int) {
	menus = map[string]int{}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "recipes":
			for _, n := range val.Content {
				recipes = append(recipes, n.Line)
			}
		case "menus":
			for j := 0; j+1 < len(val.Content); j += 2 {
				menus[val.Content[j].Value] = val.Content[j].Line
			}
		}
	}
	return recipes, menus
}

// Kinds lists the kinds of work that have a menu, sorted.
func (b Book) Kinds() []string {
	kinds := make([]string, 0, len(b.menus))
	for k := range b.menus {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	return kinds
}

// Recipe is a recipe by name, as declared; single is the one-call recipe.
func (b Book) Recipe(name string) (Recipe, bool) {
	if name == Single {
		return Default(nil), true
	}
	r, ok := b.recipes[name]
	return r, ok
}

// Menu is kind's menu, the single-call one for a kind without its own. kind
// may carry an area, as in code.go.
func (b Book) Menu(kind string) Menu {
	kind, _, _ = strings.Cut(kind, ".")
	if m, ok := b.menus[kind]; ok {
		return m
	}
	return Menu{Default: Single, Recipes: []string{Single}}
}

// MenuFor is kind's menu for one request, in order, each recipe's checks
// filled from the request's own check specs. A recipe needing a kind of check
// the request did not bring is skipped, and says so.
func (b Book) MenuFor(kind string, checks []string) (menu []Recipe, skipped []Skip) {
	for _, name := range b.Menu(kind).Recipes {
		if name == Single {
			menu = append(menu, Default(checks))
			continue
		}
		r, _ := b.Recipe(name)
		filled, missing := fill(r.Checks, checks)
		if missing != "" {
			skipped = append(skipped, Skip{Name: name, Reason: "needs a " + missing + " check, and the request has none"})
			continue
		}
		r.Checks = filled
		menu = append(menu, r)
	}
	return menu, skipped
}

// fill swaps each check kind for the request specs of that kind. missing is
// the first kind the request has no spec for.
func fill(kinds, specs []string) (filled []string, missing string) {
	for _, k := range kinds {
		n := len(filled)
		for _, s := range specs {
			if specKind(s) == k {
				filled = append(filled, s)
			}
		}
		if len(filled) == n {
			return nil, k
		}
	}
	return filled, ""
}

// specKind is the kind of a --check spec: "ground", or the part before the colon.
func specKind(spec string) string {
	kind, _, _ := strings.Cut(spec, ":")
	return kind
}

func checkRecipe(r Recipe, seen map[string]Recipe) error {
	switch _, dup := seen[r.Name]; {
	case strings.TrimSpace(r.Name) == "":
		return errors.New("a recipe has no name")
	case r.Name == Single:
		return fmt.Errorf("recipe %q is built in and cannot be redefined", Single)
	case dup:
		return fmt.Errorf("recipe %q is defined twice", r.Name)
	}
	for _, c := range r.Checks {
		if !slices.Contains(CheckKinds, c) {
			return fmt.Errorf("recipe %q: unknown check %q, want one of %s", r.Name, c, strings.Join(CheckKinds, ", "))
		}
	}
	return r.Validate()
}

func (b Book) checkMenu(kind string, m Menu) error {
	if !slices.Contains(trust.Kinds, kind) {
		return fmt.Errorf("unknown kind of work, want one of %s", strings.Join(trust.Kinds, ", "))
	}
	if len(m.Recipes) == 0 {
		return errors.New("lists no recipes")
	}
	for _, name := range m.Recipes {
		if _, ok := b.recipes[name]; !ok && name != Single {
			return fmt.Errorf("names recipe %q, which is not defined", name)
		}
	}
	if !slices.Contains(m.Recipes, m.Default) {
		return fmt.Errorf("default %q is not one of its recipes", m.Default)
	}
	return nil
}
