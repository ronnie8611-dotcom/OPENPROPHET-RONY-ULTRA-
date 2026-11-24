package services

import "testing"

func TestStrategyEngineAndActiveIDs(t *testing.T) {
	cfg := &StrategyConfig{
		ActiveStrategies: []string{"raschke"},
		Strategies: map[string]interface{}{
			"raschke": map[string]interface{}{"enabled": true},
			"my_edge": map[string]interface{}{"enabled": true, "engine": "mean_reversion"},
			"off":     map[string]interface{}{"enabled": false},
		},
	}

	if got := strategyEngine(cfg, "my_edge"); got != "mean_reversion" {
		t.Fatalf("strategyEngine(my_edge) = %q, want mean_reversion", got)
	}
	if got := strategyEngine(cfg, "raschke"); got != "raschke" {
		t.Fatalf("strategyEngine(raschke) = %q, want raschke", got)
	}
	if got := strategyEngine(nil, "x"); got != "x" {
		t.Fatalf("strategyEngine(nil) = %q, want x", got)
	}

	ids := ActiveStrategyIDs(cfg)
	if len(ids) != 2 || ids[0] != "raschke" || ids[1] != "my_edge" {
		t.Fatalf("ActiveStrategyIDs = %v, want [raschke my_edge]", ids)
	}
}
