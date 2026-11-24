package configstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testStrategiesDoc = `{
  "schemaVersion": 4,
  "active_strategies": ["raschke"],
  "watchlist": ["QQQ"],
  "playbook": {"id": "us_futures", "name": "US Futures Multi-Index", "description": "IB"},
  "beat": {"enabled": true, "symbols": ["QQQ"]},
  "trading_rules": {"enabled": false, "market_mandate": ["US only"]},
  "strategies": {
    "raschke": {"name": "Raschke", "enabled": true, "whitelist_symbols": ["QQQ"], "blacklist_symbols": ["AAPL"], "min_rr": 2.5, "max_risk_pct": 0.75},
    "fit_mom": {"enabled": false}
  },
  "global_filters": {"max_trades_per_day": 2}
}`

func setupStrategyStore(t *testing.T) *configStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "strategies.json")
	if err := os.WriteFile(path, []byte(testStrategiesDoc), 0644); err != nil {
		t.Fatalf("write strategies.json: %v", err)
	}
	old := store
	s := &configStore{path: filepath.Join(dir, "agent-config.json"), config: &Config{}}
	store = s
	t.Cleanup(func() { store = old })
	s.applyStrategyProjection(path)
	return s
}

func readDocMap(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read strategies.json: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("strategies.json is not valid JSON: %v", err)
	}
	return doc
}

func TestUpdateStrategyWritesFileAndPreservesBlocks(t *testing.T) {
	s := setupStrategyStore(t)
	path := filepath.Join(filepath.Dir(s.path), "strategies.json")

	updated, err := UpdateStrategy("raschke", map[string]interface{}{
		"enabled":           false,
		"whitelist_symbols": []interface{}{"SPY", "QQQ"},
	})
	if err != nil {
		t.Fatalf("UpdateStrategy: %v", err)
	}
	if updated.ID != "raschke" || updated.Name != "Raschke" {
		t.Fatalf("unexpected projection: %+v", updated)
	}

	doc := readDocMap(t, path)
	if _, ok := doc["trading_rules"]; !ok {
		t.Fatal("trading_rules block was lost")
	}
	if _, ok := doc["beat"]; !ok {
		t.Fatal("beat block was lost")
	}
	if _, ok := doc["global_filters"]; !ok {
		t.Fatal("global_filters block was lost")
	}

	var strategies map[string]map[string]interface{}
	json.Unmarshal(doc["strategies"], &strategies)
	if strategies["raschke"]["enabled"] != false {
		t.Fatalf("enabled was not updated: %+v", strategies["raschke"])
	}
	wl, _ := strategies["raschke"]["whitelist_symbols"].([]interface{})
	if len(wl) != 2 || wl[0] != "SPY" {
		t.Fatalf("whitelist was not updated: %+v", strategies["raschke"]["whitelist_symbols"])
	}
	if strategies["raschke"]["name"] != "Raschke" {
		t.Fatal("name should be preserved")
	}
}

func TestRemoveStrategyGuards(t *testing.T) {
	setupStrategyStore(t)

	if err := RemoveStrategy("raschke"); err == nil || !strings.Contains(err.Error(), "activa") {
		t.Fatalf("removing an active strategy must fail, got %v", err)
	}
	if err := RemoveStrategy("us_futures"); err == nil || !strings.Contains(err.Error(), "playbook") {
		t.Fatalf("removing the playbook must fail, got %v", err)
	}
	if err := RemoveStrategy("fit_mom"); err != nil {
		t.Fatalf("removing an inactive strategy failed: %v", err)
	}
	if _, err := GetStrategyDefinition("fit_mom"); err == nil {
		t.Fatal("fit_mom should be gone")
	}
}

func TestAddStrategyCreatesDisabledEntry(t *testing.T) {
	setupStrategyStore(t)

	created, err := AddStrategy(Strategy{Name: "My Edge", Description: "test edge"})
	if err != nil {
		t.Fatalf("AddStrategy: %v", err)
	}
	if created.ID != "my_edge" {
		t.Fatalf("slug id = %q, want my_edge", created.ID)
	}
	def, err := GetStrategyDefinition("my_edge")
	if err != nil {
		t.Fatalf("GetStrategyDefinition: %v", err)
	}
	if def["enabled"] != false {
		t.Fatalf("new strategies must ship disabled: %+v", def)
	}
	if def["name"] != "My Edge" {
		t.Fatalf("name = %v", def["name"])
	}
	if _, ok := def["operate_allow"]; !ok {
		t.Fatalf("new strategies must include operate_allow: %+v", def)
	}
}

func TestGetWatchlistFallsBackToLegacyKeys(t *testing.T) {
	setupStrategyStore(t)

	snapshot, err := GetWatchlist()
	if err != nil {
		t.Fatalf("GetWatchlist: %v", err)
	}
	if len(snapshot.ObserveSymbols) != 1 || snapshot.ObserveSymbols[0] != "QQQ" {
		t.Fatalf("legacy watchlist fallback failed: %+v", snapshot.ObserveSymbols)
	}
	perms, ok := snapshot.Strategies["raschke"]
	if !ok {
		t.Fatalf("raschke permissions missing: %+v", snapshot.Strategies)
	}
	if len(perms.OperateAllow) != 1 || perms.OperateAllow[0] != "QQQ" {
		t.Fatalf("legacy whitelist fallback failed: %+v", perms)
	}
	if len(perms.OperateBlock) != 1 || perms.OperateBlock[0] != "AAPL" {
		t.Fatalf("legacy blacklist fallback failed: %+v", perms)
	}
}

func TestUpdateWatchlistWritesCanonicalKeys(t *testing.T) {
	s := setupStrategyStore(t)
	path := filepath.Join(filepath.Dir(s.path), "strategies.json")

	snapshot, err := UpdateWatchlist(WatchlistUpdate{
		ObserveSymbols: []string{"qqq", "SPY", "QQQ"},
		BeatSymbols:    []string{"QQQ"},
		Strategies: map[string]StrategySymbolPermissions{
			"raschke": {OperateAllow: []string{"QQQ", "SPY"}, OperateBlock: []string{"AAPL"}},
		},
	})
	if err != nil {
		t.Fatalf("UpdateWatchlist: %v", err)
	}
	if len(snapshot.ObserveSymbols) != 2 || snapshot.ObserveSymbols[0] != "QQQ" || snapshot.ObserveSymbols[1] != "SPY" {
		t.Fatalf("observe symbols not normalized: %+v", snapshot.ObserveSymbols)
	}

	doc := readDocMap(t, path)
	if _, ok := doc["watchlist"]; ok {
		t.Fatal("legacy watchlist key should be removed after canonical save")
	}
	if _, ok := doc["observe_symbols"]; !ok {
		t.Fatal("observe_symbols key missing")
	}
	var strategies map[string]map[string]interface{}
	json.Unmarshal(doc["strategies"], &strategies)
	if _, ok := strategies["raschke"]["operate_allow"]; !ok {
		t.Fatalf("operate_allow missing: %+v", strategies["raschke"])
	}
	if _, ok := strategies["raschke"]["whitelist_symbols"]; ok {
		t.Fatalf("legacy whitelist_symbols should be removed: %+v", strategies["raschke"])
	}
}

func TestUpdateWatchlistRejectsBeatOutsideObserve(t *testing.T) {
	setupStrategyStore(t)
	if _, err := UpdateWatchlist(WatchlistUpdate{
		ObserveSymbols: []string{"QQQ"},
		BeatSymbols:    []string{"SPY"},
	}); err == nil || !strings.Contains(err.Error(), "not in Observe") {
		t.Fatalf("expected beat subset validation, got %v", err)
	}
}

func TestUpdatePlaybookIsRejected(t *testing.T) {
	setupStrategyStore(t)
	if _, err := UpdateStrategy("us_futures", map[string]interface{}{"name": "x"}); err == nil || !strings.Contains(err.Error(), "solo lectura") {
		t.Fatalf("playbook update must be rejected, got %v", err)
	}
}
