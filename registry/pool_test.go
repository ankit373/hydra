package registry

import "testing"

// The head the port provider actually discovers is ollama/<model_flag>, not the
// registry's own id, and it carries no metadata at all. Matching only on id is
// what left every Ollama row with an empty pool (#681).
func TestTokenPoolFor_MatchesAPortDiscoveredHead(t *testing.T) {
	if got := TokenPoolFor("", "ollama/Qwen2.5-Coder:7b"); got != "local_ollama" {
		t.Errorf("TokenPoolFor(ollama/Qwen2.5-Coder:7b) = %q, want local_ollama", got)
	}
}

func TestTokenPoolFor_MatchesTheRegistryID(t *testing.T) {
	if got := TokenPoolFor("", "qwen-grunt"); got != "local_ollama" {
		t.Errorf("TokenPoolFor(qwen-grunt) = %q, want local_ollama", got)
	}
}

// An unknown head must return "" so the caller records "not declared" rather
// than filing spend against a pool that is not its own.
func TestTokenPoolFor_UnknownHeadIsEmpty(t *testing.T) {
	if got := TokenPoolFor("", "something/not-in-the-registry"); got != "" {
		t.Errorf("TokenPoolFor(unknown) = %q, want empty", got)
	}
	if got := TokenPoolFor("", ""); got != "" {
		t.Errorf("TokenPoolFor(\"\") = %q, want empty", got)
	}
}
