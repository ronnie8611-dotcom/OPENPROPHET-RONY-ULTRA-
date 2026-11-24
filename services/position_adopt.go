package services

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"prophet-trader/interfaces"
)

// AdoptPositionRequest imports an existing broker position into the managed
// position system without placing a new entry order.
type AdoptPositionRequest struct {
	Symbol            string  `json:"symbol" binding:"required"`
	StopLossPercent   float64 `json:"stop_loss_percent"`
	TakeProfitPercent float64 `json:"take_profit_percent"`
}

// AdoptPosition attaches SL/TP monitoring to a position that already exists at
// the broker. It places a protective stop order and leaves the entry untouched.
func (pm *PositionManager) AdoptPosition(ctx context.Context, req *AdoptPositionRequest) (*ManagedPosition, error) {
	symbol := strings.TrimSpace(strings.ToUpper(req.Symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}

	brokerPositions, err := pm.tradingService.GetPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not read broker positions: %w", err)
	}
	var broker *interfaces.Position
	for _, p := range brokerPositions {
		if p != nil && strings.EqualFold(p.Symbol, symbol) && p.Qty != 0 {
			broker = p
			break
		}
	}
	if broker == nil {
		return nil, fmt.Errorf("no open broker position for %s", symbol)
	}

	pm.mu.RLock()
	for _, existing := range pm.positions {
		if existing != nil && existing.Status == "ACTIVE" && strings.EqualFold(existing.Symbol, symbol) {
			pm.mu.RUnlock()
			return nil, fmt.Errorf("position %s is already managed (%s)", symbol, existing.ID)
		}
	}
	pm.mu.RUnlock()

	side := "buy"
	if broker.Qty < 0 {
		side = "sell"
	}
	qty := math.Abs(broker.Qty)
	entry := broker.AvgEntryPrice
	if entry <= 0 {
		entry = broker.CurrentPrice
	}
	if entry <= 0 {
		return nil, fmt.Errorf("no entry price available for %s", symbol)
	}
	current := broker.CurrentPrice
	if current <= 0 {
		current = entry
	}

	slPct := req.StopLossPercent
	if slPct <= 0 {
		slPct = 5
	}
	tpPct := req.TakeProfitPercent
	if tpPct <= 0 {
		tpPct = 10
	}
	stopPrice := pm.calculateStopLoss(entry, nil, &slPct, side)
	takeProfit := pm.calculateTakeProfit(entry, nil, &tpPct, side)

	exitSide := "sell"
	if side == "sell" {
		exitSide = "buy"
	}
	stopOrder := &interfaces.Order{
		Symbol:      symbol,
		Qty:         qty,
		Side:        exitSide,
		Type:        "stop",
		StopPrice:   &stopPrice,
		TimeInForce: "gtc",
		Status:      "pending",
		SubmittedAt: time.Now(),
	}
	result, err := pm.tradingService.PlaceOrder(ctx, stopOrder)
	if err != nil {
		return nil, fmt.Errorf("protective stop order failed: %w", err)
	}

	now := time.Now()
	position := &ManagedPosition{
		ID:                pm.generatePositionID(),
		Symbol:            symbol,
		Side:              side,
		Strategy:          "ADOPTED",
		Quantity:          qty,
		EntryPrice:        entry,
		EntryOrderType:    "market",
		StopLossPrice:     stopPrice,
		StopLossPercent:   slPct,
		StopLossOrderID:   result.OrderID,
		TakeProfitPrice:   takeProfit,
		TakeProfitPercent: tpPct,
		Status:            "ACTIVE",
		CurrentPrice:      current,
		RemainingQty:      qty,
		CreatedAt:         now,
		UpdatedAt:         now,
		Notes:             "ADOPTED",
	}

	pm.mu.Lock()
	pm.positions[position.ID] = position
	pm.mu.Unlock()

	if err := pm.savePositionToDB(position); err != nil {
		pm.logger.WithError(err).Error("Failed to save adopted position to database")
	}
	pm.logger.WithFields(map[string]interface{}{
		"position_id": position.ID,
		"symbol":      symbol,
		"qty":         qty,
		"stop":        stopPrice,
		"target":      takeProfit,
	}).Info("Broker position adopted into managed positions")

	return position, nil
}
