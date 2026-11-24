package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"prophet-trader/configstore"
	"prophet-trader/database"
	"prophet-trader/interfaces"
	"prophet-trader/models"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// ManagedPosition represents a position with automated risk management
type ManagedPosition struct {
	ID                string                 `json:"id"`
	Symbol            string                 `json:"symbol"`
	Side              string                 `json:"side"` // "buy" or "sell"
	Strategy          string                 `json:"strategy"` // "SWING_TRADE", "LONG_TERM", "DAY_TRADE"

	// Entry details
	Quantity          float64                `json:"quantity"`
	EntryPrice        float64                `json:"entry_price"`
	EntryOrderID      string                 `json:"entry_order_id"`
	EntryOrderType    string                 `json:"entry_order_type"` // "market", "limit"
	AllocationDollars float64                `json:"allocation_dollars"`

	// Risk management
	StopLossPrice     float64                `json:"stop_loss_price"`
	StopLossPercent   float64                `json:"stop_loss_percent"`
	StopLossOrderID   string                 `json:"stop_loss_order_id,omitempty"`
	TrailingStop      bool                   `json:"trailing_stop"`
	TrailingPercent   float64                `json:"trailing_percent,omitempty"`
	
	// [MEJORA A] Trailing Stop Avanzado
	TrailingActivationPoints float64        `json:"trailing_activation_points,omitempty"`
	TrailingDistancePoints   float64        `json:"trailing_distance_points,omitempty"`
	TrailingActive           bool           `json:"trailing_active"`

	// Profit targets
	TakeProfitPrice   float64                `json:"take_profit_price"`
	TakeProfitPercent float64                `json:"take_profit_percent"`
	TakeProfitOrderID string                 `json:"take_profit_order_id,omitempty"`

	// Partial exit strategy
	PartialExit       *PartialExitConfig     `json:"partial_exit,omitempty"`
	PartialExitOrders []string               `json:"partial_exit_orders,omitempty"`

	// Status tracking
	Status            string                 `json:"status"` // "PENDING", "ACTIVE", "PARTIAL", "CLOSED", "STOPPED_OUT", "FAILED"
	CurrentPrice      float64                `json:"current_price"`
	UnrealizedPL      float64                `json:"unrealized_pl"`
	UnrealizedPLPC    float64                `json:"unrealized_pl_percent"`
	RemainingQty      float64                `json:"remaining_qty"`

	// Metadata
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	ClosedAt          *time.Time             `json:"closed_at,omitempty"`
	Notes             string                 `json:"notes,omitempty"`
	Tags              []string               `json:"tags,omitempty"`
}

// PartialExitConfig defines partial profit taking strategy
type PartialExitConfig struct {
	Enabled       bool    `json:"enabled"`
	Percent       float64 `json:"percent"`        // % of position to exit
	TargetPercent float64 `json:"target_percent"` // % gain to trigger partial exit
	TargetPrice   float64 `json:"target_price"`   // Calculated target price
}

// PlaceManagedPositionRequest represents request to open a managed position
type PlaceManagedPositionRequest struct {
	Symbol            string              `json:"symbol" binding:"required"`
	Side              string              `json:"side" binding:"required"` // "buy" or "sell"
	Strategy          string              `json:"strategy"` // "SWING_TRADE", "LONG_TERM", "DAY_TRADE"
	AllocationDollars float64             `json:"allocation_dollars"`
	ExplicitQuantity  *int                `json:"explicit_quantity,omitempty"`

	// Entry configuration
	EntryStrategy     string              `json:"entry_strategy"` // "market", "limit"
	EntryPrice        *float64            `json:"entry_price,omitempty"` // Required for limit orders

	// Risk management (one of these required)
	StopLossPrice     *float64            `json:"stop_loss_price,omitempty"`
	StopLossPercent   *float64            `json:"stop_loss_percent,omitempty"`
	TrailingStop      bool                `json:"trailing_stop"`
	TrailingPercent   float64             `json:"trailing_percent,omitempty"`
	
	// [MEJORA A] Trailing Stop Avanzado
	TrailingActivationPoints float64      `json:"trailing_activation_points,omitempty"`
	TrailingDistancePoints   float64      `json:"trailing_distance_points,omitempty"`

	// Profit targets (one of these required)
	TakeProfitPrice   *float64            `json:"take_profit_price,omitempty"`
	TakeProfitPercent *float64            `json:"take_profit_percent,omitempty"`

	// Partial exit (optional)
	PartialExit       *PartialExitConfig  `json:"partial_exit,omitempty"`

	// Internal: TWAP chunks must not consume extra daily-trade slots.
	SkipDailyCount bool `json:"-"`

	// Metadata
	Notes             string              `json:"notes,omitempty"`
	Tags              []string            `json:"tags,omitempty"`
}

// PositionManager handles automated position management
type PositionManager struct {
	tradingService interfaces.TradingService
	dataService    interfaces.DataService
	storageService *database.LocalStorage

	positions      map[string]*ManagedPosition // position_id -> position
	mu             sync.RWMutex
	checkMutex     sync.Mutex
	logger         *logrus.Logger
	intentManager  *IntentManager
	timeStopAsked  map[string]bool

	ctx            context.Context
	cancel         context.CancelFunc
}

// NewPositionManager creates a new position manager
func NewPositionManager(
	tradingService interfaces.TradingService,
	dataService interfaces.DataService,
	storageService *database.LocalStorage,
) *PositionManager {
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	ctx, cancel := context.WithCancel(context.Background())

	pm := &PositionManager{
		tradingService: tradingService,
		dataService:    dataService,
		storageService: storageService,
		positions:      make(map[string]*ManagedPosition),
		logger:         logger,
		timeStopAsked:  make(map[string]bool),
		ctx:            ctx,
		cancel:         cancel,
	}

	if err := pm.loadPositionsFromDB(); err != nil {
		logger.WithError(err).Error("Failed to load positions from database")
	}

	return pm
}

// PlaceManagedPosition opens a new managed position with automated risk management
func (pm *PositionManager) PlaceManagedPosition(ctx context.Context, req *PlaceManagedPositionRequest) (*ManagedPosition, error) {
	req.Side = strings.ToLower(req.Side)
	req.EntryStrategy = strings.ToLower(req.EntryStrategy)

	if req.Notes == "LIQUIDATION" {
		if req.Side != "buy" && req.Side != "sell" {
			return nil, fmt.Errorf("side must be 'buy' or 'sell'")
		}
		qty := 0.0
		if req.ExplicitQuantity != nil {
			qty = float64(*req.ExplicitQuantity)
		} else {
			return nil, fmt.Errorf("explicit_quantity required for liquidation")
		}
		
		order := &interfaces.Order{
			Symbol:      req.Symbol,
			Qty:         qty,
			Side:        req.Side,
			Type:        "market",
			TimeInForce: "day",
			Status:      "pending",
			SubmittedAt: time.Now(),
		}
		
		res, err := pm.tradingService.PlaceOrder(ctx, order)
		if err != nil {
			return nil, err
		}
		
		return &ManagedPosition{
			ID:           pm.generatePositionID(),
			Symbol:       req.Symbol,
			Side:         req.Side,
			Quantity:     qty,
			Status:       "CLOSED", 
			EntryOrderID: res.OrderID,
			Notes:        "Liquidation order executed",
		}, nil
	}

	pm.logger.WithFields(logrus.Fields{
		"symbol":     req.Symbol,
		"side":       req.Side,
		"allocation": req.AllocationDollars,
	}).Info("Placing managed position")

	if err := pm.validateHardStop(ctx, req); err != nil {
		return nil, err
	}

	if err := pm.validateRequest(req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	currentPrice, err := pm.getCurrentPrice(ctx, req.Symbol)
	if err != nil {
		if req.EntryPrice != nil {
			pm.logger.WithError(err).Warn("Failed to fetch live quote, falling back to explicit entry price")
			currentPrice = *req.EntryPrice
		} else {
			return nil, fmt.Errorf("failed to get current price and no entry price provided: %w", err)
		}
	}

	entryPrice := currentPrice
	if req.EntryPrice != nil {
		entryPrice = *req.EntryPrice
	}

	quantity := pm.resolveQuantity(req, entryPrice)

	// Hard portfolio guards run here as well, because the human-approval path
	// (IntentController) calls PlaceManagedPosition directly, bypassing the
	// tool-level validation.
	guardCtx := &ToolContext{Trading: pm.tradingService}
	if err := checkDailyLossLimit(guardCtx); err != nil {
		return nil, err
	}
	if err := checkWeeklyLossLimit(guardCtx); err != nil {
		return nil, err
	}
	notional := orderNotional(quantity, entryPrice, req.Symbol)
	if err := checkPortfolioCaps(ctx, pm.tradingService, req.Symbol, notional); err != nil {
		return nil, err
	}
	if err := checkPerInstrumentCap(ctx, pm.tradingService, req.Symbol, notional); err != nil {
		return nil, err
	}

	// Daily trade cap: reserve one slot before the entry order is sent.
	reservedTrade := false
	if !req.SkipDailyCount {
		if err := ReserveDailyTrade(time.Now()); err != nil {
			return nil, err
		}
		reservedTrade = true
	}

	stopLossPrice := pm.calculateStopLoss(entryPrice, req.StopLossPrice, req.StopLossPercent, req.Side)
	stopLossPercent := math.Abs((stopLossPrice - entryPrice) / entryPrice * 100)

	takeProfitPrice := pm.calculateTakeProfit(entryPrice, req.TakeProfitPrice, req.TakeProfitPercent, req.Side)
	takeProfitPercent := math.Abs((takeProfitPrice - entryPrice) / entryPrice * 100)

	if req.PartialExit != nil && req.PartialExit.Enabled {
		req.PartialExit.TargetPrice = pm.calculatePartialExitPrice(entryPrice, req.PartialExit.TargetPercent, req.Side)
	}

	position := &ManagedPosition{
		ID:                pm.generatePositionID(),
		Symbol:            req.Symbol,
		Side:              req.Side,
		Strategy:          req.Strategy,
		Quantity:          quantity,
		EntryPrice:        entryPrice,
		EntryOrderType:    req.EntryStrategy,
		AllocationDollars: req.AllocationDollars,
		StopLossPrice:     stopLossPrice,
		StopLossPercent:   stopLossPercent,
		TrailingStop:      req.TrailingStop,
		TrailingPercent:   req.TrailingPercent,
		TrailingActivationPoints: req.TrailingActivationPoints,
		TrailingDistancePoints:   req.TrailingDistancePoints,
		TrailingActive:           false,
		TakeProfitPrice:   takeProfitPrice,
		TakeProfitPercent: takeProfitPercent,
		PartialExit:       req.PartialExit,
		Status:            "PENDING",
		CurrentPrice:      currentPrice,
		RemainingQty:      quantity,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
		Notes:             req.Notes,
		Tags:              req.Tags,
	}

	if err := pm.placeEntryOrder(ctx, position); err != nil {
		if reservedTrade {
			ReleaseDailyTrade(time.Now())
		}
		return nil, fmt.Errorf("failed to place entry order: %w", err)
	}

	pm.mu.Lock()
	pm.positions[position.ID] = position
	pm.mu.Unlock()

	if err := pm.savePositionToDB(position); err != nil {
		pm.logger.WithError(err).Error("Failed to save position to database")
	}

	return position, nil
}

// [MEJORA 1] Validación de Hard Stop a nivel de código (Lee el 15% del JSON)
func (pm *PositionManager) validateHardStop(ctx context.Context, req *PlaceManagedPositionRequest) error {
	acc, err := pm.tradingService.GetAccount(ctx)
	if err != nil || acc == nil {
		return fmt.Errorf("HARD STOP: no se pudo verificar el estado de la cuenta para validar el riesgo")
	}

	if req.AllocationDollars > 0 {
		maxPosPct := 15.0
		if perms := configstore.Get().Permissions; perms.MaxPositionPct > 0 {
			maxPosPct = float64(perms.MaxPositionPct)
		}
		maxAllowed := acc.PortfolioValue * (maxPosPct / 100.0)
		if req.AllocationDollars > maxAllowed {
			return fmt.Errorf("HARD STOP: La asignación de $%.2f excede el límite del %.1f%% del NAV ($%.2f)", req.AllocationDollars, maxPosPct, maxAllowed)
		}
	}
	return nil
}

// placeEntryOrder places the initial entry order
func (pm *PositionManager) placeEntryOrder(ctx context.Context, position *ManagedPosition) error {
	orderType := "market"
	if position.EntryOrderType == "limit" {
		orderType = "limit"
	}

	order := &interfaces.Order{
		Symbol:      position.Symbol,
		Qty:         position.Quantity,
		Side:        position.Side,
		Type:        orderType,
		TimeInForce: "gtc",
		Status:      "pending",
		SubmittedAt: time.Now(),
	}

	if orderType == "limit" {
		order.LimitPrice = &position.EntryPrice
	}

	if position.TakeProfitPrice > 0 {
		order.TakeProfitPrice = &position.TakeProfitPrice
	}
	if position.StopLossPrice > 0 {
		order.StopLossPrice = &position.StopLossPrice
	}

	result, err := pm.tradingService.PlaceOrder(ctx, order)
	if err != nil {
		return err
	}

	position.EntryOrderID = result.OrderID
	position.TakeProfitOrderID = result.TakeProfitOrderID
	position.StopLossOrderID = result.StopLossOrderID
	position.Status = "PENDING"

	return nil
}

// MonitorPositions monitors all active positions and manages risk
func (pm *PositionManager) MonitorPositions(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	pm.logger.Info("Position monitoring started")

	for {
		select {
		case <-ctx.Done():
			pm.logger.Info("Position monitoring stopped")
			return
		case <-ticker.C:
			pm.checkPositions(ctx)
		}
	}
}

// checkPositions checks all positions and manages their risk orders
func (pm *PositionManager) checkPositions(ctx context.Context) {
	// [QA] Evitar solapamiento si IBKR tarda más de 10 segundos
	if !pm.checkMutex.TryLock() {
		pm.logger.Warn("Skipping position check: previous tick still running")
		return
	}
	defer pm.checkMutex.Unlock()

	if err := pm.pollActiveQuotes(ctx); err != nil {
		if strings.Contains(err.Error(), "HTTP 429") {
			pm.logger.Warn("Rate limited by IBKR, pausing checkPositions for backoff")
			time.Sleep(5 * time.Second)
			return
		}
	}

	pm.mu.RLock()
	positions := make([]*ManagedPosition, 0, len(pm.positions))
	for _, pos := range pm.positions {
		positions = append(positions, pos)
	}
	pm.mu.RUnlock()

	for _, position := range positions {
		if position.Status == "CLOSED" || position.Status == "STOPPED_OUT" {
			continue
		}

		if position.Status == "PENDING" {
			pm.checkEntryOrder(ctx, position)
			continue
		}

		if position.Status == "ACTIVE" {
			pm.manageRiskOrders(ctx, position)
			if position.TrailingStop {
				pm.updateTrailingStop(ctx, position)
			}
			pm.enforceTimeStop(ctx, position)
		}
	}
}

// SetIntentManager wires the approval queue used by the time stop.
func (pm *PositionManager) SetIntentManager(im *IntentManager) {
	pm.mu.Lock()
	pm.intentManager = im
	pm.mu.Unlock()
}

// enforceTimeStop requests human approval to close positions older than
// global_filters.time_stop_minutes. It never closes by itself.
func (pm *PositionManager) enforceTimeStop(ctx context.Context, position *ManagedPosition) {
	minutes := timeStopMinutes()
	if !shouldTimeStop(position.CreatedAt, time.Now(), minutes) {
		return
	}

	pm.mu.Lock()
	if position.Status != "ACTIVE" {
		pm.mu.Unlock()
		return
	}
	if pm.timeStopAsked == nil {
		pm.timeStopAsked = make(map[string]bool)
	}
	if pm.timeStopAsked[position.ID] {
		pm.mu.Unlock()
		return
	}
	pm.timeStopAsked[position.ID] = true
	im := pm.intentManager
	pm.mu.Unlock()

	exitSide := "sell"
	if position.Side == "sell" {
		exitSide = "buy"
	}
	qty := position.RemainingQty
	if qty <= 0 {
		qty = position.Quantity
	}

	if im == nil {
		pm.logger.WithFields(logrus.Fields{"position_id": position.ID, "symbol": position.Symbol}).Warn("Time stop reached but IntentManager is not configured; position left open")
		SendTelegramAlert(fmt.Sprintf("⏱️ TIME STOP (%d min): %s necesita cierre, pero no hay cola de aprobación. Revísalo manualmente.", minutes, position.Symbol))
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"symbol":            position.Symbol,
		"side":              exitSide,
		"strategy":          position.Strategy,
		"entry_strategy":    "market",
		"explicit_quantity": int(qty),
		"notes":             "LIQUIDATION",
	})
	id, err := im.CreateIntent(IntentTypeManagedPosition, payload, position.Symbol, exitSide, qty, position.CurrentPrice)
	if err != nil {
		pm.mu.Lock()
		delete(pm.timeStopAsked, position.ID)
		pm.mu.Unlock()
		pm.logger.WithError(err).Error("Failed to create time-stop liquidation intent")
		return
	}

	pm.logger.WithFields(logrus.Fields{
		"position_id": position.ID,
		"symbol":      position.Symbol,
		"minutes":     minutes,
		"intent_id":   id,
	}).Warn("Time stop reached; liquidation intent created for human approval")
	SendTelegramAlert(fmt.Sprintf("⏱️ TIME STOP (%d min): %s %s — aprueba el cierre en el Hub (Intent %s).", minutes, exitSide, position.Symbol, id))
}

// checkEntryOrder checks if entry order has filled
func (pm *PositionManager) checkEntryOrder(ctx context.Context, position *ManagedPosition) {
	order, err := pm.tradingService.GetOrder(ctx, position.EntryOrderID)
	if err != nil {
		brokerPositions, pErr := pm.tradingService.GetPositions(ctx)
		if pErr == nil {
			for _, bp := range brokerPositions {
				if bp.Symbol == position.Symbol && ((position.Side == "buy" && bp.Qty > 0) || (position.Side == "sell" && bp.Qty < 0)) {
					pm.mu.Lock()
					position.Status = "ACTIVE"
					position.EntryPrice = bp.AvgEntryPrice
					position.UpdatedAt = time.Now()
					pm.mu.Unlock()

					pm.logger.WithFields(logrus.Fields{
						"position_id": position.ID,
						"symbol":      position.Symbol,
						"avg_cost":    bp.AvgEntryPrice,
					}).Info("Entry order missing but native position exists; marking ACTIVE")

					if position.PartialExit != nil && position.PartialExit.Enabled {
						if err := pm.placePartialExitOrder(ctx, position); err != nil {
							pm.logger.WithError(err).Error("Failed to place partial exit order")
						}
					}
					pm.savePositionToDB(position)
					return
				}
			}
		}

		pm.mu.Lock()
		position.Status = "CLOSED"
		position.Notes = "Auto-closed: entry order lost after restart"
		position.UpdatedAt = time.Now()
		pm.mu.Unlock()
		
		pm.logger.WithFields(logrus.Fields{
			"position_id": position.ID,
			"order_id":    position.EntryOrderID,
			"symbol":      position.Symbol,
		}).Warn("Entry order not found and no matching broker position; marking CLOSED as stale")
		pm.savePositionToDB(position)
		return
	}

	if order.Status == "filled" || strings.ToLower(order.Status) == "filled" {
		pm.mu.Lock()
		position.Status = "ACTIVE"
		// [QA] Evitar panic si el broker no devuelve FilledAvgPrice
		if order.FilledAvgPrice != nil {
			position.EntryPrice = *order.FilledAvgPrice
		}
		position.UpdatedAt = time.Now()
		pm.mu.Unlock()

		pm.logger.WithFields(logrus.Fields{
			"position_id": position.ID,
			"symbol":      position.Symbol,
			"fill_price":  position.EntryPrice,
		}).Info("Entry order filled - native bracket risk active")

		if position.PartialExit != nil && position.PartialExit.Enabled {
			if err := pm.placePartialExitOrder(ctx, position); err != nil {
				pm.logger.WithError(err).Error("Failed to place partial exit order")
			}
		}
		pm.savePositionToDB(position)
	}
}

// placePartialExitOrder places partial exit order
func (pm *PositionManager) placePartialExitOrder(ctx context.Context, position *ManagedPosition) error {
	exitSide := "sell"
	if position.Side == "sell" {
		exitSide = "buy"
	}

	partialQty := position.Quantity * (position.PartialExit.Percent / 100.0)

	order := &interfaces.Order{
		Symbol:      position.Symbol,
		Qty:         partialQty,
		Side:        exitSide,
		Type:        "limit",
		TimeInForce: "gtc",
		LimitPrice:  &position.PartialExit.TargetPrice,
		Status:      "pending",
		SubmittedAt: time.Now(),
	}

	result, err := pm.tradingService.PlaceOrder(ctx, order)
	if err != nil {
		return err
	}

	pm.mu.Lock()
	position.PartialExitOrders = append(position.PartialExitOrders, result.OrderID)
	pm.mu.Unlock()

	pm.logger.WithFields(logrus.Fields{
		"position_id": position.ID,
		"order_id":    result.OrderID,
		"quantity":    partialQty,
		"limit_price": position.PartialExit.TargetPrice,
	}).Info("Partial exit order placed")

	return nil
}

// manageRiskOrders checks and updates risk management orders
func (pm *PositionManager) manageRiskOrders(ctx context.Context, position *ManagedPosition) {
	if position.CurrentPrice > 0 {
		hitStopLoss := (position.Side == "buy" && position.CurrentPrice <= position.StopLossPrice) ||
			(position.Side == "sell" && position.CurrentPrice >= position.StopLossPrice)

		hitTakeProfit := (position.TakeProfitPrice > 0) &&
			((position.Side == "buy" && position.CurrentPrice >= position.TakeProfitPrice) ||
				(position.Side == "sell" && position.CurrentPrice <= position.TakeProfitPrice))

		if hitTakeProfit {
			pm.logger.WithFields(logrus.Fields{
				"position_id":   position.ID,
				"current_price": position.CurrentPrice,
				"target_price":  position.TakeProfitPrice,
			}).Info("Programmatic Take Profit Triggered - Executing Market Sell")
			
			pm.CloseManagedPosition(ctx, position.ID)
			return
		}

		if hitStopLoss {
			pm.logger.WithFields(logrus.Fields{
				"position_id":   position.ID,
				"current_price": position.CurrentPrice,
				"stop_price":    position.StopLossPrice,
			}).Info("Programmatic Stop Loss Triggered - Executing Market Sell")
			
			pm.CloseManagedPosition(ctx, position.ID)
			return
		}
	}

	brokerPositions, err := pm.tradingService.GetPositions(ctx)
	if err == nil {
		var foundQty float64
		for _, bp := range brokerPositions {
			if bp.Symbol == position.Symbol {
				foundQty = bp.Qty
				break
			}
		}

		if foundQty == 0 {
			pm.mu.Lock()
			position.Status = "CLOSED"
			position.RemainingQty = 0
			now := time.Now()
			position.ClosedAt = &now
			pnl := position.UnrealizedPL
			result := "BREAKEVEN"
			if pnl > 0 { result = "WIN" } else if pnl < 0 { result = "LOSS" }
			pm.mu.Unlock()

			pm.logger.WithField("position_id", position.ID).Info("Position natively closed by bracket")
			LogTradeResult(position.Symbol, position.Strategy, result, pnl, "Closed via native IBKR bracket")
			pm.savePositionToDB(position)
			return
		}
	}

	if position.StopLossOrderID != "" {
		order, err := pm.tradingService.GetOrder(ctx, position.StopLossOrderID)
		if err == nil && order.Status == "filled" {
			pm.mu.Lock()
			position.Status = "STOPPED_OUT"
			now := time.Now()
			position.ClosedAt = &now
			position.RemainingQty = 0
			pnl := position.UnrealizedPL
			pm.mu.Unlock()

			pm.logger.WithField("position_id", position.ID).Info("Position stopped out natively")
			LogTradeResult(position.Symbol, position.Strategy, "LOSS", pnl, "Stopped out via native SL")
			pm.savePositionToDB(position)
			return
		}
	}

	if position.TakeProfitOrderID != "" {
		order, err := pm.tradingService.GetOrder(ctx, position.TakeProfitOrderID)
		if err == nil && order.Status == "filled" {
			pm.mu.Lock()
			position.Status = "CLOSED"
			now := time.Now()
			position.ClosedAt = &now
			position.RemainingQty = 0
			pnl := position.UnrealizedPL
			pm.mu.Unlock()

			pm.logger.WithField("position_id", position.ID).Info("Position closed natively at profit target")
			LogTradeResult(position.Symbol, position.Strategy, "WIN", pnl, "Closed via native TP")
			pm.savePositionToDB(position)
			return
		}
	}

	for _, orderID := range position.PartialExitOrders {
		order, err := pm.tradingService.GetOrder(ctx, orderID)
		if err == nil && order.Status == "filled" {
			pm.mu.Lock()
			position.Status = "PARTIAL"
			position.RemainingQty -= order.FilledQty
			remaining := position.RemainingQty
			pm.mu.Unlock()

			pm.logger.WithFields(logrus.Fields{
				"position_id":   position.ID,
				"filled_qty":    order.FilledQty,
				"remaining_qty": remaining,
			}).Info("Partial exit filled")
			pm.savePositionToDB(position)
		}
	}
}

// [MEJORA A] updateTrailingStop actualiza el Stop Loss dinámicamente (Blindado contra fallos)
func (pm *PositionManager) updateTrailingStop(ctx context.Context, position *ManagedPosition) {
	if position.CurrentPrice <= 0 || position.TrailingDistancePoints <= 0 {
		return
	}

	if !position.TrailingActive {
		if position.TrailingActivationPoints > 0 {
			if position.Side == "buy" && position.CurrentPrice >= position.EntryPrice + position.TrailingActivationPoints {
				pm.mu.Lock()
				position.TrailingActive = true
				pm.mu.Unlock()
				pm.logger.WithField("position_id", position.ID).Info("Trailing Stop ACTIVADO")
			} else if position.Side == "sell" && position.CurrentPrice <= position.EntryPrice - position.TrailingActivationPoints {
				pm.mu.Lock()
				position.TrailingActive = true
				pm.mu.Unlock()
				pm.logger.WithField("position_id", position.ID).Info("Trailing Stop ACTIVADO")
			} else {
				return
			}
		} else {
			pm.mu.Lock()
			position.TrailingActive = true
			pm.mu.Unlock()
		}
	}

	newSL := position.StopLossPrice
	if position.Side == "buy" {
		candidateSL := position.CurrentPrice - position.TrailingDistancePoints
		if candidateSL > newSL {
			newSL = candidateSL
		}
	} else if position.Side == "sell" {
		candidateSL := position.CurrentPrice + position.TrailingDistancePoints
		if candidateSL < newSL || newSL == 0 {
			newSL = candidateSL
		}
	}

	if newSL != position.StopLossPrice {
		exitSide := "sell"
		if position.Side == "sell" { exitSide = "buy" }
		
		newSLPrice := pm.roundPrice(newSL)
		stopOrder := &interfaces.Order{
			Symbol: position.Symbol,
			Qty:    position.RemainingQty,
			Side:   exitSide,
			Type:   "stop",
			TimeInForce: "gtc",
			StopLossPrice: &newSLPrice,
			Status: "pending",
			SubmittedAt: time.Now(),
		}
		
		// [QA] Poner el nuevo SL ANTES de cancelar el viejo para no dejar la posición desnuda
		res, err := pm.tradingService.PlaceOrder(ctx, stopOrder)
		if err == nil && res != nil {
			// Éxito: Ahora cancelamos el viejo SL
			if position.StopLossOrderID != "" {
				pm.tradingService.CancelOrder(ctx, position.StopLossOrderID)
			}
			
			pm.mu.Lock()
			position.StopLossPrice = newSLPrice
			position.StopLossOrderID = res.OrderID
			pm.mu.Unlock()
			
			pm.logger.WithFields(logrus.Fields{
				"position_id": position.ID,
				"new_sl":      newSLPrice,
				"current_px":  position.CurrentPrice,
			}).Info("Trailing Stop actualizado")
			
			pm.savePositionToDB(position)
		} else {
			// [QA] Si falla, NO cancelamos el viejo SL, reintentamos en el próximo tick
			pm.logger.WithError(err).Error("Failed to place new trailing stop order, keeping old one")
		}
	}
}

// updatePositionPrice updates current price and unrealized P&L
func (pm *PositionManager) updatePositionPrice(ctx context.Context, position *ManagedPosition) error {
	currentPrice, err := pm.getCurrentPrice(ctx, position.Symbol)
	if err != nil {
		return err
	}

	pm.mu.Lock()
	position.CurrentPrice = currentPrice
	if position.Side == "buy" {
		position.UnrealizedPL = (currentPrice - position.EntryPrice) * position.RemainingQty
		position.UnrealizedPLPC = ((currentPrice - position.EntryPrice) / position.EntryPrice) * 100
	} else {
		position.UnrealizedPL = (position.EntryPrice - currentPrice) * position.RemainingQty
		position.UnrealizedPLPC = ((position.EntryPrice - currentPrice) / position.EntryPrice) * 100
	}
	position.UpdatedAt = time.Now()
	pm.mu.Unlock()

	return nil
}

// pollActiveQuotes iterates over active positions and updates their prices
func (pm *PositionManager) pollActiveQuotes(ctx context.Context) error {
	pm.mu.RLock()
	var activePositions []*ManagedPosition
	for _, pos := range pm.positions {
		if pos.Status == "ACTIVE" || pos.Status == "PARTIAL" {
			activePositions = append(activePositions, pos)
		}
	}
	pm.mu.RUnlock()

	for _, pos := range activePositions {
		if err := pm.updatePositionPrice(ctx, pos); err != nil {
			pm.logger.WithError(err).WithField("symbol", pos.Symbol).Warn("Failed to poll quote")
			if strings.Contains(err.Error(), "HTTP 429") {
				return err
			}
		}
	}
	return nil
}

// GetManagedPosition retrieves a managed position by ID
func (pm *PositionManager) GetManagedPosition(positionID string) (*ManagedPosition, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	position, exists := pm.positions[positionID]
	if !exists {
		return nil, fmt.Errorf("position not found: %s", positionID)
	}
	return position, nil
}

// ListManagedPositions returns all managed positions
func (pm *PositionManager) ListManagedPositions(status string) []*ManagedPosition {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	positions := make([]*ManagedPosition, 0)
	now := time.Now()

	for _, pos := range pm.positions {
		if pos.Status == "PENDING" {
			age := now.Sub(pos.CreatedAt)
			if age > 24*time.Hour {
				continue
			}
		}

		if status == "" || pos.Status == status {
			positions = append(positions, pos)
		}
	}
	return positions
}

// CloseManagedPosition manually closes a managed position
func (pm *PositionManager) CloseManagedPosition(ctx context.Context, positionID string) error {
	pm.mu.RLock()
	position, exists := pm.positions[positionID]
	pm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("position not found: %s", positionID)
	}

	if position.EntryOrderID != "" {
		if err := pm.tradingService.CancelOrder(ctx, position.EntryOrderID); err != nil {
			pm.logger.WithError(err).Warn("Failed to cancel entry order (may already be filled/cancelled)")
		}
	}

	if position.StopLossOrderID != "" {
		if err := pm.tradingService.CancelOrder(ctx, position.StopLossOrderID); err != nil {
			pm.logger.WithError(err).Warn("Failed to cancel stop loss order")
		}
	}
	if position.TakeProfitOrderID != "" {
		if err := pm.tradingService.CancelOrder(ctx, position.TakeProfitOrderID); err != nil {
			pm.logger.WithError(err).Warn("Failed to cancel take profit order")
		}
	}
	for _, orderID := range position.PartialExitOrders {
		if err := pm.tradingService.CancelOrder(ctx, orderID); err != nil {
			pm.logger.WithError(err).Warn("Failed to cancel partial exit order")
		}
	}

	if position.Status == "ACTIVE" || position.Status == "PARTIAL" {
		if position.RemainingQty > 0 {
			exitSide := "sell"
			if position.Side == "sell" {
				exitSide = "buy"
			}

			order := &interfaces.Order{
				Symbol:      position.Symbol,
				Qty:         position.RemainingQty,
				Side:        exitSide,
				Type:        "market",
				TimeInForce: "day",
				Status:      "pending",
				SubmittedAt: time.Now(),
			}

			_, err := pm.tradingService.PlaceOrder(ctx, order)
			if err != nil {
				pm.logger.WithError(err).Error("Failed to place exit order (market may be closed)")
			}
		}
	}

	pm.mu.Lock()
	position.Status = "CLOSED"
	now := time.Now()
	position.ClosedAt = &now
	pnl := position.UnrealizedPL
	result := "BREAKEVEN"
	if pnl > 0 { result = "WIN" } else if pnl < 0 { result = "LOSS" }
	pm.mu.Unlock()

	LogTradeResult(position.Symbol, position.Strategy, result, pnl, "Closed manually or via programmatic trigger")
	pm.savePositionToDB(position)
	return nil
}

// Helper functions

func (pm *PositionManager) validateRequest(req *PlaceManagedPositionRequest) error {
	if req.Side != "buy" && req.Side != "sell" {
		return fmt.Errorf("side must be 'buy' or 'sell'")
	}

	if req.ExplicitQuantity != nil {
		if *req.ExplicitQuantity <= 0 || *req.ExplicitQuantity > 10 {
			return fmt.Errorf("explicit_quantity must be between 1 and 10; got %d", *req.ExplicitQuantity)
		}
	} else if req.AllocationDollars <= 0 {
		return fmt.Errorf("allocation_dollars must be > 0 (or set explicit_quantity)")
	}

	if req.EntryStrategy == "limit" && req.EntryPrice == nil {
		return fmt.Errorf("entry_price required for limit orders")
	}

	if req.StopLossPrice == nil && req.StopLossPercent == nil {
		return fmt.Errorf("either stop_loss_price or stop_loss_percent required")
	}

	if req.TakeProfitPrice == nil && req.TakeProfitPercent == nil {
		return fmt.Errorf("either take_profit_price or take_profit_percent required")
	}

	return nil
}

// [QA] Lógica de Fallback a QQQ para que el SL/TP funcione si MNQ no da datos
func (pm *PositionManager) getCurrentPrice(ctx context.Context, symbol string) (float64, error) {
	targetSym := symbol
	if strings.HasPrefix(strings.ToUpper(symbol), "MNQ") || symbol == "815824267" {
		targetSym = "QQQ"
	}

	quote, err := pm.dataService.GetLatestQuote(ctx, targetSym)
	if err == nil && quote != nil {
		if quote.AskPrice > 0 { return quote.AskPrice, nil }
		if quote.BidPrice > 0 { return quote.BidPrice, nil }
	}

	if bar, berr := pm.dataService.GetLatestBar(ctx, targetSym); berr == nil && bar != nil && bar.Close > 0 {
		return bar.Close, nil
	}

	if err != nil {
		return 0, fmt.Errorf("no usable price for %s (live quote failed and no bar): %w", symbol, err)
	}
	return 0, fmt.Errorf("no usable price for %s", symbol)
}

func (pm *PositionManager) calculateQuantity(allocation, price float64) float64 {
	return math.Floor(allocation / price)
}

func (pm *PositionManager) resolveQuantity(req *PlaceManagedPositionRequest, price float64) float64 {
	if req.ExplicitQuantity != nil {
		return float64(*req.ExplicitQuantity)
	}
	return pm.calculateQuantity(req.AllocationDollars, price)
}

func (pm *PositionManager) calculateStopLoss(entryPrice float64, stopPrice *float64, stopPercent *float64, side string) float64 {
	if stopPrice != nil {
		return pm.roundPrice(*stopPrice)
	}

	if side == "buy" {
		return pm.roundPrice(entryPrice * (1 - *stopPercent/100.0))
	}

	return pm.roundPrice(entryPrice * (1 + *stopPercent/100.0))
}

func (pm *PositionManager) roundPrice(val float64) float64 {
	return math.Round(val*10) / 10
}

func (pm *PositionManager) calculateTakeProfit(entryPrice float64, profitPrice *float64, profitPercent *float64, side string) float64 {
	if profitPrice != nil {
		return pm.roundPrice(*profitPrice)
	}

	if side == "buy" {
		return pm.roundPrice(entryPrice * (1 + *profitPercent/100.0))
	}

	return pm.roundPrice(entryPrice * (1 - *profitPercent/100.0))
}

func (pm *PositionManager) calculatePartialExitPrice(entryPrice, targetPercent float64, side string) float64 {
	if side == "buy" {
		return entryPrice * (1 + targetPercent/100.0)
	}

	return entryPrice * (1 - targetPercent/100.0)
}

func (pm *PositionManager) generatePositionID() string {
	return fmt.Sprintf("pos_%d", time.Now().UnixNano())
}

func (pm *PositionManager) Stop() {
	pm.cancel()
}

// loadPositionsFromDB loads all active positions from database on startup
func (pm *PositionManager) loadPositionsFromDB() error {
	dbPositions, err := pm.storageService.GetAllManagedPositions("")
	if err != nil {
		return err
	}

	loaded := 0
	for _, dbPos := range dbPositions {
		if dbPos.Status == "CLOSED" || dbPos.Status == "STOPPED_OUT" {
			continue
		}

		position := pm.dbToManagedPosition(dbPos)
		pm.positions[position.ID] = position
		loaded++
	}

	pm.logger.WithField("count", loaded).Info("Loaded managed positions from database")
	return nil
}

// savePositionToDB saves a managed position to database
func (pm *PositionManager) savePositionToDB(position *ManagedPosition) error {
	dbPosition := pm.managedPositionToDB(position)
	return pm.storageService.SaveManagedPosition(dbPosition)
}

// managedPositionToDB converts ManagedPosition to DBManagedPosition
func (pm *PositionManager) managedPositionToDB(pos *ManagedPosition) *models.DBManagedPosition {
	partialExitOrdersJSON, _ := json.Marshal(pos.PartialExitOrders)
	
	// [QA] Hack para persistir el Trailing Stop sin modificar models.go
	dbTags := pos.Tags
	if pos.TrailingActivationPoints > 0 {
		dbTags = append(dbTags, fmt.Sprintf("trail_act=%f", pos.TrailingActivationPoints))
	}
	if pos.TrailingDistancePoints > 0 {
		dbTags = append(dbTags, fmt.Sprintf("trail_dist=%f", pos.TrailingDistancePoints))
	}
	if pos.TrailingActive {
		dbTags = append(dbTags, "trail_active=true")
	}
	tagsJSON, _ := json.Marshal(dbTags)

	dbPos := &models.DBManagedPosition{
		PositionID:        pos.ID,
		Symbol:            pos.Symbol,
		Side:              pos.Side,
		Strategy:          pos.Strategy,
		Quantity:          pos.Quantity,
		EntryPrice:        pos.EntryPrice,
		EntryOrderID:      pos.EntryOrderID,
		EntryOrderType:    pos.EntryOrderType,
		AllocationDollars: pos.AllocationDollars,
		StopLossPrice:     pos.StopLossPrice,
		StopLossPercent:   pos.StopLossPercent,
		StopLossOrderID:   pos.StopLossOrderID,
		TrailingStop:      pos.TrailingStop,
		TrailingPercent:   pos.TrailingPercent,
		TakeProfitPrice:   pos.TakeProfitPrice,
		TakeProfitPercent: pos.TakeProfitPercent,
		TakeProfitOrderID: pos.TakeProfitOrderID,
		Status:            pos.Status,
		CurrentPrice:      pos.CurrentPrice,
		UnrealizedPL:      pos.UnrealizedPL,
		UnrealizedPLPC:    pos.UnrealizedPLPC,
		RemainingQty:      pos.RemainingQty,
		Notes:             pos.Notes,
		Tags:              string(tagsJSON),
		PartialExitOrders: string(partialExitOrdersJSON),
		ClosedAt:          pos.ClosedAt,
	}

	if pos.PartialExit != nil {
		dbPos.PartialExitEnabled = pos.PartialExit.Enabled
		dbPos.PartialExitPercent = pos.PartialExit.Percent
		dbPos.PartialExitTargetPercent = pos.PartialExit.TargetPercent
		dbPos.PartialExitTargetPrice = pos.PartialExit.TargetPrice
	}

	return dbPos
}

// dbToManagedPosition converts DBManagedPosition to ManagedPosition
func (pm *PositionManager) dbToManagedPosition(dbPos *models.DBManagedPosition) *ManagedPosition {
	var partialExitOrders []string
	if dbPos.PartialExitOrders != "" {
		json.Unmarshal([]byte(dbPos.PartialExitOrders), &partialExitOrders)
	}

	var tags []string
	var trailAct, trailDist float64
	var trailActive bool
	if dbPos.Tags != "" {
		json.Unmarshal([]byte(dbPos.Tags), &tags)
		// [QA] Extraer trailing stop del JSON de Tags
		for _, tag := range tags {
			if strings.HasPrefix(tag, "trail_act=") {
				fmt.Sscanf(tag, "trail_act=%f", &trailAct)
			} else if strings.HasPrefix(tag, "trail_dist=") {
				fmt.Sscanf(tag, "trail_dist=%f", &trailDist)
			} else if tag == "trail_active=true" {
				trailActive = true
			}
		}
	}

	pos := &ManagedPosition{
		ID:                dbPos.PositionID,
		Symbol:            dbPos.Symbol,
		Side:              dbPos.Side,
		Strategy:          dbPos.Strategy,
		Quantity:          dbPos.Quantity,
		EntryPrice:        dbPos.EntryPrice,
		EntryOrderID:      dbPos.EntryOrderID,
		EntryOrderType:    dbPos.EntryOrderType,
		AllocationDollars: dbPos.AllocationDollars,
		StopLossPrice:     dbPos.StopLossPrice,
		StopLossPercent:   dbPos.StopLossPercent,
		StopLossOrderID:   dbPos.StopLossOrderID,
		TrailingStop:      dbPos.TrailingStop,
		TrailingPercent:   dbPos.TrailingPercent,
		TrailingActivationPoints: trailAct,
		TrailingDistancePoints:   trailDist,
		TrailingActive:           trailActive,
		TakeProfitPrice:   dbPos.TakeProfitPrice,
		TakeProfitPercent: dbPos.TakeProfitPercent,
		TakeProfitOrderID: dbPos.TakeProfitOrderID,
		Status:            dbPos.Status,
		CurrentPrice:      dbPos.CurrentPrice,
		UnrealizedPL:      dbPos.UnrealizedPL,
		UnrealizedPLPC:    dbPos.UnrealizedPLPC,
		RemainingQty:      dbPos.RemainingQty,
		Notes:             dbPos.Notes,
		Tags:              tags,
		PartialExitOrders: partialExitOrders,
		CreatedAt:         dbPos.CreatedAt,
		UpdatedAt:         dbPos.UpdatedAt,
		ClosedAt:          dbPos.ClosedAt,
	}

	if dbPos.PartialExitEnabled {
		pos.PartialExit = &PartialExitConfig{
			Enabled:       dbPos.PartialExitEnabled,
			Percent:       dbPos.PartialExitPercent,
			TargetPercent: dbPos.PartialExitTargetPercent,
			TargetPrice:   dbPos.PartialExitTargetPrice,
		}
	}

	return pos
}