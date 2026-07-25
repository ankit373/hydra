// SPDX-License-Identifier: MIT

package sysinfo

import (
	"fmt"
	"strings"
)

// ModelRecommendation is a ranked suggestion for a specific Ollama model.
type ModelRecommendation struct {
	Model        string  // ollama pull <model>
	DisplayName  string
	SizeB        int     // parameter count in billions
	RAMNeededGB  float64 // approximate RAM required (Q4_K_M quantisation)
	RAMAfterLoad float64 // estimated free RAM remaining after model is loaded
	Fits         bool    // fits comfortably on this machine
	Reason       string  // human explanation of why this was picked/rejected
	MemoryCost   string  // e.g. "will use ~5GB · 2.8GB left free"
}

// ollamaModels lists supported Ollama models ordered by capability (highest first).
// RAMNeededGB is conservative (Q4_K_M quantisation).
var ollamaModels = []ModelRecommendation{
	{Model: "qwen2.5-coder:72b",  DisplayName: "Qwen2.5-Coder 72B",  SizeB: 72,  RAMNeededGB: 45},
	{Model: "qwen3:30b",          DisplayName: "Qwen3 30B",           SizeB: 30,  RAMNeededGB: 20},
	{Model: "qwen2.5-coder:32b",  DisplayName: "Qwen2.5-Coder 32B",  SizeB: 32,  RAMNeededGB: 20},
	{Model: "qwen3:14b",          DisplayName: "Qwen3 14B",           SizeB: 14,  RAMNeededGB: 10},
	{Model: "qwen2.5-coder:14b",  DisplayName: "Qwen2.5-Coder 14B",  SizeB: 14,  RAMNeededGB: 10},
	{Model: "llama3.1:70b",       DisplayName: "Llama 3.1 70B",       SizeB: 70,  RAMNeededGB: 45},
	{Model: "qwen3:8b",           DisplayName: "Qwen3 8B",            SizeB: 8,   RAMNeededGB: 6},
	{Model: "qwen2.5-coder:7b",   DisplayName: "Qwen2.5-Coder 7B",   SizeB: 7,   RAMNeededGB: 5},
	{Model: "llama3.2:3b",        DisplayName: "Llama 3.2 3B",        SizeB: 3,   RAMNeededGB: 3},
	{Model: "qwen2.5-coder:1.5b", DisplayName: "Qwen2.5-Coder 1.5B", SizeB: 2,   RAMNeededGB: 2},
	{Model: "phi4-mini",          DisplayName: "Phi-4 Mini",          SizeB: 4,   RAMNeededGB: 3},
}

// OllamaRecommendations returns models ranked for this machine.
// The first entry with Fits=true is the primary recommendation.
func (s *Specs) OllamaRecommendations() []ModelRecommendation {
	effective := s.EffectiveVRAMGB()

	out := make([]ModelRecommendation, len(ollamaModels))
	for i, m := range ollamaModels {
		remaining := effective - m.RAMNeededGB
		m.Fits = remaining >= 0
		m.RAMAfterLoad = remaining
		m.Reason = s.reason(m, effective)
		m.MemoryCost = s.memoryCost(m, effective)
		out[i] = m
	}
	return out
}

// memoryCost returns a short string showing what loading this model will cost.
func (s *Specs) memoryCost(m ModelRecommendation, effectiveGB float64) string {
	remaining := effectiveGB - m.RAMNeededGB
	if !m.Fits {
		return fmt.Sprintf("needs ~%.0fGB · you have %.1fGB", m.RAMNeededGB, effectiveGB)
	}
	if remaining < 0.5 {
		return fmt.Sprintf("uses ~%.0fGB · %.1fGB left (very tight)", m.RAMNeededGB, remaining)
	}
	return fmt.Sprintf("uses ~%.0fGB · %.1fGB left free", m.RAMNeededGB, remaining)
}

// BestOllamaModel returns the single highest-capability model that fits.
// Returns Fits=false if nothing fits — callers should check AnyLocalModelFits first.
func (s *Specs) BestOllamaModel() ModelRecommendation {
	for _, m := range s.OllamaRecommendations() {
		if m.Fits {
			return m
		}
	}
	return ModelRecommendation{Fits: false, Reason: "insufficient memory for any local model"}
}

// AnyLocalModelFits reports whether at least one Ollama model fits in available memory.
func (s *Specs) AnyLocalModelFits() bool {
	for _, m := range s.OllamaRecommendations() {
		if m.Fits {
			return true
		}
	}
	return false
}

func (s *Specs) reason(m ModelRecommendation, effectiveGB float64) string {
	if !m.Fits {
		return fmt.Sprintf("needs ~%.0fGB, only %.1fGB free right now", m.RAMNeededGB, effectiveGB)
	}

	var where string
	switch {
	case s.IsAppleSilicon:
		where = fmt.Sprintf("%.1fGB available of %.0fGB unified memory", effectiveGB, s.TotalRAMGB)
	case s.GPUVRAMGB > 0:
		where = fmt.Sprintf("%.1fGB usable VRAM (%s)", effectiveGB, s.GPUName)
	default:
		where = fmt.Sprintf("%.1fGB free RAM (CPU inference)", effectiveGB)
	}

	pressureNote := ""
	if s.MemPressure == PressureModerate {
		pressureNote = " · memory moderate"
	} else if s.MemPressure == PressureHigh {
		pressureNote = " · memory tight, may swap"
	}

	return strings.TrimSpace(where + pressureNote)
}

// PressureWarning returns a warning string if memory pressure is high, else "".
func (s *Specs) PressureWarning() string {
	switch s.MemPressure {
	case PressureModerate:
		return fmt.Sprintf("Memory moderate (%.1fGB free) — close other apps for better performance.", s.FreeRAMGB)
	case PressureHigh:
		return fmt.Sprintf("Memory tight (%.1fGB free of %.0fGB) — smaller models recommended to avoid swapping.", s.FreeRAMGB, s.TotalRAMGB)
	}
	return ""
}

