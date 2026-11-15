// SPDX-License-Identifier: MIT

package provider

import "strings"

// Family is the vendor behind a head: a LiteLLM head's upstream, else the vendor
// prefix of a "vendor/model" id, else the provider. Every model behind one local server reads as one family.
func Family(h Head) string {
	if up := h.Meta["litellm_upstream"]; up != "" {
		return up
	}
	if vendor, _, ok := strings.Cut(h.Meta["model"], "/"); ok && vendor != "" {
		return vendor
	}
	return h.Provider
}
