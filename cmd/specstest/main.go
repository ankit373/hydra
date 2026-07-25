// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"github.com/ankit373/hydra/internal/sysinfo"
)

func main() {
	specs := sysinfo.Detect()
	fmt.Printf("Summary:      %s\n", specs.Summary())
	fmt.Printf("Note:         %s\n", specs.MemoryNote())
	fmt.Printf("Total RAM:    %.1f GB\n", specs.TotalRAMGB)
	fmt.Printf("Free RAM:     %.1f GB\n", specs.FreeRAMGB)
	fmt.Printf("Wired RAM:    %.1f GB\n", specs.WiredRAMGB)
	fmt.Printf("Effective:    %.1f GB\n", specs.EffectiveVRAMGB())
	fmt.Printf("Pressure:     %s\n", specs.MemPressure)
	if w := specs.PressureWarning(); w != "" {
		fmt.Printf("Warning:      %s\n", w)
	}
	fmt.Println("\nModel recommendations:")
	for _, r := range specs.OllamaRecommendations() {
		icon := "✗"
		if r.Fits {
			icon = "✓"
		}
		fmt.Printf("  %s %-26s %s\n", icon, r.DisplayName, r.Reason)
	}
	fmt.Printf("\nBest: %s\n", specs.BestOllamaModel().Model)
}
