package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// weeklyBaselinePath is a package var so tests can point it at a temp file.
// In production it lives next to the SQLite DB under data/.
var weeklyBaselinePath = filepath.Join("data", "weekly_baseline.json")

var weeklyBaselineMu sync.Mutex

// WeeklyBaseline stores the NAV at the start of an ISO week so the weekly
// circuit breaker no longer depends on a hardcoded balance.
type WeeklyBaseline struct {
	Week        string    `json:"week"`
	BaselineNAV float64   `json:"baseline_nav"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// currentWeekKey renders the ISO week, e.g. "2026-W47".
func currentWeekKey(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

func loadWeeklyBaseline() (WeeklyBaseline, error) {
	var b WeeklyBaseline
	raw, err := os.ReadFile(weeklyBaselinePath)
	if err != nil {
		return b, err
	}
	err = json.Unmarshal(raw, &b)
	return b, err
}

func saveWeeklyBaseline(b WeeklyBaseline) error {
	if err := os.MkdirAll(filepath.Dir(weeklyBaselinePath), 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := weeklyBaselinePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, weeklyBaselinePath)
}

// weeklyBaselineNAV returns the NAV at the start of the current ISO week,
// creating or rolling the baseline when the week changes or the file is
// missing. The first check of a week never blocks: it only records the anchor.
func weeklyBaselineNAV(currentNAV float64, now time.Time) float64 {
	weeklyBaselineMu.Lock()
	defer weeklyBaselineMu.Unlock()

	week := currentWeekKey(now)
	if b, err := loadWeeklyBaseline(); err == nil && b.Week == week && b.BaselineNAV > 0 {
		return b.BaselineNAV
	}
	_ = saveWeeklyBaseline(WeeklyBaseline{Week: week, BaselineNAV: currentNAV, UpdatedAt: now.UTC()})
	return currentNAV
}

// weeklyLossBreached reports whether currentNAV has fallen more than maxLossPct
// below the weekly baseline.
func weeklyLossBreached(currentNAV, baselineNAV, maxLossPct float64) bool {
	if baselineNAV <= 0 || maxLossPct <= 0 {
		return false
	}
	return currentNAV < baselineNAV*(1-maxLossPct/100)
}
