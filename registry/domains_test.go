// SPDX-License-Identifier: MIT

package registry

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// domainProblems lists every task_routing value that is not a routing key, every
// name or alias claimed twice, and domains out of alphabetical order.
func domainProblems(raw []byte, keys map[string]int) ([]string, error) {
	var doc struct {
		Domains yaml.Node `yaml:"domains"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out, names []string
	claimed := map[string]string{}
	claim := func(word, domain string) {
		if prev, ok := claimed[word]; ok {
			out = append(out, fmt.Sprintf("%q is claimed by both %s and %s", word, prev, domain))
			return
		}
		claimed[word] = domain
	}
	n := doc.Domains.Content
	for i := 0; i+1 < len(n); i += 2 {
		name := n[i].Value
		names = append(names, name)
		var d struct {
			Aliases     []string          `yaml:"aliases"`
			TaskRouting map[string]string `yaml:"task_routing"`
		}
		if err := n[i+1].Decode(&d); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		claim(name, name)
		for _, a := range d.Aliases {
			claim(a, name)
		}
		for task, key := range d.TaskRouting {
			if _, ok := keys[key]; !ok {
				out = append(out, fmt.Sprintf("%s.%s routes to %q, which is not a routing.yaml key", name, task, key))
			}
		}
	}
	if !sort.StringsAreSorted(names) {
		out = append(out, "domains are not in alphabetical order: "+strings.Join(names, ", "))
	}
	sort.Strings(out)
	return out, nil
}

// Nothing at runtime reads domains.yaml, so a typo in a key would ship
// silently and send whoever reads the table to an --enum that does not exist.
func TestDomains_EveryTaskRoutingValueIsARoutingKey(t *testing.T) {
	home := t.TempDir() // no override, so the embedded copies
	keys, err := EnumTiers(home)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Read(home, "domains.yaml")
	if err != nil {
		t.Fatal(err)
	}
	problems, err := domainProblems(raw, keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// The check itself must go red, or the test above proves nothing.
func TestDomainProblems_CatchesBadKeyClashAndOrder(t *testing.T) {
	raw := []byte(`domains:
  support:
    aliases: [helpdesk]
    task_routing:
      faq_reply: SIMPEL
  legal:
    aliases: [helpdesk]
    task_routing:
      clause_lookup: SIMPLE
`)
	got, err := domainProblems(raw, map[string]int{"SIMPLE": 8})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`"helpdesk" is claimed by both support and legal`,
		"domains are not in alphabetical order: support, legal",
		`support.faq_reply routes to "SIMPEL", which is not a routing.yaml key`,
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
