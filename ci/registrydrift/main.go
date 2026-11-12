// SPDX-License-Identifier: MIT

// Command registrydrift reports where the registry has drifted from OpenRouter's
// catalog: models gone or retiring, prices moved, a stale "as of" date. Read-only.
// Usage: go run ./ci/registrydrift -md drift.md -json drift.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	catalogURL = "https://openrouter.ai/api/v1/models"
	maxBody    = 32 << 20
	// marker lets the workflow find its own issue body among any others.
	marker = "<!-- registry-drift -->"
)

type options struct {
	registryDir string
	watchlist   string
	maxMovePct  float64
	maxAgeDays  int
	today       time.Time
}

func main() {
	var (
		opts     options
		modelsIn = flag.String("models", "", "read the catalog from this file instead of "+catalogURL)
		mdOut    = flag.String("md", "", "write the markdown report here (default stdout)")
		jsonOut  = flag.String("json", "", "write the JSON report here")
		today    = flag.String("today", "", "date to measure ages from, YYYY-MM-DD (default today, UTC)")
	)
	flag.StringVar(&opts.registryDir, "registry", "registry", "registry directory")
	flag.StringVar(&opts.watchlist, "watchlist", "ci/registry-drift.yaml", "tier id to OpenRouter model map")
	flag.Float64Var(&opts.maxMovePct, "max-price-move", 25, "flag an upstream price this many percent off the recorded one")
	flag.IntVar(&opts.maxAgeDays, "max-age-days", 90, "flag a pricing.yaml \"as of\" date older than this")
	flag.Parse()

	if err := run(opts, *modelsIn, *today, *mdOut, *jsonOut); err != nil {
		fmt.Fprintf(os.Stderr, "registrydrift: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options, modelsIn, today, mdOut, jsonOut string) error {
	opts.today = time.Now().UTC().Truncate(24 * time.Hour)
	if today != "" {
		t, err := time.Parse(time.DateOnly, today)
		if err != nil {
			return fmt.Errorf("-today: %w", err)
		}
		opts.today = t
	}

	var body io.ReadCloser
	if modelsIn != "" {
		f, err := os.Open(modelsIn)
		if err != nil {
			return err
		}
		body = f
	} else {
		var err error
		if body, err = fetch(context.Background(), catalogURL); err != nil {
			return err
		}
	}
	cat, err := parseCatalog(io.LimitReader(body, maxBody))
	body.Close()
	if err != nil {
		return err
	}

	rep, err := check(opts, cat)
	if err != nil {
		return err
	}
	if jsonOut != "" {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(jsonOut, append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	if mdOut == "" {
		_, err = os.Stdout.WriteString(rep.markdown())
		return err
	}
	return os.WriteFile(mdOut, []byte(rep.markdown()), 0o644)
}

func fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("fetch catalog: HTTP %d", resp.StatusCode)
	}
	return cancelOnClose{resp.Body, cancel}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error { defer c.cancel(); return c.ReadCloser.Close() }

// upstream is one catalog entry. Prices are per million tokens; priced is false
// when OpenRouter publishes no usable price ("" or "-1").
type upstream struct {
	input, output float64
	priced        bool
	expires       string
}

type catalog map[string]upstream

func parseCatalog(r io.Reader) (catalog, error) {
	var raw struct {
		Data []struct {
			ID      string `json:"id"`
			Expires string `json:"expiration_date"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	// An empty catalog is an outage, and reading it as "every model retired"
	// would open an issue claiming exactly that.
	if len(raw.Data) == 0 {
		return nil, fmt.Errorf("parse catalog: no models")
	}
	cat := make(catalog, len(raw.Data))
	for _, m := range raw.Data {
		in, errI := perMillion(m.Pricing.Prompt)
		out, errO := perMillion(m.Pricing.Completion)
		cat[strings.ToLower(m.ID)] = upstream{in, out, errI == nil && errO == nil, m.Expires}
	}
	return cat, nil
}

func perMillion(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, fmt.Errorf("no price %q", s)
	}
	// Rounded so 0.000005 reads as 5, not 4.999999999999999.
	return math.Round(f*1e12) / 1e6, nil
}

type report struct {
	CheckedOn   string      `json:"checked_on"`
	CatalogSize int         `json:"catalog_size"`
	MaxMovePct  float64     `json:"max_price_move_pct"`
	MaxAgeDays  int         `json:"max_age_days"`
	DriftCount  int         `json:"drift_count"`
	Missing     []missing   `json:"missing"`
	PriceMoves  []priceMove `json:"price_moves"`
	StaleDates  []staleDate `json:"stale_dates"`
}

type missing struct {
	Source  string `json:"source"`
	Model   string `json:"model"`
	Status  string `json:"status"` // "missing" or "retiring"
	Expires string `json:"expires,omitempty"`
}

type priceMove struct {
	Tier           string   `json:"tier"`
	Model          string   `json:"model"`
	RecordedInput  float64  `json:"recorded_input_per_m"`
	RecordedOutput float64  `json:"recorded_output_per_m"`
	UpstreamInput  float64  `json:"upstream_input_per_m"`
	UpstreamOutput float64  `json:"upstream_output_per_m"`
	InputMovePct   *float64 `json:"input_move_pct"` // nil: recorded $0, now charged
	OutputMovePct  *float64 `json:"output_move_pct"`
}

type staleDate struct {
	File    string `json:"file"`
	AsOf    string `json:"as_of"` // empty: the file states no date at all
	AgeDays int    `json:"age_days,omitempty"`
}

type tierPrice struct {
	ID     string  `yaml:"id"`
	Input  float64 `yaml:"input_per_million"`
	Output float64 `yaml:"output_per_million"`
}

func check(opts options, cat catalog) (report, error) {
	rep := report{
		CheckedOn:   opts.today.Format(time.DateOnly),
		CatalogSize: len(cat),
		MaxMovePct:  opts.maxMovePct,
		MaxAgeDays:  opts.maxAgeDays,
		Missing:     []missing{},
		PriceMoves:  []priceMove{},
		StaleDates:  []staleDate{},
	}

	pricingPath := filepath.Join(opts.registryDir, "pricing.yaml")
	pricingRaw, err := os.ReadFile(pricingPath)
	if err != nil {
		return rep, err
	}
	var pricing struct {
		Tiers map[int]tierPrice `yaml:"tiers"`
	}
	if err := yaml.Unmarshal(pricingRaw, &pricing); err != nil {
		return rep, fmt.Errorf("%s: %w", pricingPath, err)
	}
	tiers := make(map[string]tierPrice, len(pricing.Tiers))
	for _, t := range pricing.Tiers {
		tiers[t.ID] = t
	}

	watch, err := loadWatchlist(opts.watchlist)
	if err != nil {
		return rep, err
	}
	ids := make([]string, 0, len(watch))
	for id := range watch {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		model := strings.ToLower(watch[id])
		tier, ok := tiers[id]
		if !ok {
			return rep, fmt.Errorf("%s: %q is not a tier id in %s", opts.watchlist, id, pricingPath)
		}
		up, found := rep.lookup(cat, filepath.ToSlash(opts.watchlist)+": "+id, model)
		if !found || !up.priced {
			continue
		}
		mv := priceMove{
			Tier: id, Model: model,
			RecordedInput: tier.Input, RecordedOutput: tier.Output,
			UpstreamInput: up.input, UpstreamOutput: up.output,
			InputMovePct:  movePct(tier.Input, up.input),
			OutputMovePct: movePct(tier.Output, up.output),
		}
		if exceeds(mv.InputMovePct, tier.Input, up.input, opts.maxMovePct) ||
			exceeds(mv.OutputMovePct, tier.Output, up.output, opts.maxMovePct) {
			rep.PriceMoves = append(rep.PriceMoves, mv)
		}
	}

	refs, err := scanRegistry(opts.registryDir, cat)
	if err != nil {
		return rep, err
	}
	for _, r := range refs {
		rep.lookup(cat, r.source, r.model)
	}

	if stale, ok := staleAsOf(pricingRaw, opts.today, opts.maxAgeDays); ok {
		stale.File = filepath.ToSlash(pricingPath)
		rep.StaleDates = append(rep.StaleDates, stale)
	}

	rep.DriftCount = len(rep.Missing) + len(rep.PriceMoves) + len(rep.StaleDates)
	return rep, nil
}

// lookup records a model that is gone or scheduled to expire, and returns its
// catalog entry when present.
func (r *report) lookup(cat catalog, source, model string) (upstream, bool) {
	up, ok := cat[model]
	switch {
	case !ok:
		r.Missing = append(r.Missing, missing{Source: source, Model: model, Status: "missing"})
	case up.expires != "":
		r.Missing = append(r.Missing, missing{Source: source, Model: model, Status: "retiring", Expires: up.expires})
	}
	return up, ok
}

func loadWatchlist(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w struct {
		Upstream map[string]string `yaml:"upstream"`
	}
	if err := yaml.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return w.Upstream, nil
}

func movePct(recorded, now float64) *float64 {
	if recorded == 0 {
		return nil
	}
	p := (now - recorded) / recorded * 100
	return &p
}

func exceeds(pct *float64, recorded, now, limit float64) bool {
	if pct == nil {
		return now != recorded
	}
	return math.Abs(*pct) > limit
}

type ref struct{ source, model string }

// modelShaped is a whole YAML scalar spelled like a catalog id, vendor/model.
var modelShaped = regexp.MustCompile(`^~?[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9._:-]*$`)

// scanRegistry finds catalog-shaped ids in any registry YAML value. A vendor
// the catalog has never listed is a path like internal/trust, not a model.
func scanRegistry(dir string, cat catalog) ([]ref, error) {
	vendors := map[string]bool{}
	for id := range cat {
		vendors[strings.SplitN(id, "/", 2)[0]] = true
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var refs []ref
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var root yaml.Node
		if err := yaml.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		walk(&root, func(n *yaml.Node) {
			v := strings.ToLower(n.Value)
			if modelShaped.MatchString(v) && vendors[strings.SplitN(v, "/", 2)[0]] {
				refs = append(refs, ref{fmt.Sprintf("%s:%d", filepath.ToSlash(f), n.Line), v})
			}
		})
	}
	return refs, nil
}

func walk(n *yaml.Node, visit func(*yaml.Node)) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		visit(n)
	}
	for _, c := range n.Content {
		walk(c, visit)
	}
}

var asOfRe = regexp.MustCompile(`(?i)\bas of (\d{4})-(\d{2})(?:-(\d{2}))?\b`)

// staleAsOf reads the oldest "as of" date in the file. A month-only date counts
// from its last day, so a flag means the prices really are that old.
func staleAsOf(raw []byte, today time.Time, maxAge int) (staleDate, bool) {
	matches := asOfRe.FindAllSubmatch(raw, -1)
	if len(matches) == 0 {
		return staleDate{}, true
	}
	var oldest time.Time
	var label string
	for _, m := range matches {
		y, _ := strconv.Atoi(string(m[1]))
		mo, _ := strconv.Atoi(string(m[2]))
		d := time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC)
		if len(m[3]) > 0 {
			day, _ := strconv.Atoi(string(m[3]))
			d = time.Date(y, time.Month(mo), day, 0, 0, 0, 0, time.UTC)
		}
		if oldest.IsZero() || d.Before(oldest) {
			oldest, label = d, string(m[0][len("as of "):])
		}
	}
	age := int(today.Sub(oldest).Hours() / 24)
	if age <= maxAge {
		return staleDate{}, false
	}
	return staleDate{AsOf: label, AgeDays: age}, true
}

func (r report) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n## Registry drift\n\n", marker)
	fmt.Fprintf(&b, "Checked on %s against OpenRouter's catalog (%d models). %d finding(s).\n\n",
		r.CheckedOn, r.CatalogSize, r.DriftCount)

	b.WriteString("### Referenced models missing or retiring upstream\n\n")
	if len(r.Missing) == 0 {
		b.WriteString("None.\n\n")
	} else {
		b.WriteString("| Referenced in | Model | Status |\n|---|---|---|\n")
		for _, m := range r.Missing {
			status := m.Status
			if m.Expires != "" {
				status += " on " + m.Expires
			}
			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", m.Source, m.Model, status)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "### Prices moved more than %g%% from registry/pricing.yaml\n\n", r.MaxMovePct)
	if len(r.PriceMoves) == 0 {
		b.WriteString("None.\n\n")
	} else {
		b.WriteString("| Tier | Model | Recorded in / out ($/M) | Upstream in / out ($/M) | Move in / out |\n|---|---|---|---|---|\n")
		for _, p := range r.PriceMoves {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s / %s | %s / %s | %s / %s |\n", p.Tier, p.Model,
				usd(p.RecordedInput), usd(p.RecordedOutput), usd(p.UpstreamInput), usd(p.UpstreamOutput),
				pct(p.InputMovePct), pct(p.OutputMovePct))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "### Pricing dates older than %d days\n\n", r.MaxAgeDays)
	if len(r.StaleDates) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, s := range r.StaleDates {
			if s.AsOf == "" {
				fmt.Fprintf(&b, "- `%s` states no \"as of\" date.\n", s.File)
				continue
			}
			fmt.Fprintf(&b, "- `%s`: as of %s, %d days old.\n", s.File, s.AsOf, s.AgeDays)
		}
	}
	return b.String()
}

func usd(f float64) string { return "$" + strconv.FormatFloat(f, 'f', -1, 64) }

func pct(p *float64) string {
	if p == nil {
		return "was free"
	}
	return fmt.Sprintf("%+.1f%%", *p)
}
