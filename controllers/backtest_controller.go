package controllers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"prophet-trader/backtest"
	"prophet-trader/interfaces"

	"github.com/gin-gonic/gin"
)

type BacktestController struct {
	data interfaces.DataService
}

func NewBacktestController(data interfaces.DataService) *BacktestController {
	return &BacktestController{data: data}
}

// HandleRunBacktest runs the native Go backtester on IBKR historical bars.
func (bc *BacktestController) HandleRunBacktest(c *gin.Context) {
	var req struct {
		Symbol         string  `json:"symbol"`
		Timeframe      string  `json:"timeframe"`
		Strategy       string  `json:"strategy"`
		Days           int     `json:"days"`
		InitialCapital float64 `json:"initial_capital"`
		RiskPct        float64 `json:"risk_pct"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request", "details": err.Error()})
		return
	}
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	if req.Symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol is required"})
		return
	}
	if req.Timeframe == "" {
		req.Timeframe = "1Day"
	}
	if req.Strategy == "" {
		req.Strategy = "raschke"
	}
	if req.Days <= 0 {
		req.Days = 365
	}
	if req.Days > 730 {
		req.Days = 730
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	bars, err := bc.data.GetHistoricalBars(ctx, req.Symbol, time.Now().AddDate(0, 0, -req.Days), time.Now(), req.Timeframe)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load historical bars", "details": err.Error()})
		return
	}

	res := backtest.Run(req.Symbol, bars, backtest.Params{
		Strategy:       req.Strategy,
		Timeframe:      req.Timeframe,
		InitialCapital: req.InitialCapital,
		RiskPct:        req.RiskPct,
		MaxPositionPct: 20,
		Commission:     0.005,
		SlippagePct:    0.01,
	})
	c.JSON(http.StatusOK, res)
}
