package services

import (
	"context"
	"fmt"
	"math"
	"time"

	"prophet-trader/database"
	"prophet-trader/interfaces"
)

type EquityPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

type RiskMetrics struct {
	NAV            float64            `json:"nav"`
	Cash           float64            `json:"cash"`
	GrossExposure  float64            `json:"gross_exposure"`
	InvestedPct    float64            `json:"invested_pct"`
	Positions      int                `json:"positions"`
	TopSymbol      string             `json:"top_symbol"`
	TopPct         float64            `json:"top_pct"`
	VolatilityPct  float64            `json:"volatility"`
	VaR95          float64            `json:"var_95"`
	VaR99          float64            `json:"var_99"`
	MaxDrawdownPct float64            `json:"max_drawdown_pct"`
	Concentration  float64            `json:"concentration_hhi"`
	Sectors        map[string]float64 `json:"sectors"`
	EquityCurve    []EquityPoint      `json:"equity_curve"`
	Message        string             `json:"message"`
}

var symbolSectors = map[string]string{
	"QQQ": "Tech", "SPY": "Broad", "IWM": "SmallCap",
	"AAPL": "Tech", "MSFT": "Tech", "NVDA": "Tech", "GOOGL": "Tech", "META": "Tech", "AMZN": "Consumer",
	"TSLA": "Consumer", "PLTR": "Tech", "AMD": "Tech", "AVGO": "Tech",
}

func sectorOf(symbol string) string {
	if s, ok := symbolSectors[symbol]; ok {
		return s
	}
	return "Other"
}

// parametricVaR converts a daily volatility percentage into a USD VaR estimate.
func parametricVaR(nav, volPct, z float64) float64 {
	if nav <= 0 || volPct <= 0 || z <= 0 {
		return 0
	}
	return nav * (volPct / 100) * z
}

// maxDrawdownPct returns the worst peak-to-trough decline of an equity curve.
func maxDrawdownPct(curve []EquityPoint) float64 {
	peak := 0.0
	worst := 0.0
	for _, p := range curve {
		if p.Value > peak {
			peak = p.Value
		}
		if peak > 0 {
			dd := (peak - p.Value) / peak * 100
			if dd > worst {
				worst = dd
			}
		}
	}
	return worst
}

// ComputeRiskMetrics builds the Risk tab payload: exposure, VaR, drawdown,
// concentration and equity curve. It degrades gracefully when quote history
// is unavailable (falls back to a conservative 2% daily volatility).
func ComputeRiskMetrics(ctx context.Context, trading interfaces.TradingService, data interfaces.DataService, storage *database.LocalStorage) RiskMetrics {
	m := RiskMetrics{Sectors: map[string]float64{}, EquityCurve: []EquityPoint{}}
	if trading != nil {
		if acc, err := trading.GetAccount(ctx); err == nil && acc != nil {
			m.NAV, m.Cash = acc.PortfolioValue, acc.Cash
		}
		positions, _ := trading.GetPositions(ctx)
		m.Positions = len(positions)
		var topValue float64
		for _, p := range positions {
			if p == nil || p.Qty == 0 {
				continue
			}
			value := math.Abs(p.CostBasis)
			if value == 0 {
				value = math.Abs(p.MarketValue)
			}
			m.GrossExposure += value
			if value > topValue {
				topValue, m.TopSymbol = value, p.Symbol
			}
			if m.NAV > 0 {
				m.Sectors[sectorOf(p.Symbol)] += value / m.NAV * 100
			}
		}
		if m.NAV > 0 {
			m.TopPct = topValue / m.NAV * 100
			m.InvestedPct = m.GrossExposure / m.NAV * 100
		}
	}

	// Daily volatility proxy from QQQ ATR14.
	m.VolatilityPct = 2.0
	if data != nil {
		bars, err := data.GetHistoricalBars(ctx, "QQQ", time.Now().AddDate(0, 0, -40), time.Now(), "1Day")
		if err == nil && len(bars) >= 15 {
			trSum := 0.0
			start := len(bars) - 14
			for i := start; i < len(bars); i++ {
				if i == 0 {
					continue
				}
				high, low, prevClose := bars[i].High, bars[i].Low, bars[i-1].Close
				tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
				trSum += tr
			}
			if last := bars[len(bars)-1].Close; last > 0 && trSum > 0 {
				m.VolatilityPct = (trSum / 14) / last * 100
			}
		}
	}
	if m.VolatilityPct <= 0 {
		m.VolatilityPct = 2.0
	}

	m.VaR95 = parametricVaR(m.NAV, m.VolatilityPct, 1.645)
	m.VaR99 = parametricVaR(m.NAV, m.VolatilityPct, 2.326)

	if storage != nil {
		if snaps, err := storage.GetAccountSnapshots(500); err == nil {
			for _, s := range snaps {
				m.EquityCurve = append(m.EquityCurve, EquityPoint{Time: s.SnapshotTime, Value: s.PortfolioValue})
			}
		}
	}
	if m.NAV > 0 {
		m.EquityCurve = append(m.EquityCurve, EquityPoint{Time: time.Now(), Value: m.NAV})
	}
	m.MaxDrawdownPct = maxDrawdownPct(m.EquityCurve)

	if m.NAV > 0 {
		for _, pct := range m.Sectors {
			m.Concentration += math.Pow(pct/100, 2)
		}
	}

	top := "sin posiciones"
	if m.TopSymbol != "" {
		top = fmt.Sprintf("%s (%.1f%% del NAV)", m.TopSymbol, m.TopPct)
	}
	m.Message = fmt.Sprintf("VaR 95%%: $%.0f (%.1f%% del NAV) · Exposición bruta: $%.0f (%.0f%%) · Mayor posición: %s · Máx. caída: %.1f%%",
		m.VaR95, pctOf(m.VaR95, m.NAV), m.GrossExposure, m.InvestedPct, top, m.MaxDrawdownPct)
	return m
}

func pctOf(v, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return v / total * 100
}
