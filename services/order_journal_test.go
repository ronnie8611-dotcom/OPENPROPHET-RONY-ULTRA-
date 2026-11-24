package services

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"prophet-trader/database"
	"prophet-trader/interfaces"

	"github.com/sirupsen/logrus"
)

type fakeJournalTrading struct {
	interfaces.TradingService
	placeFn func(context.Context, *interfaces.Order) (*interfaces.OrderResult, error)
	listFn  func(context.Context, string) ([]*interfaces.Order, error)
}

func (f *fakeJournalTrading) PlaceOrder(ctx context.Context, o *interfaces.Order) (*interfaces.OrderResult, error) {
	if f.placeFn != nil {
		return f.placeFn(ctx, o)
	}
	return &interfaces.OrderResult{OrderID: "B1", Status: "submitted"}, nil
}

func (f *fakeJournalTrading) ListOrders(ctx context.Context, status string) ([]*interfaces.Order, error) {
	if f.listFn != nil {
		return f.listFn(ctx, status)
	}
	return nil, nil
}

func newJournalForTest(t *testing.T) (*JournalTradingService, *database.LocalStorage) {
	t.Helper()
	store, err := database.NewLocalStorage(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fake := &fakeJournalTrading{}
	return NewJournalTradingService(fake, store, logrus.New()), store
}

func TestJournalPlaceOrderSuccessAndFailure(t *testing.T) {
	journal, store := newJournalForTest(t)

	order := &interfaces.Order{Symbol: "QQQ", Qty: 1, Side: "buy", Type: "market"}
	if _, err := journal.PlaceOrder(context.Background(), order); err != nil {
		t.Fatalf("PlaceOrder success: %v", err)
	}
	rec, err := store.GetOrderByClientID(order.ClientOrderID)
	if err != nil {
		t.Fatalf("GetOrderByClientID: %v", err)
	}
	if rec.SubmitState != "submitted" || rec.ID != "B1" {
		t.Fatalf("journal record = submit_state %q id %q, want submitted/B1", rec.SubmitState, rec.ID)
	}

	fake := journal.TradingService.(*fakeJournalTrading)
	fake.placeFn = func(context.Context, *interfaces.Order) (*interfaces.OrderResult, error) {
		return nil, errors.New("broker timeout")
	}
	order2 := &interfaces.Order{Symbol: "SPY", Qty: 2, Side: "buy", Type: "market"}
	if _, err := journal.PlaceOrder(context.Background(), order2); err == nil {
		t.Fatal("expected broker error")
	}
	rec2, err := store.GetOrderByClientID(order2.ClientOrderID)
	if err != nil {
		t.Fatalf("GetOrderByClientID: %v", err)
	}
	if rec2.SubmitState != "submit_failed" {
		t.Fatalf("failed order submit_state = %q, want submit_failed", rec2.SubmitState)
	}
}

func TestJournalReconcilesPendingOrder(t *testing.T) {
	journal, store := newJournalForTest(t)
	now := time.Now()
	pending := &interfaces.Order{
		ID:            "coid_pending",
		ClientOrderID: "coid_pending",
		Symbol:        "QQQ",
		Qty:           3,
		Side:          "buy",
		Type:          "limit",
		Status:        "submitting",
		SubmitState:   "submitting",
		SubmittedAt:   now.Add(-2 * time.Minute),
	}
	if err := store.SaveOrder(pending); err != nil {
		t.Fatalf("SaveOrder: %v", err)
	}

	fake := journal.TradingService.(*fakeJournalTrading)
	fake.listFn = func(context.Context, string) ([]*interfaces.Order, error) {
		return []*interfaces.Order{{
			ID: "B99", Symbol: "QQQ", Qty: 3, Side: "buy", Type: "limit",
			Status: "Submitted", SubmittedAt: now,
		}}, nil
	}

	if err := journal.ReconcilePendingOrders(context.Background()); err != nil {
		t.Fatalf("ReconcilePendingOrders: %v", err)
	}
	rec, err := store.GetOrderByClientID("coid_pending")
	if err != nil {
		t.Fatalf("GetOrderByClientID: %v", err)
	}
	if rec.SubmitState != "submitted" || rec.ID != "B99" {
		t.Fatalf("reconciled order = submit_state %q id %q, want submitted/B99", rec.SubmitState, rec.ID)
	}
}

func TestJournalReconcileMarksMissingOldOrder(t *testing.T) {
	journal, store := newJournalForTest(t)
	old := time.Now().Add(-30 * time.Minute)
	pending := &interfaces.Order{
		ID:            "coid_old",
		ClientOrderID: "coid_old",
		Symbol:        "IWM",
		Qty:           1,
		Side:          "sell",
		Type:          "market",
		Status:        "submitting",
		SubmitState:   "submitting",
		SubmittedAt:   old,
	}
	if err := store.SaveOrder(pending); err != nil {
		t.Fatalf("SaveOrder: %v", err)
	}
	if err := journal.ReconcilePendingOrders(context.Background()); err != nil {
		t.Fatalf("ReconcilePendingOrders: %v", err)
	}
	rec, err := store.GetOrderByClientID("coid_old")
	if err != nil {
		t.Fatalf("GetOrderByClientID: %v", err)
	}
	if rec.SubmitState != "submit_failed" {
		t.Fatalf("missing order submit_state = %q, want submit_failed", rec.SubmitState)
	}
}
