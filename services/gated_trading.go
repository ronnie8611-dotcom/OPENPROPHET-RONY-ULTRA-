package services

import (
	"context"
	"fmt"
	"log"
	"prophet-trader/config"
	"prophet-trader/interfaces"
	"sync/atomic"
	"time"
)

// GatedTradingService wraps a TradingService with a master kill-switch.
type GatedTradingService struct {
	inner   interfaces.TradingService
	enabled atomic.Bool
}

var _ interfaces.TradingService = (*GatedTradingService)(nil)

// NewGatedTradingService wraps inner. enabled=false means dry-run (no orders).
func NewGatedTradingService(inner interfaces.TradingService, enabled bool) *GatedTradingService {
	g := &GatedTradingService{inner: inner}
	g.enabled.Store(enabled)
	if enabled {
		log.Printf("[GATE] trading ENABLED — orders will be sent to the broker")
	} else {
		log.Printf("[GATE] trading DISABLED (dry-run) — order intent will be logged but not sent")
	}
	return g
}

// Enabled reports the connection kill-switch state.
func (g *GatedTradingService) Enabled() bool { return g.enabled.Load() }

// CanTrade checks if the switch is on AND the live trading mode allows execution.
func (g *GatedTradingService) CanTrade() bool {
	if !g.enabled.Load() {
		return false
	}
	mode := config.CurrentTradingMode()
	return mode.AllowsExecution()
}

// [FIX BUG #13] Disable turns off order placement at runtime AND cancels all open orders.
func (g *GatedTradingService) Disable(reason string) {
	if g.enabled.Swap(false) {
		log.Printf("[GATE] trading DISABLED at runtime: %s", reason)
		g.CancelAllOpenOrders(reason)
	}
}

// Enable turns on order placement at runtime.
func (g *GatedTradingService) Enable(reason string) {
	if !g.enabled.Swap(true) {
		log.Printf("[GATE] trading ENABLED at runtime: %s", reason)
	}
}

// [FIX BUG #13] CancelAllOpenOrders cancels any live orders to avoid unprotected positions.
func (g *GatedTradingService) CancelAllOpenOrders(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orders, err := g.inner.ListOrders(ctx, "open")
	if err != nil {
		log.Printf("[GATE] Failed to list open orders during disable: %v", err)
		return
	}

	for _, o := range orders {
		if err := g.inner.CancelOrder(ctx, o.ID); err != nil {
			log.Printf("[GATE] Failed to cancel order %s during disable: %v", o.ID, err)
		} else {
			log.Printf("[GATE] Cancelled order %s during disable: %s", o.ID, reason)
		}
	}
}

// blocked logs the dry-run intent and returns the standard refusal error.
func (g *GatedTradingService) blocked(action string) error {
	log.Printf("[GATE][DRY-RUN] %s suppressed — trading disabled; no order sent", action)
	return fmt.Errorf("trading disabled (dry-run mode): %s not sent", action)
}

func (g *GatedTradingService) PlaceOrder(ctx context.Context, order *interfaces.Order) (*interfaces.OrderResult, error) {
	if !g.CanTrade() {
		return nil, g.blocked(fmt.Sprintf("PlaceOrder %s %s qty=%v type=%s", order.Side, order.Symbol, order.Qty, order.Type))
	}
	return g.inner.PlaceOrder(ctx, order)
}

func (g *GatedTradingService) CancelOrder(ctx context.Context, orderID string) error {
	if !g.CanTrade() {
		return g.blocked("CancelOrder " + orderID)
	}
	return g.inner.CancelOrder(ctx, orderID)
}

func (g *GatedTradingService) PlaceOptionsOrder(ctx context.Context, order *interfaces.OptionsOrder) (*interfaces.OrderResult, error) {
	if !g.CanTrade() {
		return nil, g.blocked("PlaceOptionsOrder")
	}
	return g.inner.PlaceOptionsOrder(ctx, order)
}

func (g *GatedTradingService) PlaceComboOrder(ctx context.Context, order *interfaces.ComboOrder) (*interfaces.OrderResult, error) {
	if !g.CanTrade() {
		return nil, g.blocked("PlaceComboOrder")
	}
	return g.inner.PlaceComboOrder(ctx, order)
}

// --- read-only pass-throughs ---

func (g *GatedTradingService) GetOrder(ctx context.Context, orderID string) (*interfaces.Order, error) {
	return g.inner.GetOrder(ctx, orderID)
}

func (g *GatedTradingService) ListOrders(ctx context.Context, status string) ([]*interfaces.Order, error) {
	return g.inner.ListOrders(ctx, status)
}

func (g *GatedTradingService) GetPositions(ctx context.Context) ([]*interfaces.Position, error) {
	return g.inner.GetPositions(ctx)
}

func (g *GatedTradingService) GetAccount(ctx context.Context) (*interfaces.Account, error) {
	return g.inner.GetAccount(ctx)
}

func (g *GatedTradingService) GetOptionsChain(ctx context.Context, underlying string, expiration time.Time) ([]*interfaces.OptionContract, error) {
	return g.inner.GetOptionsChain(ctx, underlying, expiration)
}

func (g *GatedTradingService) GetOptionsQuote(ctx context.Context, symbol string) (*interfaces.OptionsQuote, error) {
	return g.inner.GetOptionsQuote(ctx, symbol)
}

func (g *GatedTradingService) GetOptionsPosition(ctx context.Context, symbol string) (*interfaces.OptionsPosition, error) {
	return g.inner.GetOptionsPosition(ctx, symbol)
}

func (g *GatedTradingService) ListOptionsPositions(ctx context.Context) ([]*interfaces.OptionsPosition, error) {
	return g.inner.ListOptionsPositions(ctx)
}