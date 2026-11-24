package services

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTradingRulesPromptDisabledDoesNotLeak(t *testing.T) {
	cfg := &StrategyConfig{
		TradingRules: TradingRulesConfig{
			Enabled:       false,
			MarketMandate: []string{"US only"},
			Setups:        []string{"IB + VWAP"},
		},
	}
	if got := cfg.TradingRulesPrompt(); got != "" {
		t.Fatalf("disabled rules must not reach the prompt, got %q", got)
	}
}

func TestTradingRulesPromptEnabledRendersSections(t *testing.T) {
	cfg := &StrategyConfig{
		TradingRules: TradingRulesConfig{
			Enabled:        true,
			MarketMandate:  []string{"US only"},
			Setups:         []string{"IB + VWAP"},
			RiskManagement: []string{"-2% kill switch"},
		},
	}
	got := cfg.TradingRulesPrompt()
	for _, want := range []string{"TRADING RULES", "MARKET MANDATE", "US only", "SETUPS", "IB + VWAP", "RISK MANAGEMENT", "-2% kill switch"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestBeatSymbolsPriority(t *testing.T) {
	t.Setenv("BEAT_SYMBOL", "")

	cfg := &StrategyConfig{Beat: BeatConfig{Symbols: []string{" qqq ", "MNQ", "QQQ"}}}
	got := cfg.BeatSymbols()
	if len(got) != 2 || got[0] != "QQQ" || got[1] != "MNQ" {
		t.Fatalf("beat.symbols priority failed: %v", got)
	}

	withEnv := &StrategyConfig{}
	t.Setenv("BEAT_SYMBOL", "SPY, qqq")
	gotEnv := withEnv.BeatSymbols()
	if len(gotEnv) != 2 || gotEnv[0] != "SPY" || gotEnv[1] != "QQQ" {
		t.Fatalf("BEAT_SYMBOL fallback failed: %v", gotEnv)
	}

	t.Setenv("BEAT_SYMBOL", "")
	withWatchlist := &StrategyConfig{Watchlist: []string{"IWM"}}
	gotWatchlist := withWatchlist.BeatSymbols()
	if len(gotWatchlist) != 1 || gotWatchlist[0] != "IWM" {
		t.Fatalf("watchlist fallback failed: %v", gotWatchlist)
	}

	gotDefault := (&StrategyConfig{}).BeatSymbols()
	if len(gotDefault) != 1 || gotDefault[0] != "QQQ" {
		t.Fatalf("default fallback failed: %v", gotDefault)
	}
}

func TestBeatSymbolsUsesObserveBeforeEnv(t *testing.T) {
	t.Setenv("BEAT_SYMBOL", "SPY")
	cfg := &StrategyConfig{ObserveSymbols: []string{"IWM", "QQQ"}}
	got := cfg.BeatSymbols()
	if len(got) != 2 || got[0] != "IWM" || got[1] != "QQQ" {
		t.Fatalf("observe should win over BEAT_SYMBOL: %v", got)
	}
}

func TestIsSymbolAllowedInheritsObserve(t *testing.T) {
	cfg := &StrategyConfig{
		ObserveSymbols: []string{"QQQ", "SPY"},
		Strategies: map[string]interface{}{
			"empty_allow": map[string]interface{}{
				"operate_allow": []interface{}{},
				"operate_block": []interface{}{},
			},
			"blocked": map[string]interface{}{
				"operate_allow": []interface{}{"QQQ"},
				"operate_block": []interface{}{"SPY"},
			},
		},
	}
	if allowed, reason := isSymbolAllowed(cfg, "empty_allow", "QQQ"); !allowed {
		t.Fatalf("QQQ should inherit Observe: %s", reason)
	}
	if allowed, _ := isSymbolAllowed(cfg, "empty_allow", "TSLA"); allowed {
		t.Fatal("TSLA is not in Observe and must be rejected")
	}
	if allowed, _ := isSymbolAllowed(cfg, "blocked", "SPY"); allowed {
		t.Fatal("Operate block must win over Observe")
	}
	if allowed, _ := isSymbolAllowed(cfg, "blocked", "QQQ"); !allowed {
		t.Fatal("QQQ should be allowed for blocked strategy")
	}
}

func TestStrategiesFileParsesWithUnifiedRules(t *testing.T) {
	raw, err := os.ReadFile("../data/strategies.json")
	if err != nil {
		t.Fatalf("cannot read data/strategies.json: %v", err)
	}

	var cfg StrategyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("data/strategies.json is not valid JSON: %v", err)
	}
	if len(cfg.ActiveStrategies) == 0 {
		t.Fatal("active_strategies is empty")
	}
	if len(cfg.Beat.Symbols) == 0 && len(cfg.ObserveSymbols) == 0 {
		t.Fatal("beat.symbols and observe_symbols are both empty")
	}
	beatSymbols := cfg.Beat.Symbols
	if len(beatSymbols) == 0 {
		beatSymbols = cfg.ObserveSymbols
	}
	for _, sym := range beatSymbols {
		if strings.EqualFold(sym, "ESTX50") {
			t.Fatalf("ESTX50 must not be part of the beat watchlist: %v", beatSymbols)
		}
	}
	if cfg.TradingRules.Enabled {
		t.Fatal("trading_rules must ship disabled (reference only)")
	}
	if len(cfg.TradingRules.MarketMandate) == 0 || len(cfg.TradingRules.Setups) == 0 {
		t.Fatal("documented trading rules are missing from data/strategies.json")
	}
	if cfg.TradingRulesPrompt() != "" {
		t.Fatal("disabled trading rules must not be injected into the prompt")
	}
}
