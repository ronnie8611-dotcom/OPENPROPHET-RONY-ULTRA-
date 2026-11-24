package services

import (
	"path/filepath"
	"testing"
)

func TestSimilarTradesRankByContext(t *testing.T) {
	old := memoryPath
	memoryPath = filepath.Join(t.TempDir(), "agent_memory.json")
	t.Cleanup(func() { memoryPath = old })

	LogTradeResultWithContext("QQQ", "raschke", "WIN", 100, "high volatility breakout oversold", "HIGH_VOLATILITY", 28, 2.1)
	LogTradeResultWithContext("SPY", "mean_reversion", "LOSS", -50, "low volatility range fade", "RANGE", 55, 0.8)

	got := GetSimilarTrades(TradeQuery{
		Symbol: "QQQ", Setup: "raschke", Context: "high volatility oversold", RSI: 30, ATRPercent: 2.0,
	}, 1)
	if len(got) != 1 {
		t.Fatalf("similar trades = %d, want 1", len(got))
	}
	if got[0].Symbol != "QQQ" {
		t.Fatalf("top match = %s, want QQQ", got[0].Symbol)
	}
	if got[0].Score <= 0 {
		t.Fatalf("score = %.2f, want > 0", got[0].Score)
	}
}
