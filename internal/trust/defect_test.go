package trust

import (
	"math"
	"testing"
)

func TestDefectCostUSD(t *testing.T) {
	dm := NewDefectModel() // Base 1, Irrev 5, PII 10, Prod 3

	tests := []struct {
		name string
		task Task
		want float64
	}{
		{"local reversible non-prod", Task{Domain: "go"}, 1},
		{"blast only", Task{BlastRadius: 4}, 4},
		{"production only", Task{Production: true}, 3},
		{"irreversible + pii, blast 2", Task{BlastRadius: 2, Irreversible: true, TouchesPII: true}, 100},
		{"everything, blast 1", Task{BlastRadius: 1, Irreversible: true, TouchesPII: true, Production: true}, 150},
		{"zero blast treated as local", Task{BlastRadius: 0, Production: true}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dm.CostUSD(tt.task); got != tt.want {
				t.Errorf("CostUSD(%+v) = %v, want %v", tt.task, got, tt.want)
			}
		})
	}
}

func TestRequiredConfidence_HoldsLeakConstant(t *testing.T) {
	dm := NewDefectModel() // tolerated leak $0.10

	// Baseline task (defect cost = Base = 1) → α=0.10 → 90%.
	if got := dm.RequiredConfidence(Task{}); math.Abs(got-0.90) > 1e-9 {
		t.Errorf("RequiredConfidence(base) = %v, want 0.90", got)
	}
	// blast 2 → cost 2 → α=0.05 → 95%.
	if got := dm.RequiredConfidence(Task{BlastRadius: 2}); math.Abs(got-0.95) > 1e-9 {
		t.Errorf("RequiredConfidence(blast 2) = %v, want 0.95", got)
	}
	// The invariant: α × defect cost ≈ tolerated leak, for un-capped targets.
	for _, blast := range []float64{1, 2, 5, 10} {
		task := Task{BlastRadius: blast}
		alpha := 1 - dm.RequiredConfidence(task)
		if leak := alpha * dm.CostUSD(task); math.Abs(leak-0.10) > 1e-9 {
			t.Errorf("blast %v: expected leaked cost α·C = %v, want 0.10", blast, leak)
		}
	}
}

func TestRequiredConfidence_CappedAtMax(t *testing.T) {
	dm := NewDefectModel()
	// Enormous defect cost (all flags + high blast) would demand α→0; capped.
	got := dm.RequiredConfidence(Task{BlastRadius: 50, Irreversible: true, TouchesPII: true, Production: true})
	if got > maxConfidence {
		t.Errorf("RequiredConfidence = %v, want ≤ %v (capped)", got, maxConfidence)
	}
	if got < 0.99 {
		t.Errorf("very costly task should still demand ≥0.99, got %v", got)
	}
}

func TestDefectCost_HigherRiskCostsMore(t *testing.T) {
	dm := NewDefectModel()
	safe := dm.CostUSD(Task{Domain: "go"})
	risky := dm.CostUSD(Task{Domain: "go", Production: true, TouchesPII: true})
	if risky <= safe {
		t.Errorf("risky task (%.1f) should cost more than safe task (%.1f)", risky, safe)
	}
}
