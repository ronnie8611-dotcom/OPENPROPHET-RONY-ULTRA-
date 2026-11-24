package services

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"prophet-trader/interfaces"
)

func TestGrossExposure(t *testing.T) {
	positions := []*interfaces.Position{
		{Symbol: "AMZN", Qty: 85, CostBasis: 21675, MarketValue: 21931},
		{Symbol: "QQQ", Qty: -10, CostBasis: 6600},
		nil,
		{Symbol: "CASH", Qty: 0, CostBasis: 999},
	}
	if got, want := grossExposure(positions), 28275.0; math.Abs(got-want) > 0.01 {
		t.Fatalf("grossExposure = %.2f, want %.2f", got, want)
	}
}

func TestOpenPositionCountAndHoldsSymbol(t *testing.T) {
	positions := []*interfaces.Position{
		{Symbol: "AMZN", Qty: 85},
		{Symbol: "QQQ", Qty: -10},
		{Symbol: "FLAT", Qty: 0},
		nil,
	}
	if got := openPositionCount(positions); got != 2 {
		t.Fatalf("openPositionCount = %d, want 2", got)
	}
	if !holdsSymbol(positions, "QQQ") {
		t.Fatal("expected holdsSymbol(QQQ) = true")
	}
	if holdsSymbol(positions, "SPY") {
		t.Fatal("expected holdsSymbol(SPY) = false")
	}
}

func TestOrderNotional(t *testing.T) {
	if got := orderNotional(10, 100, "QQQ"); got != 1000 {
		t.Fatalf("stock notional = %.2f, want 1000", got)
	}
	if got := orderNotional(2, 20000, "MNQ"); got != 80000 {
		t.Fatalf("MNQ notional = %.2f, want 80000", got)
	}
	if got := orderNotional(-2, 20000, "MNQ"); got != 80000 {
		t.Fatalf("short notional must be absolute, got %.2f", got)
	}
	if got := orderNotional(0, 100, "QQQ"); got != 0 {
		t.Fatalf("zero qty notional = %.2f, want 0", got)
	}
}

func TestWeeklyLossBreached(t *testing.T) {
	cases := []struct {
		current, baseline, pct float64
		want                   bool
	}{
		{10000, 10000, 8, false},
		{9201, 10000, 8, false},
		{9199, 10000, 8, true},
		{9000, 0, 8, false},
		{9000, 10000, 0, false},
	}
	for _, c := range cases {
		if got := weeklyLossBreached(c.current, c.baseline, c.pct); got != c.want {
			t.Fatalf("weeklyLossBreached(%.0f, %.0f, %.1f) = %v, want %v", c.current, c.baseline, c.pct, got, c.want)
		}
	}
}

func TestWeeklyBaselineLifecycle(t *testing.T) {
	old := weeklyBaselinePath
	weeklyBaselinePath = filepath.Join(t.TempDir(), "weekly_baseline.json")
	defer func() { weeklyBaselinePath = old }()

	weekOne := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

	if got := weeklyBaselineNAV(10000, weekOne); got != 10000 {
		t.Fatalf("first baseline = %.2f, want 10000", got)
	}
	// Same week: the anchor must not move even if NAV drops.
	if got := weeklyBaselineNAV(9000, weekOne.Add(48*time.Hour)); got != 10000 {
		t.Fatalf("same-week baseline = %.2f, want 10000", got)
	}
	// New ISO week: the anchor rolls to the new NAV.
	nextWeek := weekOne.AddDate(0, 0, 7)
	if got := weeklyBaselineNAV(9000, nextWeek); got != 9000 {
		t.Fatalf("next-week baseline = %.2f, want 9000", got)
	}
}
