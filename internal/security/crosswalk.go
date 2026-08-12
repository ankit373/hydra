// SPDX-License-Identifier: MIT

package security

import "sort"

// One finding, every standard it bears on. Every entry is Curated: an
// assertion about what a standard means, never measured from data, and the two
// must not render alike. Shallow control-family mappings only, never subclauses.

// crosswalkTable maps a risk class to the framework controls it bears on.
var crosswalkTable = map[RiskClass][]FrameworkRef{
	ClassExposure: {
		{Framework: "OWASP LLM", Control: "LLM02 Sensitive Information Disclosure"},
		{Framework: "NIST AI RMF", Control: "MAP 5 — impacts characterised"},
		{Framework: "ISO/IEC 42001", Control: "A.7 Data for AI systems"},
		{Framework: "SOC 2", Control: "CC6.1 Logical access"},
		{Framework: "MITRE ATLAS", Control: "Exfiltration via AI service"},
	},
	ClassIncident: {
		{Framework: "OWASP LLM", Control: "LLM01 Prompt Injection"},
		{Framework: "NIST AI RMF", Control: "MANAGE 4 — incidents handled"},
		{Framework: "ISO/IEC 42001", Control: "A.10 Incident management"},
		{Framework: "SOC 2", Control: "CC7.3 Security incident evaluation"},
		{Framework: "MITRE ATLAS", Control: "Prompt injection / privilege escalation"},
	},
	ClassControl: {
		{Framework: "NIST AI RMF", Control: "GOVERN 1 — policies enacted"},
		{Framework: "ISO/IEC 42001", Control: "A.6 AI system lifecycle"},
		{Framework: "SOC 2", Control: "CC5.2 Control activities deployed"},
	},
	ClassPolicy: {
		{Framework: "OWASP LLM", Control: "LLM06 Excessive Agency"},
		{Framework: "NIST AI RMF", Control: "GOVERN 1 — policies enacted"},
		{Framework: "ISO/IEC 42001", Control: "A.9 Use of AI systems"},
		{Framework: "SOC 2", Control: "CC6.3 Least privilege"},
	},
	ClassSupplyChain: {
		{Framework: "OWASP LLM", Control: "LLM03 Supply Chain"},
		{Framework: "NIST AI RMF", Control: "MAP 4 — third-party risk"},
		{Framework: "ISO/IEC 42001", Control: "A.10 Third-party suppliers"},
		{Framework: "SOC 2", Control: "CC9.2 Vendor management"},
		{Framework: "MITRE ATLAS", Control: "ML supply chain compromise"},
	},
	ClassCoverage: {
		{Framework: "NIST AI RMF", Control: "MEASURE 2 — risks assessed"},
		{Framework: "ISO/IEC 42001", Control: "A.5 Risk assessment"},
	},
	ClassEvidence: {
		{Framework: "NIST AI RMF", Control: "MEASURE 1 — metrics documented"},
		{Framework: "ISO/IEC 42001", Control: "A.8 Information for interested parties"},
		{Framework: "SOC 2", Control: "CC4.1 Monitoring"},
	},
}

// crosswalk returns the framework references for a risk class, every one
// flagged as a curated assertion.
func crosswalk(c RiskClass) []FrameworkRef {
	src := crosswalkTable[c]
	if len(src) == 0 {
		return nil
	}
	out := make([]FrameworkRef, 0, len(src))
	for _, f := range src {
		f.Curated = true
		out = append(out, f)
	}
	return out
}

// Frameworks lists every framework this crosswalk can speak to, so a caller
// can report per-framework coverage without hardcoding the set.
func Frameworks() []string {
	seen := map[string]bool{}
	var out []string
	for _, refs := range crosswalkTable {
		for _, f := range refs {
			if !seen[f.Framework] {
				seen[f.Framework] = true
				out = append(out, f.Framework)
			}
		}
	}
	sort.Strings(out)
	return out
}

// FrameworkExposure counts open risks touching each framework — the
// "how do we look against X" answer, per framework, from one pass.
func FrameworkExposure(reg RiskRegister) map[string]int {
	out := map[string]int{}
	for _, k := range reg.Risks {
		if k.Status != StatusOpen {
			continue
		}
		for _, f := range k.Frameworks {
			out[f.Framework]++
		}
	}
	return out
}
