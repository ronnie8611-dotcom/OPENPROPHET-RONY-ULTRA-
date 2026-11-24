package services

import (
	"testing"
	"time"
)

func TestOptionMultiplierAndExpiry(t *testing.T) {
	if got := instrumentMultiplier("QQQ:20260116:C:600"); got != 100 {
		t.Fatalf("option multiplier = %.0f, want 100", got)
	}
	if got := instrumentMultiplier("QQQ"); got != 1 {
		t.Fatalf("stock multiplier = %.0f, want 1", got)
	}
	if got := instrumentMultiplier("MNQZ6"); got != 2 {
		t.Fatalf("MNQ multiplier = %.0f, want 2", got)
	}

	exp, ok := OptionExpiryDate("QQQ:20260116:C:600")
	if !ok || exp.Format("2006-01-02") != "2026-01-16" {
		t.Fatalf("colon expiry = %v ok=%v", exp, ok)
	}
	exp, ok = OptionExpiryDate("TSLA251219C00400000")
	if !ok || exp.Format("2006-01-02") != "2025-12-19" {
		t.Fatalf("OCC expiry = %v ok=%v", exp, ok)
	}
	if _, ok := OptionExpiryDate("QQQ"); ok {
		t.Fatal("stock symbol must not parse as an option expiry")
	}
}

func TestOptionExpiryUsesLocalDayBoundary(t *testing.T) {
	today := time.Now().In(time.Local).Format("20060102")
	exp, ok := OptionExpiryDate("QQQ:" + today + ":C:600")
	if !ok {
		t.Fatal("today's expiry should parse")
	}
	if exp.Year() != time.Now().Year() || exp.YearDay() != time.Now().YearDay() {
		t.Fatalf("expiry %v is not today's local date", exp)
	}
}
