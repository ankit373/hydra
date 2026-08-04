// SPDX-License-Identifier: MIT

package formats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/registry"
)

// ── field-presence contracts ──────────────────────────────────────────────────

// jsonFields returns the JSON name and kind of every field a struct serialises.
func jsonFields(t *testing.T, v any) map[string]reflect.Kind {
	t.Helper()

	typ := reflect.TypeOf(v)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("jsonFields: %T is not a struct", v)
	}

	out := map[string]reflect.Kind{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue // unexported: never serialised
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type.Kind()
	}
	return out
}

// requireFields asserts every named field is still present, with the same kind.
//
// A subset check, not an exact match: adding a field is a compatible change and
// must not need this test edited, while removing or retyping one breaks every
// reader — `hyctl stats`, the desktop app, and whatever a user has piped into
// jq — and must go red here first.
func requireFields(t *testing.T, name string, v any, want map[string]reflect.Kind) {
	t.Helper()

	got := jsonFields(t, v)
	for field, kind := range want {
		gotKind, present := got[field]
		if !present {
			t.Errorf("%s: field %q is gone. Every reader of this file breaks — "+
				"`hyctl stats`, the desktop app, and any jq pipeline a user wrote. "+
				"If the rename is intended, migrate the readers in the same change.",
				name, field)
			continue
		}
		if gotKind != kind {
			t.Errorf("%s: field %q changed from %v to %v. Files already on disk "+
				"will not parse.", name, field, kind, gotKind)
		}
	}

	// Report additions without failing: they are compatible, and naming them
	// here means the next person editing this test knows the list is stale.
	var added []string
	for field := range got {
		if _, known := want[field]; !known {
			added = append(added, field)
		}
	}
	if len(added) > 0 {
		sort.Strings(added)
		t.Logf("%s: new field(s) since this contract was written: %s "+
			"(compatible — add them here when convenient)", name, strings.Join(added, ", "))
	}
}

// cost.jsonl is what `hyctl cost`, `hyctl stats` and the desktop dashboard all
// read. The token/cost source labels are load-bearing: they are what stops an
// agy char/4 estimate being presented as measured spend.
func TestCostRow_Schema(t *testing.T) {
	requireFields(t, "cost.jsonl", cost.Row{}, map[string]reflect.Kind{
		"ts":              reflect.String,
		"tier":            reflect.Int,
		"enum":            reflect.String,
		"model":           reflect.String,
		"executor":        reflect.String,
		"pool":            reflect.String,
		"prompt_tokens":   reflect.Int,
		"response_tokens": reflect.Int,
		"est_cost_usd":    reflect.Float64,
		"wall_ms":         reflect.Int64,
		"source":          reflect.String,
		"tokens_source":   reflect.String,
		"cost_source":     reflect.String,
		"task_id":         reflect.String,
		"run_id":          reflect.String,
		"swarm_mode":      reflect.String,
		"swarm_winner":    reflect.Bool,
		"config":          reflect.String,
	})
}

func TestTrustRunLog_Schema(t *testing.T) {
	requireFields(t, "trust.jsonl", trust.RunLog{}, map[string]reflect.Kind{
		"ts":          reflect.String,
		"task_hash":   reflect.String,
		"domain":      reflect.String,
		"target_conf": reflect.Float64,
		"final_conf":  reflect.Float64,
		"samples":     reflect.Int,
		"models":      reflect.Slice,
		"cost_usd":    reflect.Float64,
		"cost_source": reflect.String,
		"decision":    reflect.String,
		"ledger":      reflect.Slice,
		"config":      reflect.String,
	})
}

func TestRunlogEvent_Schema(t *testing.T) {
	requireFields(t, "runlog event", runlog.Event{}, map[string]reflect.Kind{
		"v":           reflect.Int,
		"seq":         reflect.Uint64,
		"ts":          reflect.String,
		"run_id":      reflect.String,
		"task_id":     reflect.String,
		"kind":        reflect.String,
		"agent":       reflect.String,
		"parent":      reflect.String,
		"head":        reflect.String,
		"model":       reflect.String,
		"tier":        reflect.Int,
		"status":      reflect.String,
		"cost_usd":    reflect.Float64,
		"duration_ms": reflect.Int64,
		"confidence":  reflect.Float64,
	})
}

func TestHandoff_Schema(t *testing.T) {
	requireFields(t, "last_handoff.json", a2a.Handoff{}, map[string]reflect.Kind{
		"from":         reflect.String,
		"model":        reflect.String,
		"task":         reflect.String,
		"files":        reflect.Slice,
		"conventions":  reflect.String,
		"context":      reflect.String,
		"prior_output": reflect.String,
		"clock":        reflect.Map,
	})
}

func TestGraph_Schema(t *testing.T) {
	requireFields(t, "graph.json", graph.Doc{}, map[string]reflect.Kind{
		"nodes": reflect.Slice,
		"edges": reflect.Slice,
	})
	requireFields(t, "graph.json node", graph.Node{}, map[string]reflect.Kind{
		"id":   reflect.String,
		"file": reflect.String,
	})
	requireFields(t, "graph.json edge", graph.Edge{}, map[string]reflect.Kind{
		"from": reflect.String,
		"to":   reflect.String,
	})
}

// ── backward-compatibility fixtures ───────────────────────────────────────────

// Each fixture is a file as an older Hydra wrote it. They must still parse and
// still mean the same thing. This is the check a field rename actually trips:
// the struct compiles, the round trip passes, and only a file written before
// the change reveals it.

func TestCostLog_OlderFixturesStillParse(t *testing.T) {
	s := testutil.NewSandbox(t)
	logDir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := readFixture(t, "cost.v1.jsonl")
	if err := os.WriteFile(filepath.Join(logDir, "cost.jsonl"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = s

	rows, err := cost.LoadAll()
	if err != nil {
		t.Fatalf("a cost.jsonl written by an earlier Hydra no longer loads: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("loaded %d rows from the fixture, want 3", len(rows))
	}

	// The oldest row predates run_id/task_id/swarm fields entirely. It must
	// load with those zeroed rather than failing the whole file — one
	// unparsable line would hide every row after it.
	oldest := rows[0]
	if oldest.Model != "claude-sonnet-4" || oldest.Tier != 2 {
		t.Errorf("oldest row = %+v, want its model and tier preserved", oldest)
	}
	if oldest.EstCostUSD != 0.0123 {
		t.Errorf("est_cost_usd = %v, want 0.0123", oldest.EstCostUSD)
	}
	if oldest.RunID != "" {
		t.Errorf("run_id = %q on a row written before that field existed", oldest.RunID)
	}

	// A row from after the token-source split must keep its label — that label
	// is the whole reason an estimate is not reported as measured spend.
	var sawEstimated bool
	for _, r := range rows {
		if r.TokensSource == "estimated" {
			sawEstimated = true
		}
	}
	if !sawEstimated {
		t.Error("the tokens_source label did not survive the load; an agy char/4 " +
			"guess would be reported as measured spend")
	}

	// The aggregate every reader actually consumes must still compute.
	summary, err := cost.Summary()
	if err != nil {
		t.Fatalf("Summary over the fixture failed: %v", err)
	}
	if summary.AllTime.Calls != 3 {
		t.Errorf("AllTime.Calls = %d, want 3", summary.AllTime.Calls)
	}
}

func TestRunlog_OlderFixturesStillParse(t *testing.T) {
	testutil.NewSandbox(t)

	dir := filepath.Join(config.Dir(), "logs", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := readFixture(t, "runlog.v1.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "run-fixture.jsonl"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := runlog.Load("run-fixture")
	if err != nil {
		t.Fatalf("a run log written by an earlier Hydra no longer loads: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events loaded from the fixture")
	}

	// The schema version is the point of the `v` field: a reader must be able
	// to tell which shape it is holding.
	for i, e := range events {
		if e.V == 0 {
			t.Errorf("event %d carries no schema version; nothing can tell which "+
				"shape it is", i)
		}
		if e.Kind == "" {
			t.Errorf("event %d has no kind", i)
		}
		if e.RunID == "" {
			t.Errorf("event %d has no run id", i)
		}
	}
	// Ordering comes from sequence, not from the timestamp string.
	for i := 1; i < len(events); i++ {
		if events[i].Seq < events[i-1].Seq {
			t.Errorf("events are out of sequence at %d: %d then %d",
				i, events[i-1].Seq, events[i].Seq)
		}
	}
}

func TestTrustLog_OlderFixturesStillParse(t *testing.T) {
	testutil.NewSandbox(t)

	fixture := readFixture(t, "trust.v1.jsonl")
	path := trust.DefaultLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(fixture)), "\n") {
		var got trust.RunLog
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("a trust.jsonl line written by an earlier Hydra no longer "+
				"parses: %v\n%s", err, line)
		}
		if got.TaskHash == "" {
			t.Errorf("task_hash is empty; `hyctl trust explain` finds a run by it: %s", line)
		}
		if got.Decision == "" {
			t.Errorf("decision is empty: %s", line)
		}
	}
}

func TestHandoff_OlderFixtureStillParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last_handoff.json")
	if err := os.WriteFile(path, readFixture(t, "handoff.v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}

	h, err := a2a.Load(path)
	if err != nil {
		t.Fatalf("a handoff written by an earlier Hydra no longer loads: %v", err)
	}
	if h == nil {
		t.Fatal("Load returned no handoff")
	}
	if h.From == "" || h.Task == "" {
		t.Errorf("handoff = %+v, want its identity and task preserved", h)
	}
	// A handoff written before vector clocks existed has none, and must load
	// as the empty causal history rather than failing.
	older := filepath.Join(dir, "no-clock.json")
	if err := os.WriteFile(older, readFixture(t, "handoff.preclock.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h2, err := a2a.Load(older)
	if err != nil {
		t.Fatalf("a pre-vector-clock handoff no longer loads: %v", err)
	}
	if h2 == nil || h2.From == "" {
		t.Fatalf("pre-clock handoff = %+v", h2)
	}
	if len(h2.Clock) != 0 {
		t.Errorf("clock = %v on a handoff written before clocks existed", h2.Clock)
	}
	// And it must still be usable: ticking an empty clock is what starts a
	// chain from a pre-clock handoff.
	if ticked := h2.Clock.Tick("agent"); len(ticked) != 1 {
		t.Errorf("ticking an absent clock produced %v", ticked)
	}
}

func TestState_OlderFixtureStillParses(t *testing.T) {
	fixture := readFixture(t, "state.v1.json")

	// state.json is a map, not a struct: Hydra owns some keys and reads others
	// that the orchestrator writes by hand. Both must survive.
	var state map[string]any
	if err := json.Unmarshal(fixture, &state); err != nil {
		t.Fatalf("a state.json written by an earlier Hydra no longer parses: %v", err)
	}
	for _, key := range []string{"claude_pct", "last_model", "last_tier", "last_status"} {
		if _, present := state[key]; !present {
			t.Errorf("state.json key %q is missing from the fixture; `hyctl status` "+
				"and the cockpit both read it", key)
		}
	}
	// claude_pct arrives as a JSON number, which is float64 in Go. Every reader
	// of it goes through that conversion.
	if _, ok := state["claude_pct"].(float64); !ok {
		t.Errorf("claude_pct is %T, not a JSON number", state["claude_pct"])
	}
}

func TestGraph_OlderFixtureStillParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(path, readFixture(t, "graph.v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}

	g, err := graph.Load(path)
	if err != nil {
		t.Fatalf("a graph.json written by an earlier indexer no longer loads: %v", err)
	}
	if g.Empty() {
		t.Fatal("the fixture loaded as an empty graph")
	}
	if !g.Knows("internal/auth/token.go") {
		t.Fatal("the graph does not know a file the fixture declares")
	}

	// token.go has two direct dependents and one transitive, so a change to it
	// is not a local change. This is what `hyctl dispatch --file` routes on.
	if got := g.DependentCount("internal/auth/token.go"); got < 2 {
		t.Errorf("DependentCount = %d, want at least the two direct dependents "+
			"the fixture declares", got)
	}
	if got := g.BlastRadiusForFile("internal/auth/token.go"); got <= 1 {
		t.Errorf("BlastRadiusForFile = %v, want more than 1 for a file with "+
			"dependents", got)
	}
	// A leaf has no dependents and must not be inflated into one.
	if got := g.DependentCount("cmd/server/main.go"); got != 0 {
		t.Errorf("a leaf reports %d dependents", got)
	}
}

// ── registry contracts ────────────────────────────────────────────────────────

// Every registry file must parse from the copy embedded in the binary. Before
// #238 nothing checked that: brew/npm/pip/curl ship the binary alone, so every
// installed Hydra ran with no registry at all and priced every CLI-agent head
// at $0.00.
func TestRegistry_EmbeddedCopyIsReadableAndValid(t *testing.T) {
	s := testutil.NewSandbox(t) // $HYDRA_HOME is empty: no on-disk override

	for _, name := range []string{
		"routing.yaml", "models.yaml", "domains.yaml",
		"pricing.yaml", "policy.yaml", "workspace.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := registry.Read(s.HydraHome, name)
			if err != nil {
				t.Fatalf("%s is not readable from the embedded registry — this is "+
					"every brew/npm/pip/curl install (#238): %v", name, err)
			}
			if len(raw) == 0 {
				t.Fatalf("%s is empty", name)
			}
			var doc any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("%s is not valid YAML: %v", name, err)
			}
			if doc == nil {
				t.Fatalf("%s parses to nothing", name)
			}
		})
	}
}

// An operator's on-disk copy must win over the embedded one — that is the whole
// point of $HYDRA_HOME/registry, and a silent fallback to embedded would mean
// their retuned routing was ignored.
func TestRegistry_OnDiskOverrideWinsOverEmbedded(t *testing.T) {
	s := testutil.NewSandbox(t)

	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := "# operator override\nversion: \"9.9\"\n"
	if err := os.WriteFile(filepath.Join(regDir, "routing.yaml"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}

	raw, err := registry.Read(s.HydraHome, "routing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "operator override") {
		t.Errorf("the embedded copy won over the operator's on-disk file:\n%s", raw)
	}

	// A file they did *not* override still comes from the embedded copy, so a
	// partial override does not blank the rest of the registry.
	models, err := registry.Read(s.HydraHome, "models.yaml")
	if err != nil {
		t.Fatalf("overriding one file broke the others: %v", err)
	}
	if len(models) == 0 {
		t.Error("models.yaml is empty after routing.yaml was overridden")
	}
}

// pricing.yaml is not just an offline fallback: it is what prices the CLI-agent
// heads that never appear in OpenRouter's catalogue. A tier missing from it is
// a head that costs $0.00 in every report.
func TestRegistry_PricingCoversEveryTier(t *testing.T) {
	s := testutil.NewSandbox(t)

	raw, err := registry.Read(s.HydraHome, "pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tiers map[int]struct {
			InputPerMillion  float64 `yaml:"input_per_million"`
			OutputPerMillion float64 `yaml:"output_per_million"`
		} `yaml:"tiers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Tiers) == 0 {
		t.Fatal("pricing.yaml declares no tiers; every tier estimate would be $0.00")
	}
	for tier := 1; tier <= 10; tier++ {
		p, present := doc.Tiers[tier]
		if !present {
			t.Errorf("tier %d has no price; every head at that tier reports $0.00", tier)
			continue
		}
		if p.InputPerMillion < 0 || p.OutputPerMillion < 0 {
			t.Errorf("tier %d has a negative price: %+v", tier, p)
		}
	}
	// Tier 10 is the local/terminal tier and must be free, or a local head
	// appears to cost money and cost routing inverts.
	if p := doc.Tiers[10]; p.InputPerMillion != 0 || p.OutputPerMillion != 0 {
		t.Errorf("tier 10 is priced at %+v; local heads cost nothing", p)
	}
	// Tier 1 must cost more than tier 10, or there is nothing to route away from.
	if doc.Tiers[1].InputPerMillion <= doc.Tiers[10].InputPerMillion {
		t.Errorf("tier 1 (%v) is not dearer than tier 10 (%v); cost routing has "+
			"nothing to route on",
			doc.Tiers[1].InputPerMillion, doc.Tiers[10].InputPerMillion)
	}
}

// workspace.yaml ships embedded to every install, so it must not carry anyone's
// absolute paths. It did — two roots from the maintainer's own machine, which
// meant a fresh install had no workspace that could ever match (#297).
func TestRegistry_WorkspaceShipsNoAbsolutePaths(t *testing.T) {
	s := testutil.NewSandbox(t)

	raw, err := registry.Read(s.HydraHome, "workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Workspaces map[string]struct {
			Root string `yaml:"root"`
		} `yaml:"workspaces"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for name, ws := range doc.Workspaces {
		t.Errorf("the embedded workspace.yaml ships workspace %q rooted at %q. "+
			"That path exists on one machine; every other install has a workspace "+
			"that can never match (#297).", name, ws.Root)
	}
}

// readFixture loads a checked-in sample file.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("missing fixture %s: %v", name, err)
	}
	return raw
}
