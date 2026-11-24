package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTradingLimits(t *testing.T) {
	t.Helper()
	oldCfg := strategiesCache
	oldPath := dailyTradesPath
	strategiesCache = &StrategyConfig{GlobalFilters: map[string]interface{}{
		"max_trades_per_day": 2.0,
		"time_stop_minutes":  30.0,
	}}
	dailyTradesPath = filepath.Join(t.TempDir(), "daily_trades.json")
	t.Cleanup(func() {
		strategiesCache = oldCfg
		dailyTradesPath = oldPath
	})
}

func TestReserveDailyTradeCapAndRelease(t *testing.T) {
	setupTradingLimits(t)
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

	if err := ReserveDailyTrade(now); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if err := ReserveDailyTrade(now); err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if err := ReserveDailyTrade(now); err == nil {
		t.Fatal("third reserve must fail the daily cap")
	}
	if got := tradesToday(now); got != 2 {
		t.Fatalf("tradesToday = %d, want 2", got)
	}

	ReleaseDailyTrade(now)
	if got := tradesToday(now); got != 1 {
		t.Fatalf("after release tradesToday = %d, want 1", got)
	}

	nextDay := now.AddDate(0, 0, 1)
	if err := ReserveDailyTrade(nextDay); err != nil {
		t.Fatalf("next day reserve: %v", err)
	}
	if got := tradesToday(nextDay); got != 1 {
		t.Fatalf("next day count = %d, want 1", got)
	}
}

func TestShouldTimeStop(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		created time.Time
		minutes int
		want    bool
	}{
		{now.Add(-31 * time.Minute), 30, true},
		{now.Add(-29 * time.Minute), 30, false},
		{now.Add(-2 * time.Hour), 0, false},
		{time.Time{}, 30, false},
	}
	for _, c := range cases {
		if got := shouldTimeStop(c.created, now, c.minutes); got != c.want {
			t.Fatalf("shouldTimeStop(%v, %d) = %v, want %v", c.created, c.minutes, got, c.want)
		}
	}
}

func TestRotateLogIfNeeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bot.log")
	if err := os.WriteFile(path, make([]byte, 4096), 0644); err != nil {
		t.Fatal(err)
	}
	RotateLogIfNeeded(path, 1024)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("log should have been renamed")
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("rotated file is missing")
	}
}
