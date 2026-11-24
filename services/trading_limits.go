package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// dailyTradesPath is a package var so tests can point it at a temp file.
var dailyTradesPath = filepath.Join("data", "daily_trades.json")
var dailyTradesMu sync.Mutex

type dailyTradesFile struct {
	Date      string    `json:"date"`
	Count     int       `json:"count"`
	UpdatedAt time.Time `json:"updated_at"`
}

func loadDailyTrades() (dailyTradesFile, error) {
	var f dailyTradesFile
	raw, err := os.ReadFile(dailyTradesPath)
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(raw, &f)
	return f, err
}

func saveDailyTrades(f dailyTradesFile) error {
	if err := os.MkdirAll(filepath.Dir(dailyTradesPath), 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := dailyTradesPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dailyTradesPath)
}

// maxTradesPerDay reads global_filters.max_trades_per_day (0 = unlimited).
func maxTradesPerDay() int {
	cfg := LoadStrategiesConfig()
	if cfg == nil || cfg.GlobalFilters == nil {
		return 0
	}
	switch v := cfg.GlobalFilters["max_trades_per_day"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return 0
}

func tradesToday(now time.Time) int {
	dailyTradesMu.Lock()
	defer dailyTradesMu.Unlock()
	f, err := loadDailyTrades()
	if err != nil || f.Date != now.Format("2006-01-02") {
		return 0
	}
	return f.Count
}

// ReserveDailyTrade checks the daily cap and increments the counter atomically.
func ReserveDailyTrade(now time.Time) error {
	dailyTradesMu.Lock()
	defer dailyTradesMu.Unlock()

	f, _ := loadDailyTrades()
	today := now.Format("2006-01-02")
	if f.Date != today {
		f = dailyTradesFile{Date: today}
	}
	if max := maxTradesPerDay(); max > 0 && f.Count >= max {
		return fmt.Errorf("HARD STOP: máximo de %d operaciones nuevas por día alcanzado", max)
	}
	f.Count++
	f.UpdatedAt = now.UTC()
	return saveDailyTrades(f)
}

// ReleaseDailyTrade rolls back a reservation when the entry order fails.
func ReleaseDailyTrade(now time.Time) {
	dailyTradesMu.Lock()
	defer dailyTradesMu.Unlock()

	f, err := loadDailyTrades()
	if err != nil || f.Date != now.Format("2006-01-02") || f.Count <= 0 {
		return
	}
	f.Count--
	f.UpdatedAt = now.UTC()
	_ = saveDailyTrades(f)
}

// timeStopMinutes reads global_filters.time_stop_minutes (0 = disabled).
func timeStopMinutes() int {
	cfg := LoadStrategiesConfig()
	if cfg == nil || cfg.GlobalFilters == nil {
		return 0
	}
	switch v := cfg.GlobalFilters["time_stop_minutes"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return 0
}

func shouldTimeStop(created, now time.Time, minutes int) bool {
	if minutes <= 0 || created.IsZero() {
		return false
	}
	return now.Sub(created) >= time.Duration(minutes)*time.Minute
}
