// SPDX-License-Identifier: MIT

package ground

// Quotes are the phrases of three or more words text puts in double quotes, the ones Check checks.
func Quotes(text string) []string { return quotes(text) }

// QuoteIn says whether source holds q word for word, by Check's rule: case, spacing and an ellipsis aside.
func QuoteIn(q, source string) bool { return quoted(q, normalise(source)) }
