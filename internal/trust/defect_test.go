package trust

import "testing"

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

func TestDefectCost_HigherRiskCostsMore(t *testing.T) {
	dm := NewDefectModel()
	safe := dm.CostUSD(Task{Domain: "go"})
	risky := dm.CostUSD(Task{Domain: "go", Production: true, TouchesPII: true})
	if risky <= safe {
		t.Errorf("risky task (%.1f) should cost more than safe task (%.1f)", risky, safe)
	}
}
