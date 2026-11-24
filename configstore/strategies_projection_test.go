package configstore

import (
	"encoding/json"
	"os"
	"testing"
)

func TestBuildStrategyProjectionOrderAndNames(t *testing.T) {
	raw := []byte(`{
	  "active_strategies": ["raschke", "mean_reversion"],
	  "playbook": {"id": "us_futures", "name": "US Futures Multi-Index", "description": "IB & VWAP"},
	  "strategies": {
	    "raschke": {"name": "Raschke Volatility Breakout", "enabled": true, "description": "Sharpe 3.48"},
	    "mean_reversion": {"name": "Mean Reversion", "enabled": true},
	    "fit_mom": {"enabled": false}
	  }
	}`)

	got := buildStrategyProjection(raw, "data/strategies.json")
	wantOrder := []string{"us_futures", "raschke", "mean_reversion", "fit_mom"}
	if len(got) != len(wantOrder) {
		t.Fatalf("projection len = %d, want %d (%+v)", len(got), len(wantOrder), got)
	}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Fatalf("position %d = %q, want %q", i, got[i].ID, id)
		}
	}
	if got[0].Name != "US Futures Multi-Index" {
		t.Fatalf("playbook name = %q", got[0].Name)
	}
	if got[2].Name != "Mean Reversion" {
		t.Fatalf("mean_reversion name = %q", got[2].Name)
	}
	if got[3].Name != "fit_mom" {
		t.Fatalf("missing name must fall back to the id, got %q", got[3].Name)
	}
	if got[0].RulesFile == nil || *got[0].RulesFile != "data/strategies.json" {
		t.Fatalf("rulesFile = %v, want data/strategies.json", got[0].RulesFile)
	}
}

func TestBuildStrategyProjectionInvalidJSON(t *testing.T) {
	if got := buildStrategyProjection([]byte("{"), "x"); got != nil {
		t.Fatalf("invalid JSON must return nil, got %+v", got)
	}
}

func TestAgentConfigNoLongerDefinesStrategies(t *testing.T) {
	raw, err := os.ReadFile("../data/agent-config.json")
	if err != nil {
		t.Fatalf("cannot read agent-config.json: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("agent-config.json is not valid JSON: %v", err)
	}
	if _, ok := probe["strategies"]; ok {
		t.Fatal("agent-config.json must not define strategies anymore (single source: data/strategies.json)")
	}
}

func TestRealStrategiesProjectionIncludesActive(t *testing.T) {
	raw, err := os.ReadFile("../data/strategies.json")
	if err != nil {
		t.Fatalf("cannot read strategies.json: %v", err)
	}
	got := buildStrategyProjection(raw, "data/strategies.json")
	ids := make(map[string]bool, len(got))
	for _, s := range got {
		ids[s.ID] = true
	}
	for _, want := range []string{"raschke", "mean_reversion"} {
		if !ids[want] {
			t.Fatalf("projection is missing %q: %+v", want, got)
		}
	}
	if ids["us_futures"] {
		t.Fatalf("us_futures playbook was removed and must not appear: %+v", got)
	}
}
