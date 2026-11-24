package services

import (
	"math"
	"testing"
)

func TestMaxDrawdownPct(t *testing.T) {
	curve := []EquityPoint{{Value: 100}, {Value: 120}, {Value: 90}, {Value: 110}}
	if got, want := maxDrawdownPct(curve), 25.0; math.Abs(got-want) > 0.01 {
		t.Fatalf("maxDrawdownPct = %.2f, want %.2f", got, want)
	}
	if got := maxDrawdownPct(nil); got != 0 {
		t.Fatalf("empty curve drawdown = %.2f, want 0", got)
	}
}

func TestParametricVaR(t *testing.T) {
	got := parametricVaR(100000, 2, 1.645)
	if math.Abs(got-3290) > 1 {
		t.Fatalf("VaR = %.2f, want ~3290", got)
	}
	if got := parametricVaR(0, 2, 1.645); got != 0 {
		t.Fatalf("VaR with zero NAV = %.2f, want 0", got)
	}
}

func TestSectorOf(t *testing.T) {
	if sectorOf("QQQ") != "Tech" {
		t.Fatalf("QQQ sector = %q", sectorOf("QQQ"))
	}
	if sectorOf("AMZN") != "Consumer" {
		t.Fatalf("AMZN sector = %q", sectorOf("AMZN"))
	}
	if sectorOf("UNKNOWN") != "Other" {
		t.Fatalf("unknown sector = %q", sectorOf("UNKNOWN"))
	}
}
