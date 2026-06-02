// Package sysinfo detects hardware specs relevant to AI model selection.
package sysinfo

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Pressure represents current memory pressure level.
type Pressure int

const (
	PressureLow      Pressure = iota // plenty of headroom
	PressureModerate                 // usable but getting tight
	PressureHigh                     // constrained; only small models safe
)

func (p Pressure) String() string {
	return [...]string{"low", "moderate", "high"}[p]
}

// Specs describes the hardware and current memory state relevant to AI model selection.
type Specs struct {
	TotalRAMGB     float64
	FreeRAMGB      float64  // recoverable memory right now (free + reclaimable)
	WiredRAMGB     float64  // locked by OS/kernel — cannot be reclaimed
	GPUVRAMGB      float64  // 0 if no discrete GPU detected
	IsAppleSilicon bool
	GPUName        string
	Arch           string
	OS             string
	MemPressure    Pressure
	History        *HistoricalStats // nil if no history yet
}

// osMinBufferGB is the minimum we always keep free for OS stability.
// Sized generously — better to under-recommend than cause swapping.
const osMinBufferGB = 1.5

// EffectiveVRAMGB returns how much memory is realistically available for a model.
//
// Priority order:
//  1. Discrete GPU VRAM (separate from system RAM, very reliable)
//  2. 7-day historical P75 free memory (most accurate — reflects real usage patterns)
//  3. Current free memory snapshot (accurate but a single moment in time)
//  4. Conservative fraction of total RAM (last resort when nothing else is known)
func (s *Specs) EffectiveVRAMGB() float64 {
	if s.GPUVRAMGB > 0 && !s.IsAppleSilicon {
		return s.GPUVRAMGB * 0.90 // 10% headroom for driver overhead
	}

	// Determine the best free-memory estimate.
	available := s.bestFreeEstimate()

	usable := available - osMinBufferGB
	if usable < 0 {
		return 0
	}

	// Hard ceiling: never use more than 85% of total, regardless of what free says.
	if ceiling := s.TotalRAMGB * 0.85; usable > ceiling {
		usable = ceiling
	}
	return usable
}

// bestFreeEstimate returns the most reliable estimate of recoverable free memory.
func (s *Specs) bestFreeEstimate() float64 {
	// Historical P75 is the most trustworthy signal when we have enough data.
	if s.History != nil && s.History.Reliable {
		return s.History.P75FreeGB
	}
	// Fall back to current snapshot.
	if s.FreeRAMGB > 0 {
		return s.FreeRAMGB
	}
	// Nothing known — be conservative.
	return s.TotalRAMGB * 0.55
}

// UsingHistoricalData reports whether recommendations are based on historical
// averages (true) or just the current snapshot (false).
func (s *Specs) UsingHistoricalData() bool {
	return s.History != nil && s.History.Reliable
}

// MemoryNote returns a human-readable explanation of the effective value.
func (s *Specs) MemoryNote() string {
	eff := s.EffectiveVRAMGB()
	switch {
	case s.GPUVRAMGB > 0 && !s.IsAppleSilicon:
		return fmt.Sprintf("%.1fGB VRAM usable (10%% reserved for driver overhead)", eff)
	case s.FreeRAMGB > 0:
		used := s.TotalRAMGB - s.FreeRAMGB
		if eff < 0.1 {
			return fmt.Sprintf("%.1fGB free · all reserved for OS stability · close other apps to free space for local models",
				s.FreeRAMGB)
		}
		return fmt.Sprintf("%.1fGB usable · %.1fGB in use by other processes · %.1fGB OS buffer",
			eff, used, osMinBufferGB)
	default:
		return fmt.Sprintf("%.1fGB estimated usable (%.0fGB total, current usage unknown)", eff, s.TotalRAMGB)
	}
}

// Summary returns a one-line hardware description for display.
func (s *Specs) Summary() string {
	eff := s.EffectiveVRAMGB()
	switch {
	case s.IsAppleSilicon:
		if eff < 0.1 {
			return fmt.Sprintf("Apple Silicon · %.0fGB total · memory fully occupied (%.1fGB free)", s.TotalRAMGB, s.FreeRAMGB)
		}
		return fmt.Sprintf("Apple Silicon · %.0fGB total · %.1fGB free for models", s.TotalRAMGB, eff)
	case s.GPUVRAMGB > 0:
		return fmt.Sprintf("%.0fGB RAM · %.0fGB VRAM (%s) · %.1fGB usable", s.TotalRAMGB, s.GPUVRAMGB, s.GPUName, eff)
	default:
		if eff < 0.1 {
			return fmt.Sprintf("%.0fGB RAM · memory fully occupied (%.1fGB free)", s.TotalRAMGB, s.FreeRAMGB)
		}
		return fmt.Sprintf("%.0fGB RAM · %.1fGB free for models (CPU inference)", s.TotalRAMGB, eff)
	}
}

// Detect reads hardware specs and current memory state from the OS.
// Never returns an error — conservative estimates are returned on failure.
func Detect() *Specs {
	s := &Specs{
		Arch: runtime.GOARCH,
		OS:   runtime.GOOS,
	}
	s.IsAppleSilicon = runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"

	switch runtime.GOOS {
	case "darwin":
		s.TotalRAMGB = darwinRAM()
		s.FreeRAMGB, s.WiredRAMGB = darwinMemoryState()
		if !s.IsAppleSilicon {
			s.GPUVRAMGB, s.GPUName = darwinGPUVRAM()
		}
	case "linux":
		s.TotalRAMGB = linuxRAM()
		s.FreeRAMGB = linuxFreeRAM()
		s.GPUVRAMGB, s.GPUName = nvidiaVRAM()
	}

	s.History = LoadHistory()
	recordSample(s) // append current reading (rate-limited to once/hour)
	s.MemPressure = s.computePressure()
	return s
}

func (s *Specs) computePressure() Pressure {
	if s.TotalRAMGB == 0 {
		return PressureLow
	}
	ratio := s.EffectiveVRAMGB() / s.TotalRAMGB
	switch {
	case ratio > 0.45:
		return PressureLow
	case ratio > 0.20:
		return PressureModerate
	default:
		return PressureHigh
	}
}

// ── macOS ─────────────────────────────────────────────────────────────────────

func darwinRAM() float64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 8
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 8
	}
	return float64(bytes) / (1 << 30)
}

// darwinMemoryState returns (freeGB, wiredGB) by parsing vm_stat.
// "free" here = free_pages + inactive_pages (reclaimable by OS).
// "wired" = wired_pages (locked by kernel, not reclaimable).
func darwinMemoryState() (freeGB, wiredGB float64) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, 0
	}

	// Apple Silicon page size is 16384 bytes; Intel is 4096.
	// vm_stat prints "Mach Virtual Memory Statistics: (page size of N bytes)"
	pageSize := 16384.0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "page size of") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "bytes)" && i > 0 {
					if v, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
						pageSize = v
					}
				}
			}
			break
		}
	}

	pages := parseVmStatFields(string(out))
	toGB := func(p float64) float64 { return p * pageSize / (1 << 30) }

	free     := pages["Pages free"]
	inactive := pages["Pages inactive"]   // reclaimable
	wired    := pages["Pages wired down"] // locked

	return toGB(free + inactive), toGB(wired)
}

func parseVmStatFields(output string) map[string]float64 {
	result := map[string]float64{}
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."))
		if v, err := strconv.ParseFloat(val, 64); err == nil {
			result[key] = v
		}
	}
	return result
}

func darwinGPUVRAM() (float64, string) {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		return 0, ""
	}
	lines := strings.Split(string(out), "\n")
	var name string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Chipset Model:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "Chipset Model:"))
		}
		if strings.Contains(line, "VRAM") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if (f == "GB" || f == "MB") && i > 0 {
					v, err := strconv.ParseFloat(fields[i-1], 64)
					if err != nil {
						continue
					}
					if f == "MB" {
						v /= 1024
					}
					return v, name
				}
			}
		}
	}
	return 0, name
}

// ── Linux ─────────────────────────────────────────────────────────────────────

func linuxRAM() float64 {
	out, err := exec.Command("grep", "MemTotal", "/proc/meminfo").Output()
	if err != nil {
		return 8
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 8
	}
	kb, _ := strconv.ParseFloat(fields[1], 64)
	return kb / (1 << 20)
}

func linuxFreeRAM() float64 {
	// MemAvailable is a kernel estimate of how much can be freed without swapping.
	out, err := exec.Command("grep", "MemAvailable", "/proc/meminfo").Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0
	}
	kb, _ := strconv.ParseFloat(fields[1], 64)
	return kb / (1 << 20)
}

func nvidiaVRAM() (float64, string) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=memory.total,name",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.SplitN(line, ", ", 2)
	if len(parts) < 2 {
		return 0, ""
	}
	mb, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, ""
	}
	return mb / 1024, strings.TrimSpace(parts[1])
}
