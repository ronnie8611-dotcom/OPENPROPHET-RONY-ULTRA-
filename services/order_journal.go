package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"prophet-trader/database"
	"prophet-trader/interfaces"

	"github.com/sirupsen/logrus"
)

// JournalTradingService persists every order intent before it reaches the
// broker, so a crash or timeout cannot silently duplicate an order. Exits and
// protective orders fail open (the stop is never withheld); entries fail closed.
type JournalTradingService struct {
	interfaces.TradingService
	storage *database.LocalStorage
	logger  *logrus.Logger
}

var _ interfaces.TradingService = (*JournalTradingService)(nil)

func NewJournalTradingService(inner interfaces.TradingService, storage *database.LocalStorage, logger *logrus.Logger) *JournalTradingService {
	return &JournalTradingService{TradingService: inner, storage: storage, logger: logger}
}

func newClientOrderID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("coid_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func isProtectiveOrder(o *interfaces.Order) bool {
	if o == nil {
		return false
	}
	if strings.Contains(strings.ToLower(o.Type), "stop") {
		return true
	}
	return o.StopLossPrice != nil || o.TakeProfitPrice != nil
}

func (j *JournalTradingService) journalOrder(o *interfaces.Order) error {
	if j.storage == nil {
		return nil
	}
	if o.ClientOrderID == "" {
		o.ClientOrderID = newClientOrderID()
	}
	if o.SubmittedAt.IsZero() {
		o.SubmittedAt = time.Now()
	}
	o.SubmitState = "submitting"
	if o.Status == "" {
		o.Status = "submitting"
	}
	if o.ID == "" {
		o.ID = o.ClientOrderID
	}
	return j.storage.SaveOrder(o)
}

func (j *JournalTradingService) finish(clientOrderID, brokerOrderID, status, submitState, note string) {
	if j.storage == nil {
		return
	}
	if err := j.storage.UpdateOrderSubmission(clientOrderID, brokerOrderID, status, submitState, note); err != nil {
		j.logger.WithError(err).Error("failed to update order journal")
	}
}

func (j *JournalTradingService) PlaceOrder(ctx context.Context, order *interfaces.Order) (*interfaces.OrderResult, error) {
	protective := isProtectiveOrder(order)
	if err := j.journalOrder(order); err != nil {
		if protective {
			j.logger.WithError(err).Error("order journal failed for a protective order; submitting anyway (fail-open)")
		} else {
			return nil, fmt.Errorf("order journal unavailable (fail-closed): %w", err)
		}
	}

	res, err := j.TradingService.PlaceOrder(ctx, order)
	if err != nil {
		j.finish(order.ClientOrderID, "", "submit_failed", "submit_failed", err.Error())
		return nil, err
	}

	brokerID, status := "", "submitted"
	if res != nil {
		brokerID = res.OrderID
		if res.Status != "" {
			status = res.Status
		}
	}
	j.finish(order.ClientOrderID, brokerID, status, "submitted", "")
	return res, nil
}

func (j *JournalTradingService) PlaceOptionsOrder(ctx context.Context, order *interfaces.OptionsOrder) (*interfaces.OrderResult, error) {
	synthetic := &interfaces.Order{
		Symbol:      order.Symbol,
		Qty:         order.Qty,
		Side:        strings.ToLower(order.Side),
		Type:        order.Type,
		LimitPrice:  order.LimitPrice,
		SubmittedAt: time.Now(),
	}
	protective := strings.Contains(strings.ToLower(order.PositionIntent), "to_close")
	if err := j.journalOrder(synthetic); err != nil {
		if protective {
			j.logger.WithError(err).Error("order journal failed for an options close; submitting anyway (fail-open)")
		} else {
			return nil, fmt.Errorf("options order journal unavailable (fail-closed): %w", err)
		}
	}

	res, err := j.TradingService.PlaceOptionsOrder(ctx, order)
	if err != nil {
		j.finish(synthetic.ClientOrderID, "", "submit_failed", "submit_failed", err.Error())
		return nil, err
	}

	brokerID, status := "", "submitted"
	if res != nil {
		brokerID = res.OrderID
		if res.Status != "" {
			status = res.Status
		}
	}
	j.finish(synthetic.ClientOrderID, brokerID, status, "submitted", "")
	return res, nil
}

func (j *JournalTradingService) PlaceComboOrder(ctx context.Context, order *interfaces.ComboOrder) (*interfaces.OrderResult, error) {
	legs := make([]string, 0, len(order.Legs))
	for _, l := range order.Legs {
		legs = append(legs, fmt.Sprintf("%s %s x%d", l.Action, l.Symbol, l.Ratio))
	}
	synthetic := &interfaces.Order{
		Symbol:      "COMBO[" + strings.Join(legs, " / ") + "]",
		Qty:         order.Qty,
		Side:        strings.ToLower(order.Action),
		Type:        order.OrderType,
		LimitPrice:  order.LimitPrice,
		SubmittedAt: time.Now(),
	}
	if err := j.journalOrder(synthetic); err != nil {
		return nil, fmt.Errorf("combo order journal unavailable (fail-closed): %w", err)
	}

	res, err := j.TradingService.PlaceComboOrder(ctx, order)
	if err != nil {
		j.finish(synthetic.ClientOrderID, "", "submit_failed", "submit_failed", err.Error())
		return nil, err
	}

	brokerID, status := "", "submitted"
	if res != nil {
		brokerID = res.OrderID
		if res.Status != "" {
			status = res.Status
		}
	}
	j.finish(synthetic.ClientOrderID, brokerID, status, "submitted", "")
	return res, nil
}

func (j *JournalTradingService) CancelOrder(ctx context.Context, orderID string) error {
	err := j.TradingService.CancelOrder(ctx, orderID)
	if err == nil && j.storage != nil {
		if uerr := j.storage.UpdateOrderStatusByBrokerID(orderID, "canceled", "canceled"); uerr != nil {
			j.logger.WithError(uerr).Warn("failed to mark order canceled in journal")
		}
	}
	return err
}

// ReconcilePendingOrders repairs local journal state against broker truth after
// a restart. It only marks an order submitted when it is found at the broker;
// otherwise it marks submit_failed and alerts instead of assuming a fill.
func (j *JournalTradingService) ReconcilePendingOrders(ctx context.Context) error {
	if j.storage == nil {
		return nil
	}
	local, err := j.storage.GetOrdersBySubmitState([]string{"submitting", "submit_failed"})
	if err != nil {
		return err
	}
	if len(local) == 0 {
		return nil
	}

	brokerOrders, err := j.TradingService.ListOrders(ctx, "")
	if err != nil {
		j.logger.WithError(err).Warn("reconcile: could not list broker orders; journal left unchanged")
		return nil
	}

	for _, lo := range local {
		if lo == nil {
			continue
		}
		var match *interfaces.Order
		for _, bo := range brokerOrders {
			if bo == nil {
				continue
			}
			if lo.ID != "" && bo.ID == lo.ID {
				match = bo
				break
			}
			sameSymbol := strings.EqualFold(bo.Symbol, lo.Symbol)
			sameSide := strings.EqualFold(bo.Side, lo.Side)
			sameQty := math.Abs(bo.Qty-lo.Qty) < 0.0001
			closeInTime := !bo.SubmittedAt.IsZero() && !lo.SubmittedAt.IsZero() &&
				math.Abs(bo.SubmittedAt.Sub(lo.SubmittedAt).Minutes()) <= 15
			if sameSymbol && sameSide && sameQty && closeInTime {
				match = bo
				break
			}
		}

		if match != nil {
			j.finish(lo.ClientOrderID, match.ID, match.Status, "submitted", "reconciled at startup")
			j.logger.WithFields(logrus.Fields{
				"client_order_id": lo.ClientOrderID,
				"broker_order_id": match.ID,
				"symbol":          lo.Symbol,
			}).Warn("journal order reconciled with broker")
			continue
		}

		if !lo.SubmittedAt.IsZero() && time.Since(lo.SubmittedAt) > 15*time.Minute {
			j.finish(lo.ClientOrderID, "", "submit_failed", "submit_failed", "not found at broker after restart")
			SendTelegramAlert(fmt.Sprintf("⚠️ Orden no encontrada en IBKR tras reinicio: %s %s qty=%.0f (client %s)", lo.Side, lo.Symbol, lo.Qty, lo.ClientOrderID))
		}
	}
	return nil
}

