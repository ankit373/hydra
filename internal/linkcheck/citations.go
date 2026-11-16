// SPDX-License-Identifier: MIT

package linkcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// CitationsSource is the calibration key the citations check records under.
const CitationsSource = "check:citations"

const (
	maxCitations = 20
	cacheFor     = 24 * time.Hour
	// https://citation.doi.org/docs.html: CSL JSON by content negotiation, 404 for a DOI that does not exist.
	doiAccept = "application/vnd.citationstyles.csl+json"
)

// Service endpoints, vars so a test can stand in for them.
// arXiv: https://info.arxiv.org/help/api/user-manual.html, id_list takes a comma list.
var (
	doiBase   = "https://doi.org/"
	arxivBase = "https://export.arxiv.org/api/query?max_results=100&id_list="
)

var (
	errNoCitations = errors.New("no DOI or arXiv id in the answer")
	doiRE          = regexp.MustCompile(`\b10\.\d{4,9}/[^\s"'“”<>]+`)
	arxivRE        = regexp.MustCompile(`(?i)(?:arxiv:\s*|arxiv\.org/(?:abs|pdf)/)([a-z-]+(?:\.[a-z]{2})?/\d{7}|\d{4}\.\d{4,5})(v\d+)?`)
)

// Citations checks that each cited DOI and arXiv id exists, and that a quoted title in its
// sentence matches the record. Lookups are cached in CacheDir for a day; "" caches nothing.
type Citations struct {
	Gate     Gate
	CacheDir string
}

// Key is the calibration source this oracle records under.
func (c *Citations) Key() string { return CitationsSource }

// record is what a service said about one id.
type record struct {
	Found bool      `json:"found"`
	Title string    `json:"title,omitempty"`
	At    time.Time `json:"at"`
}

type citation struct{ kind, id, sentence string }

// Verify fails on an id that does not exist or a quoted title that differs from the record.
// No ids, a denied fetch or a service that did not answer is not checked.
func (c *Citations) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	var cites []citation
	for _, s := range ground.Sentences(candidate) {
		for _, d := range doiRE.FindAllString(s, -1) {
			cites = append(cites, citation{"doi", strings.TrimRight(d, ".,;:)]"), s})
		}
		for _, m := range arxivRE.FindAllStringSubmatch(s, -1) {
			cites = append(cites, citation{"arxiv", m[1], s})
		}
	}
	if len(cites) == 0 {
		return oracle.Verdict{}, errNoCitations
	}
	if len(cites) > maxCitations {
		return oracle.Verdict{}, fmt.Errorf("partly checked: %d citations, at most %d are looked up", len(cites), maxCitations)
	}
	recs, err := c.lookup(ctx, cites)
	if err != nil {
		return oracle.Verdict{}, err
	}
	var failed []string
	for _, ct := range cites {
		r := recs[ct.kind+":"+ct.id]
		name := ct.kind + " " + ct.id
		if !r.Found {
			failed = append(failed, name+" does not exist")
			continue
		}
		titles := ground.Quotes(ct.sentence)
		if len(titles) == 0 {
			continue
		}
		matched := false
		for _, t := range titles {
			matched = matched || foldTitle(t) == foldTitle(r.Title)
		}
		if !matched {
			failed = append(failed, fmt.Sprintf("%s is titled %q, not %q", name, r.Title, titles[0]))
		}
	}
	if len(failed) > 0 {
		return oracle.Verdict{Detail: "citations failed: " + strings.Join(failed, "; ")}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("all %d citations exist", len(recs))}, nil
}

// lookup answers each id from the cache, then DOIs one by one and every arXiv id in one request.
func (c *Citations) lookup(ctx context.Context, cites []citation) (map[string]record, error) {
	out := map[string]record{}
	var arxiv []string
	for _, ct := range cites {
		key := ct.kind + ":" + ct.id
		if _, done := out[key]; done {
			continue
		}
		if r, ok := c.cached(key); ok {
			out[key] = r
			continue
		}
		if ct.kind == "arxiv" {
			out[key] = record{} // filled below
			arxiv = append(arxiv, ct.id)
			continue
		}
		r, err := c.doi(ctx, ct.id)
		if err != nil {
			return nil, fmt.Errorf("doi %s: %w", ct.id, err)
		}
		out[key] = r
		c.store(key, r)
	}
	if len(arxiv) == 0 {
		return out, nil
	}
	found, err := c.arxiv(ctx, arxiv)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	for _, id := range arxiv {
		r := record{At: time.Now()}
		if t, ok := found[id]; ok {
			r.Found, r.Title = true, t
		}
		out["arxiv:"+id] = r
		c.store("arxiv:"+id, r)
	}
	return out, nil
}

func (c *Citations) doi(ctx context.Context, id string) (record, error) {
	r, err := get(ctx, c.Gate, doiBase+id, doiAccept)
	switch {
	case err != nil:
		return record{}, err
	case r.status == 404:
		return record{At: time.Now()}, nil
	case r.failed != "" || r.status < 200 || r.status > 299:
		return record{}, fmt.Errorf("doi.org answered %d %s", r.status, r.failed)
	}
	var meta struct {
		Title json.RawMessage `json:"title"`
	}
	if err := json.Unmarshal(r.body, &meta); err != nil {
		return record{}, fmt.Errorf("doi.org metadata: %w", err)
	}
	// CSL JSON gives title as a string; some registries send a list.
	var title string
	if json.Unmarshal(meta.Title, &title) != nil {
		var titles []string
		if json.Unmarshal(meta.Title, &titles) == nil && len(titles) > 0 {
			title = titles[0]
		}
	}
	return record{Found: true, Title: title, At: time.Now()}, nil
}

// arxiv asks for every id at once and returns the title of each one found, by the id as asked.
func (c *Citations) arxiv(ctx context.Context, ids []string) (map[string]string, error) {
	r, err := get(ctx, c.Gate, arxivBase+strings.Join(ids, ","), "") // ids match arxivRE, so need no escaping
	if err != nil {
		return nil, err
	}
	if r.failed != "" || r.status < 200 || r.status > 299 {
		return nil, fmt.Errorf("export.arxiv.org answered %d %s", r.status, r.failed)
	}
	var feed struct {
		Entries []struct {
			ID    string `xml:"id"`
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(r.body, &feed); err != nil {
		return nil, fmt.Errorf("export.arxiv.org feed: %w", err)
	}
	out := map[string]string{}
	for _, e := range feed.Entries {
		// An error entry, e.g. a malformed id, says nothing about the others.
		if strings.Contains(e.ID, "/api/errors") {
			return nil, errors.New("export.arxiv.org: " + strings.TrimSpace(e.Title))
		}
		abs := e.ID[strings.LastIndex(e.ID, "/abs/")+len("/abs/"):]
		for _, id := range ids {
			if strings.HasPrefix(abs, id) && (len(abs) == len(id) || abs[len(id)] == 'v') {
				out[id] = strings.Join(strings.Fields(e.Title), " ")
			}
		}
	}
	return out, nil
}

func (c *Citations) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.CacheDir, hex.EncodeToString(sum[:8])+".json")
}

func (c *Citations) cached(key string) (record, bool) {
	if c.CacheDir == "" {
		return record{}, false
	}
	raw, err := os.ReadFile(c.path(key))
	var r record
	if err != nil || json.Unmarshal(raw, &r) != nil || time.Since(r.At) > cacheFor {
		return record{}, false
	}
	return r, true
}

// store keeps a lookup for a day. A failed write only costs a repeat lookup.
func (c *Citations) store(key string, r record) {
	if c.CacheDir == "" || os.MkdirAll(c.CacheDir, 0o700) != nil {
		return
	}
	if raw, err := json.Marshal(r); err == nil {
		_ = os.WriteFile(c.path(key), raw, 0o600)
	}
}

// foldTitle lowercases a title and keeps only its letters and digits, one space between words.
func foldTitle(t string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}
