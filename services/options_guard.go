package services

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"prophet-trader/configstore"
	"prophet-trader/interfaces"
)

// OptionExpiryDate extracts the expiry from "UND:YYYYMMDD:C:STRIKE" or the
// compact OCC form (e.g. TSLA251219C00400000).
func OptionExpiryDate(symbol string) (time.Time, bool) {
	s := strings.TrimSpace(symbol)
	if s == "" {
		return time.Time{}, false
	}
	for _, part := range strings.Split(s, ":") {
		if len(part) == 8 {
			if t, err := time.ParseInLocation("20060102", part, time.Local); err == nil {
				return t, true
			}
		}
	}
	if len(s) >= 15 {
		datePart := s[len(s)-15 : len(s)-9]
		if t, err := time.ParseInLocation("060102", datePart, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func optionPremium(symbol string, lmtPrice float64, trading interfaces.TradingService, ctx context.Context) float64 {
	if lmtPrice > 0 {
		return lmtPrice
	}
	if trading == nil {
		return 0
	}
	qctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	q, err := trading.GetOptionsQuote(qctx, symbol)
	if err != nil || q == nil {
		return 0
	}
	if q.BidPrice > 0 && q.AskPrice > 0 {
		return (q.BidPrice + q.AskPrice) / 2
	}
	if q.AskPrice > 0 {
		return q.AskPrice
	}
	return q.BidPrice
}

// ValidateOptionsOrder enforces allowOptions, maxOrderValue (premium × 100 ×
// contracts) and the 0DTE gate in local time. It is used by both the agent tool
// and the human approval path.
func ValidateOptionsOrder(ctx context.Context, trading interfaces.TradingService, symbol, action string, qty float64, orderType string, lmtPrice float64) error {
	perms := configstore.Get().Permissions
	if configstore.IsInitialized() && !perms.AllowOptions {
		return fmt.Errorf("HARD STOP: options trading is disabled (allowOptions=false)")
	}
	if qty <= 0 {
		return fmt.Errorf("HARD STOP: options quantity must be greater than zero")
	}

	premium := optionPremium(symbol, lmtPrice, trading, ctx)
	if premium <= 0 {
		if perms.MaxOrderValue > 0 {
			return fmt.Errorf("HARD STOP: could not value %s and MaxOrderValue is active — refusing a blind options order", symbol)
		}
	} else {
		notional := qty * premium * 100
		if perms.MaxOrderValue > 0 && notional > perms.MaxOrderValue {
			return fmt.Errorf("HARD STOP: options notional $%.0f exceeds MaxOrderValue $%.0f", notional, perms.MaxOrderValue)
		}
	}

	if exp, ok := OptionExpiryDate(symbol); ok && !perms.Allow0DTE {
		now := time.Now().In(time.Local)
		if exp.Year() == now.Year() && exp.YearDay() == now.YearDay() {
			return fmt.Errorf("HARD STOP: 0DTE options are disabled (allow0DTE=false) for %s", symbol)
		}
	}
	return nil
}

// ValidateComboOrder applies the same value/allowOptions checks to a net combo.
func ValidateComboOrder(trading interfaces.TradingService, action string, qty float64, orderType string, lmtPrice float64) error {
	perms := configstore.Get().Permissions
	if configstore.IsInitialized() && !perms.AllowOptions {
		return fmt.Errorf("HARD STOP: options trading is disabled (allowOptions=false)")
	}
	if qty <= 0 {
		return fmt.Errorf("HARD STOP: combo quantity must be greater than zero")
	}
	if perms.MaxOrderValue > 0 {
		if lmtPrice == 0 {
			return fmt.Errorf("HARD STOP: combo has no limit price and MaxOrderValue is active — refusing a blind combo")
		}
		notional := math.Abs(lmtPrice) * qty * 100
		if notional > perms.MaxOrderValue {
			return fmt.Errorf("HARD STOP: combo notional $%.0f exceeds MaxOrderValue $%.0f", notional, perms.MaxOrderValue)
		}
	}
	return nil
}
