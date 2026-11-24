package services

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"prophet-trader/interfaces"

	"github.com/sirupsen/logrus"
)

// StartDailyReport sends one Telegram summary per day after DAILY_REPORT_TIME.
func StartDailyReport(ctx context.Context, trading interfaces.TradingService, logger *logrus.Logger) {
	if strings.EqualFold(os.Getenv("DAILY_REPORT_ENABLED"), "false") {
		return
	}
	at := os.Getenv("DAILY_REPORT_TIME")
	if at == "" {
		at = "16:05"
	}

	lastSent := ""
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.Format("15:04") < at {
				continue
			}
			day := now.Format("2006-01-02")
			if lastSent == day {
				continue
			}
			msg := buildDailyReport(ctx, trading, now)
			if msg == "" {
				continue
			}
			lastSent = day
			SendTelegramAlert(msg)
			if logger != nil {
				logger.WithField("date", day).Info("Daily report sent")
			}
		}
	}
}

func buildDailyReport(ctx context.Context, trading interfaces.TradingService, now time.Time) string {
	if trading == nil {
		return ""
	}
	actx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	acc, err := trading.GetAccount(actx)
	if err != nil || acc == nil {
		return ""
	}
	positions, _ := trading.GetPositions(actx)

	unrealized := 0.0
	for _, p := range positions {
		if p != nil {
			unrealized += p.UnrealizedPL
		}
	}
	realized := RealizedPnLToday()

	return fmt.Sprintf("📊 OpenProphet — cierre del día (%s)\nNAV: $%.2f | Cash: $%.2f\nP&L realizado hoy: $%+.2f\nP&L no realizado: $%+.2f\nPosiciones abiertas: %d\nOperaciones nuevas hoy: %d",
		now.Format("2006-01-02"), acc.PortfolioValue, acc.Cash, realized, unrealized, len(positions), tradesToday(now))
}
