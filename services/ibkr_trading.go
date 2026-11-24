package services

import (
	"context"
	"fmt"
	"log"
	"math"
	"prophet-trader/interfaces"
	"prophet-trader/tws"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
)

type IBKRTradingService struct {
	tws.DefaultWrapper
	client      *tws.Client
	resolver    *tws.ContractResolver
	dataService interfaces.DataService
	globalReqMu sync.Mutex
	
	// [QA] Mutex para evitar colisiones en la generación de OrderIDs
	reqIdMu     sync.Mutex

	// [FIX BUG #7] Contador separado para peticiones de datos (no órdenes)
	reqIDCounter int64

	cacheMu    sync.RWMutex
	orderCache map[string]*interfaces.Order

	posMu        sync.RWMutex
	posCache     []*interfaces.Position
	posReady     chan struct{}
	posSubscribed bool
}

func (s *IBKRTradingService) SetDataService(ds interfaces.DataService) {
	s.dataService = ds
}

// Ensure IBKRTradingService implements interfaces.TradingService
var _ interfaces.TradingService = (*IBKRTradingService)(nil)

func NewIBKRTradingService(client *tws.Client, resolver *tws.ContractResolver) *IBKRTradingService {
	s := &IBKRTradingService{
		client:     client,
		resolver:   resolver,
		orderCache: make(map[string]*interfaces.Order),
	}
	client.AddWrapper(s)
	return s
}

// [QA] Generador de OrderID seguro para hilos
func (s *IBKRTradingService) nextReqId() int64 {
	s.reqIdMu.Lock()
	defer s.reqIdMu.Unlock()
	return s.client.NextOrderId()
}

// [FIX BUG #7] Generador de IDs para peticiones de datos (ej. AccountSummary, MarketData)
// Usa un contador separado para no consumir OrderIDs de TWS.
func (s *IBKRTradingService) nextDataReqId() int64 {
	return atomic.AddInt64(&s.reqIDCounter, 1) + 1000000
}

// OnDisconnect clears stale caches when IB Gateway drops.
func (s *IBKRTradingService) OnDisconnect() {
	s.cacheMu.Lock()
	s.orderCache = make(map[string]*interfaces.Order)
	s.cacheMu.Unlock()

	s.posMu.Lock()
	s.posCache = nil
	s.posSubscribed = false
	s.posMu.Unlock()
}

// OnReconnect re-subscribes to account updates after a successful reconnect.
func (s *IBKRTradingService) OnReconnect() {
	s.SubscribePositions()
}

// SubscribePositions starts a persistent reqAccountUpdates subscription.
func (s *IBKRTradingService) SubscribePositions() {
	s.posMu.Lock()
	if s.posSubscribed {
		s.posMu.Unlock()
		return
	}
	s.posReady = make(chan struct{})
	s.posSubscribed = true
	s.posMu.Unlock()

	accts := strings.Split(s.client.Accounts(), ",")
	acct := ""
	if len(accts) > 0 {
		acct = strings.TrimSpace(accts[0])
	}
	if acct == "" {
		log.Printf("[IBKR] SubscribePositions error: no account name available yet")
		s.posMu.Lock()
		s.posSubscribed = false
		s.posMu.Unlock()
		return
	}

	if err := s.client.Encoder().ReqAccountUpdates(true, acct); err != nil {
		log.Printf("[IBKR] SubscribePositions error: %v", err)
		s.posMu.Lock()
		s.posSubscribed = false
		s.posMu.Unlock()
	}
}

func (s *IBKRTradingService) UpdatePortfolio(contract tws.Contract, position decimal.Decimal, marketPrice, marketValue, averageCost, unrealizedPNL, realizedPNL float64, accountName string) {
	qty := position.InexactFloat64()
	if qty == 0 {
		sym := s.resolver.Format(contract)
		s.posMu.Lock()
		for i, existing := range s.posCache {
			if existing.Symbol == sym {
				s.posCache = append(s.posCache[:i], s.posCache[i+1:]...)
				break
			}
		}
		s.posMu.Unlock()
		return
	}

	avgPrice := averageCost
	multiplier := 1.0
	if contract.Multiplier != "" {
		if m, err := strconv.ParseFloat(contract.Multiplier, 64); err == nil && m > 0 {
			multiplier = m
			avgPrice = averageCost / m
		}
	}

	absQty := qty
	side := "long"
	if qty < 0 {
		absQty = -qty
		side = "short"
	}

	costBasis := avgPrice * absQty * multiplier
	sym := s.resolver.Format(contract)

	p := &interfaces.Position{
		Symbol:        sym,
		Qty:           absQty,
		AvgEntryPrice: avgPrice,
		MarketValue:   marketValue,
		CostBasis:     costBasis,
		UnrealizedPL:  unrealizedPNL,
		CurrentPrice:  marketPrice,
		Side:          side,
	}
	if costBasis != 0 {
		p.UnrealizedPLPC = unrealizedPNL / costBasis
	}

	s.posMu.Lock()
	found := false
	for i, existing := range s.posCache {
		if existing.Symbol == sym {
			s.posCache[i] = p
			found = true
			break
		}
	}
	if !found {
		s.posCache = append(s.posCache, p)
	}
	s.posMu.Unlock()
}

func (s *IBKRTradingService) AccountDownloadEnd(accountName string) {
	s.posMu.Lock()
	select {
	case <-s.posReady:
	default:
		close(s.posReady)
	}
	s.posMu.Unlock()
}

func normalizeOrderType(t string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "market", "mkt":
		return "MKT", nil
	case "limit", "lmt":
		return "LMT", nil
	case "stop", "stp":
		return "STP", nil
	case "stop_limit", "stop limit", "stp lmt":
		return "STP LMT", nil
	case "midprice", "mid", "midpoint":
		return "MIDPRICE", nil
	case "":
		return "", fmt.Errorf("order type is required (market/limit/stop); refusing to default to a market order")
	default:
		return "", fmt.Errorf("unsupported order type %q", t)
	}
}

func (s *IBKRTradingService) PlaceOrder(ctx context.Context, order *interfaces.Order) (*interfaces.OrderResult, error) {
	contract, err := s.resolver.Resolve(ctx, order.Symbol)
	if err != nil {
		return nil, fmt.Errorf("PlaceOrder: %w", err)
	}

	if contract.ConId == 0 && contract.SecType == tws.Option {
		details, err := s.client.ReqContractDetails(ctx, contract)
		if err != nil {
			return nil, fmt.Errorf("PlaceOrder: resolve contract details: %w", err)
		}
		if len(details) == 0 {
			return nil, fmt.Errorf("PlaceOrder: no matching contract for %s", order.Symbol)
		}
		if len(details) > 1 {
			return nil, fmt.Errorf("PlaceOrder: ambiguous contract for %s (%d matches)", order.Symbol, len(details))
		}
		contract = details[0].Contract
		if len(contract.LastTradeDateOrContractMonth) > 8 {
			contract.LastTradeDateOrContractMonth = contract.LastTradeDateOrContractMonth[:8]
		}
		log.Printf("[IBKR] Resolved option conid=%d local=%s exch=%s",
			contract.ConId, contract.LocalSymbol, contract.Exchange)
	}

	parentID := s.nextReqId()

	side := "BUY"
	reverseSide := "SELL"
	if strings.EqualFold(order.Side, "sell") {
		side = "SELL"
		reverseSide = "BUY"
	}

	orderType, err := normalizeOrderType(order.Type)
	if err != nil {
		return nil, fmt.Errorf("PlaceOrder: %w", err)
	}

	parentOrder := tws.Order{
		Action:        side,
		TotalQuantity: decimal.NewFromFloat(order.Qty),
		OrderType:     orderType,
		Tif:           order.TimeInForce,
		LmtPrice:      tws.UnsetFloat,
		AuxPrice:      tws.UnsetFloat,
	}

	if order.LimitPrice != nil {
		parentOrder.LmtPrice = *order.LimitPrice
	}
	if order.StopPrice != nil {
		parentOrder.AuxPrice = *order.StopPrice
	}
	if parentOrder.Tif == "" {
		parentOrder.Tif = "DAY"
	}

	var tpID, slID int64
	var tpOrder, slOrder tws.Order

	if order.TakeProfitPrice != nil {
		tpID = s.nextReqId()
		tpOrder = tws.Order{
			Action:        reverseSide,
			TotalQuantity: parentOrder.TotalQuantity,
			OrderType:     "LMT",
			Tif:           parentOrder.Tif,
			LmtPrice:      *order.TakeProfitPrice,
			AuxPrice:      tws.UnsetFloat,
			ParentId:      parentID,
		}
	}

	eurexOpt := strings.EqualFold(contract.Exchange, "EUREX") && contract.SecType == tws.Option
	if order.StopLossPrice != nil && !eurexOpt {
		slID = s.nextReqId()
		slOrder = tws.Order{
			Action:        reverseSide,
			TotalQuantity: parentOrder.TotalQuantity,
			OrderType:     "STP",
			Tif:           parentOrder.Tif,
			LmtPrice:      tws.UnsetFloat,
			AuxPrice:      *order.StopLossPrice,
			ParentId:      parentID,
		}
	}

	if tpID == 0 && slID == 0 {
		parentOrder.Transmit = true
	} else if slID > 0 {
		parentOrder.Transmit = false
		if tpID > 0 {
			tpOrder.Transmit = false
		}
		slOrder.Transmit = true
	} else if tpID > 0 {
		parentOrder.Transmit = false
		tpOrder.Transmit = true
	}

	lmtStr := "unset"
	if order.LimitPrice != nil {
		lmtStr = fmt.Sprintf("%.4f", *order.LimitPrice)
	}
	log.Printf("[IBKR] ORDER INTENT id=%d %s %s qty=%v type=%s lmt=%s tif=%s tp=%v sl=%v exchange=%s sectype=%s conid=%d (paper)",
		parentID, side, order.Symbol, order.Qty, orderType, lmtStr, parentOrder.Tif, tpID > 0, slID > 0,
		contract.Exchange, contract.SecType, contract.ConId)

	type resultMsg struct {
		id  int64
		msg any
	}
	aggCh := make(chan resultMsg, 30)

	ids := []int64{parentID}
	if tpID > 0 {
		ids = append(ids, tpID)
	}
	if slID > 0 {
		ids = append(ids, slID)
	}

	for _, id := range ids {
		id := id
		ch := s.client.Register(id)
		defer s.client.Complete(id)
		go func() {
			for msg := range ch {
				aggCh <- resultMsg{id: id, msg: msg}
			}
		}()
	}

	if err := s.client.Encoder().PlaceOrder(s.client.ServerVersion(), parentID, contract, parentOrder); err != nil {
		return nil, fmt.Errorf("PlaceOrder parent encode/send failed: %w", err)
	}
	if tpID > 0 {
		if err := s.client.Encoder().PlaceOrder(s.client.ServerVersion(), tpID, contract, tpOrder); err != nil {
			s.cancelForCleanup(parentID, "TP send failed")
			return nil, fmt.Errorf("PlaceOrder TP encode/send failed: %w", err)
		}
	}
	if slID > 0 {
		if err := s.client.Encoder().PlaceOrder(s.client.ServerVersion(), slID, contract, slOrder); err != nil {
			s.cancelForCleanup(parentID, "SL send failed")
			return nil, fmt.Errorf("PlaceOrder SL encode/send failed: %w", err)
		}
	}

	confirmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		select {
		case rm := <-aggCh:
			switch t := rm.msg.(type) {
			case tws.OrderStatusMsg:
				if rm.id == parentID {
					var tpStr, slStr string
					if tpID > 0 {
						tpStr = fmt.Sprintf("%d", tpID)
					}
					if slID > 0 {
						slStr = fmt.Sprintf("%d", slID)
					}

					return &interfaces.OrderResult{
						OrderID:           fmt.Sprintf("%d", parentID),
						TakeProfitOrderID: tpStr,
						StopLossOrderID:   slStr,
						Status:            "submitted",
						Message:           fmt.Sprintf("Parent order %d submitted; awaiting fills", parentID),
					}, nil
				}
			case error:
				s.cancelForCleanup(parentID, fmt.Sprintf("leg %d rejected", rm.id))
				return nil, fmt.Errorf("order %d rejected by TWS: %w", rm.id, t)
			}
		case <-confirmCtx.Done():
			var tpStr, slStr string
			if tpID > 0 {
				tpStr = fmt.Sprintf("%d", tpID)
			}
			if slID > 0 {
				slStr = fmt.Sprintf("%d", slID)
			}

			return &interfaces.OrderResult{
				OrderID:           strconv.FormatInt(parentID, 10),
				TakeProfitOrderID: tpStr,
				StopLossOrderID:   slStr,
				Status:            "PendingConfirm",
				Message:           "Order sent; no acknowledgement yet — reconcile via ListOrders",
			}, nil
		}
	}
}

func (s *IBKRTradingService) cancelForCleanup(parentID int64, reason string) {
	log.Printf("[IBKR] CLEANUP CANCEL id=%d: %s (cancel parent; cascades to children)", parentID, reason)
	_ = s.client.Encoder().CancelOrder(s.client.ServerVersion(), parentID)
}

func (s *IBKRTradingService) CancelOrder(ctx context.Context, orderID string) error {
	reqId, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid orderID %q: %w", orderID, err)
	}
	log.Printf("[IBKR] CANCEL INTENT id=%d (paper)", reqId)
	if err := s.client.Encoder().CancelOrder(s.client.ServerVersion(), reqId); err != nil {
		return fmt.Errorf("CancelOrder failed: %w", err)
	}
	return nil
}

func (s *IBKRTradingService) OpenOrder(orderId int64, contract tws.Contract, order tws.Order, orderState tws.OrderState) {
	log.Printf("[IBKR] OpenOrder: %d %s %s %s -> %s", orderId, order.Action, order.TotalQuantity.String(), contract.Symbol, orderState.Status)
}

func (s *IBKRTradingService) OrderStatus(orderId int64, status string, filled, remaining decimal.Decimal, avgFillPrice float64, permId, parentId int64, lastFillPrice float64, clientId int, whyHeld string, mktCapPrice float64) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	idStr := strconv.FormatInt(orderId, 10)
	
	o, exists := s.orderCache[idStr]
	if !exists {
		o = &interfaces.Order{ID: idStr}
		s.orderCache[idStr] = o
	}
	o.Status = strings.ToLower(status)
	o.FilledQty = filled.InexactFloat64()
	if avgFillPrice > 0 {
		avg := avgFillPrice
		o.FilledAvgPrice = &avg
	}
	log.Printf("[IBKR] OrderStatus: %d status=%s filled=%s remaining=%s avgFill=%.4f",
		orderId, status, filled.String(), remaining.String(), avgFillPrice)
}

func (s *IBKRTradingService) GetOrder(ctx context.Context, orderID string) (*interfaces.Order, error) {
	s.cacheMu.RLock()
	if o, exists := s.orderCache[orderID]; exists {
		s.cacheMu.RUnlock()
		return o, nil
	}
	s.cacheMu.RUnlock()

	orders, err := s.ListOrders(ctx, "")
	if err == nil {
		for _, o := range orders {
			if o.ID == orderID {
				return o, nil
			}
		}
	}
	
	return nil, fmt.Errorf("order %s not found", orderID)
}

// [FIX BUG #6, #8, #19] ListOrders ya no cierra el canal manualmente (lo hace OpenOrderEnd)
// y filtra correctamente las órdenes inactivas.
func (s *IBKRTradingService) ListOrders(ctx context.Context, status string) ([]*interfaces.Order, error) {
	s.globalReqMu.Lock()
	defer s.globalReqMu.Unlock()

	// [FIX BUG #19] NO hacemos defer s.client.Complete(0) aquí.
	// El canal 0 es cerrado automáticamente por OpenOrderEnd en tws_client.go.
	ch := s.client.Register(0)

	if err := s.client.Encoder().ReqOpenOrders(); err != nil {
		s.client.Complete(0) // Limpieza en caso de error antes de recibir End
		return nil, fmt.Errorf("ReqOpenOrders error: %w", err)
	}

	var orders []*interfaces.Order
	
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 5*time.Second)
	defer timeoutCancel()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return s.filterOrders(orders, status), nil
			}
			
			switch t := msg.(type) {
			case tws.OpenOrderMsg:
				o := &interfaces.Order{
					ID:     strconv.FormatInt(t.OrderId, 10),
					Symbol: s.resolver.Format(t.Contract),
					Qty:    t.Order.TotalQuantity.InexactFloat64(),
					Side:   t.Order.Action,
					Type:   t.Order.OrderType,
					Status: t.OrderState.Status,
				}
				orders = append(orders, o)
			case tws.OpenOrderEndMsg:
				// Ignored: el canal se cerrará solo
			case error:
				return nil, fmt.Errorf("tws error: %w", t)
			}
		case <-timeoutCtx.Done():
			s.client.Complete(0) // Limpieza en caso de timeout
			if len(orders) > 0 {
				return s.filterOrders(orders, status), nil
			}
			return nil, fmt.Errorf("ListOrders timeout: no response from IBKR")
		}
	}
}

// [FIX BUG #8] Filtra las órdenes inactivas correctamente.
func (s *IBKRTradingService) filterOrders(orders []*interfaces.Order, status string) []*interfaces.Order {
	if status == "" {
		return orders
	}
	var filtered []*interfaces.Order
	for _, o := range orders {
		isActive := false
		switch o.Status {
		case "Submitted", "PreSubmitted", "PendingSubmit", "ApiPending", "PendingCancel":
			isActive = true
		}
		
		if status == "open" && isActive {
			filtered = append(filtered, o)
		} else if status == "closed" && !isActive {
			filtered = append(filtered, o)
		} else if status == o.Status {
			filtered = append(filtered, o)
		}
	}
	return filtered
}

func (s *IBKRTradingService) GetPositions(ctx context.Context) ([]*interfaces.Position, error) {
	s.posMu.RLock()
	subscribed := s.posSubscribed
	ready := s.posReady
	s.posMu.RUnlock()

	if !subscribed {
		s.SubscribePositions()
		s.posMu.RLock()
		ready = s.posReady
		s.posMu.RUnlock()
	}

	if ready != nil {
		select {
		case <-ready:
		case <-time.After(4 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	s.posMu.RLock()
	out := make([]*interfaces.Position, len(s.posCache))
	copy(out, s.posCache)
	s.posMu.RUnlock()
	return out, nil
}

// [FIX BUG #7] Usa nextDataReqId() en lugar de nextReqId()
func (s *IBKRTradingService) GetAccount(ctx context.Context) (*interfaces.Account, error) {
	reqId := s.nextDataReqId()
	ch := s.client.Register(reqId)
	defer s.client.Complete(reqId)

	if err := s.client.Encoder().ReqAccountSummary(reqId, "All", "NetLiquidation,TotalCashValue,BuyingPower,DayTradesRemaining"); err != nil {
		return nil, fmt.Errorf("ReqAccountSummary error: %w", err)
	}
	defer s.client.Encoder().CancelAccountSummary(reqId)

	acc := &interfaces.Account{
		ID: "IBKR_PAPER",
	}
	
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return acc, nil
			}
			
			switch t := msg.(type) {
			case tws.AccountSummaryMsg:
				acc.ID = t.Account
				if t.Tag == "NetLiquidation" {
					if val, err := strconv.ParseFloat(t.Value, 64); err == nil {
						acc.PortfolioValue = val
					}
				} else if t.Tag == "TotalCashValue" {
					if val, err := strconv.ParseFloat(t.Value, 64); err == nil {
						acc.Cash = val
					}
				} else if t.Tag == "BuyingPower" {
					if val, err := strconv.ParseFloat(t.Value, 64); err == nil {
						acc.BuyingPower = val
					}
				} else if t.Tag == "DayTradesRemaining" {
					if val, err := strconv.Atoi(t.Value); err == nil {
						acc.DayTradeCount = val
					}
				}
			case tws.AccountSummaryEndMsg:
				// Ignored: el canal se cerrará solo
			case error:
				return nil, fmt.Errorf("tws error: %w", t)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *IBKRTradingService) PlaceOptionsOrder(ctx context.Context, order *interfaces.OptionsOrder) (*interfaces.OrderResult, error) {
	regularOrder := &interfaces.Order{
		Symbol:     order.Symbol,
		Qty:        order.Qty,
		Side:       order.Side,
		Type:       order.Type,
		TimeInForce: order.TimeInForce,
		LimitPrice: order.LimitPrice,
	}
	return s.PlaceOrder(ctx, regularOrder)
}

func legContractMatches(want, got tws.Contract) bool {
	if want.SecType != tws.Option {
		return true
	}
	return got.Strike == want.Strike && strings.EqualFold(got.Right, want.Right)
}

func (s *IBKRTradingService) PlaceComboOrder(ctx context.Context, order *interfaces.ComboOrder) (*interfaces.OrderResult, error) {
	if len(order.Legs) < 2 {
		return nil, fmt.Errorf("PlaceComboOrder: need at least 2 legs, got %d", len(order.Legs))
	}

	var comboLegs []tws.ComboLeg
	var underlying string
	currency := "USD"

	for _, leg := range order.Legs {
		want, werr := tws.ParseSymbol(leg.Symbol)
		if werr != nil {
			return nil, fmt.Errorf("PlaceComboOrder: parse leg %q: %w", leg.Symbol, werr)
		}

		contract, err := s.resolver.Resolve(ctx, leg.Symbol)
		if err != nil {
			return nil, fmt.Errorf("PlaceComboOrder: resolve leg %q: %w", leg.Symbol, err)
		}
		if contract.ConId == 0 {
			details, err := s.client.ReqContractDetails(ctx, contract)
			if err != nil {
				return nil, fmt.Errorf("PlaceComboOrder: ReqContractDetails for %q: %w", leg.Symbol, err)
			}
			if len(details) != 1 {
				return nil, fmt.Errorf("PlaceComboOrder: expected 1 match for %q, got %d", leg.Symbol, len(details))
			}
			contract = details[0].Contract
		}

		if !legContractMatches(want, contract) {
			return nil, fmt.Errorf("PlaceComboOrder: leg %q resolved to the WRONG contract (got strike=%v right=%s conId=%d) — refusing to send",
				leg.Symbol, contract.Strike, contract.Right, contract.ConId)
		}

		if underlying == "" {
			underlying = contract.Symbol
			if contract.Currency != "" {
				currency = contract.Currency
			}
		}
		ratio := leg.Ratio
		if ratio <= 0 {
			ratio = 1
		}
		comboLegs = append(comboLegs, tws.ComboLeg{
			ConId:    contract.ConId,
			Ratio:    ratio,
			Action:   strings.ToUpper(leg.Action),
			Exchange: contract.Exchange,
		})
	}

	bagContract := tws.Contract{
		Symbol:   underlying,
		SecType:  tws.Bag,
		Exchange: comboLegs[0].Exchange,
		Currency: currency,
		ComboLegs: comboLegs,
	}

	orderType, err := normalizeOrderType(order.OrderType)
	if err != nil {
		return nil, fmt.Errorf("PlaceComboOrder: %w", err)
	}

	orderId := s.nextReqId()

	twsOrder := tws.Order{
		Action:        "BUY",
		TotalQuantity: decimal.NewFromFloat(order.Qty),
		OrderType:     orderType,
		Tif:           order.TimeInForce,
		LmtPrice:      tws.UnsetFloat,
		AuxPrice:      tws.UnsetFloat,
		Transmit:      true,
	}
	if order.LimitPrice != nil {
		twsOrder.LmtPrice = *order.LimitPrice
	}
	if twsOrder.Tif == "" {
		twsOrder.Tif = "DAY"
	}

	ch := s.client.Register(orderId)
	defer s.client.Complete(orderId)

	if err := s.client.Encoder().PlaceOrder(s.client.ServerVersion(), orderId, bagContract, twsOrder); err != nil {
		return nil, fmt.Errorf("PlaceComboOrder encode/send: %w", err)
	}

	confirmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		select {
		case msg := <-ch:
			switch t := msg.(type) {
			case tws.OrderStatusMsg:
				return &interfaces.OrderResult{
					OrderID: strconv.FormatInt(orderId, 10),
					Status:  t.Status,
				}, nil
			case error:
				return nil, fmt.Errorf("PlaceComboOrder rejected: %w", t)
			}
		case <-confirmCtx.Done():
			return &interfaces.OrderResult{
				OrderID: strconv.FormatInt(orderId, 10),
				Status:  "PendingSubmit",
			}, nil
		}
	}
}

func (s *IBKRTradingService) GetOptionsChain(ctx context.Context, underlying string, expiration time.Time) ([]*interfaces.OptionContract, error) {
	underContract, err := s.resolver.Resolve(ctx, underlying)
	if err != nil {
		return nil, fmt.Errorf("GetOptionsChain: resolve underlying %q: %w", underlying, err)
	}

	details, err := s.client.ReqContractDetails(ctx, underContract)
	if err != nil {
		return nil, fmt.Errorf("GetOptionsChain: ReqContractDetails for %q: %w", underlying, err)
	}
	if len(details) == 0 {
		return nil, fmt.Errorf("GetOptionsChain: no contract details found for %q", underlying)
	}
	underConId := details[0].Contract.ConId
	underSecType := string(details[0].Contract.SecType)

	sdops, err := s.client.ReqSecDefOptParams(ctx, underContract.Symbol, "", underSecType, underConId)
	if err != nil {
		return nil, fmt.Errorf("GetOptionsChain: ReqSecDefOptParams: %w", err)
	}

	expirationStr := expiration.Format("20060102")
	var matchedStrikes []float64
	var matchedTradingClass, matchedMultiplier, matchedExchange string
	for _, sdop := range sdops {
		hasExpiration := false
		for _, exp := range sdop.Expirations {
			if exp == expirationStr {
				hasExpiration = true
				break
			}
		}
		if !hasExpiration {
			continue
		}
		if underContract.Symbol == "ESTX50" && sdop.TradingClass != "OESX" {
			continue
		}
		matchedStrikes = sdop.Strikes
		matchedTradingClass = sdop.TradingClass
		matchedMultiplier = sdop.Multiplier
		matchedExchange = sdop.Exchange
		break
	}

	if len(matchedStrikes) == 0 {
		return nil, fmt.Errorf("GetOptionsChain: no strikes found for %s expiration %s", underlying, expirationStr)
	}

	sort.Float64s(matchedStrikes)

	spotPrice := 0.0
	if s.dataService != nil {
		quoteCtx, quoteCancel := context.WithTimeout(ctx, 5*time.Second)
		quote, qErr := s.dataService.GetLatestQuote(quoteCtx, underlying)
		quoteCancel()
		if qErr == nil && quote.BidPrice > 0 {
			spotPrice = quote.BidPrice
		}
	}

	if spotPrice > 0 {
		lo := spotPrice * 0.85
		hi := spotPrice * 1.15
		filtered := make([]float64, 0, len(matchedStrikes))
		for _, s := range matchedStrikes {
			if s >= lo && s <= hi {
				filtered = append(filtered, s)
			}
		}
		matchedStrikes = filtered
	}

	if len(matchedStrikes) == 0 {
		return nil, fmt.Errorf("GetOptionsChain: no strikes in tradeable range for %s", underlying)
	}

	const batchSize = 25
	const tickTimeout = 5 * time.Second

	var chain []*interfaces.OptionContract
	dte := int(time.Until(expiration).Hours() / 24)
	if dte < 0 {
		dte = 0
	}

	for batchStart := 0; batchStart < len(matchedStrikes); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(matchedStrikes) {
			batchEnd = len(matchedStrikes)
		}
		batchStrikes := matchedStrikes[batchStart:batchEnd]

		batchContracts, err := s.fetchOptionBatch(ctx, underlying, underContract, expirationStr, matchedTradingClass, matchedMultiplier, matchedExchange, batchStrikes, dte, tickTimeout)
		if err != nil {
			log.Printf("[IBKR] GetOptionsChain batch %d-%d error: %v", batchStart, batchEnd, err)
			continue
		}
		chain = append(chain, batchContracts...)
	}

	return chain, nil
}

type optionTickData struct {
	mu        sync.Mutex
	bid       float64
	ask       float64
	iv        float64
	delta     float64
	gamma     float64
	vega      float64
	theta     float64
	hasBid    bool
	hasAsk    bool
	hasGreeks bool
}

func (s *IBKRTradingService) fetchOptionBatch(
	ctx context.Context,
	underlying string,
	underContract tws.Contract,
	expirationStr, tradingClass, multiplier, exchange string,
	strikes []float64,
	dte int,
	timeout time.Duration,
) ([]*interfaces.OptionContract, error) {

	type subInfo struct {
		reqId  int64
		strike float64
		right  string
		data   *optionTickData
	}

	var subs []subInfo
	collector := &optionDataCollector{
		subs: make(map[int64]*optionTickData),
	}

	s.client.AddWrapper(collector)
	defer s.client.RemoveWrapper(collector)

	for _, strike := range strikes {
		for _, right := range []string{"C", "P"} {
			contract := tws.Contract{
				Symbol:                       underContract.Symbol,
				SecType:                      tws.Option,
				Exchange:                     exchange,
				Currency:                     underContract.Currency,
				LastTradeDateOrContractMonth: expirationStr,
				Strike:                       strike,
				Right:                        right,
				Multiplier:                   multiplier,
				TradingClass:                 tradingClass,
			}

			reqId := s.nextDataReqId() // [FIX BUG #7] Usa nextDataReqId()
			td := &optionTickData{
				iv:    math.MaxFloat64,
				delta: math.MaxFloat64,
				gamma: math.MaxFloat64,
				vega:  math.MaxFloat64,
				theta: math.MaxFloat64,
			}

			collector.mu.Lock()
			collector.subs[reqId] = td
			collector.mu.Unlock()

			if err := s.client.Encoder().ReqMktData(reqId, contract, "", false, false); err != nil {
				log.Printf("[IBKR] GetOptionsChain: ReqMktData error for %s %.0f%s: %v", underlying, strike, right, err)
				continue
			}

			subs = append(subs, subInfo{reqId: reqId, strike: strike, right: right, data: td})
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}

	for _, sub := range subs {
		_ = s.client.Encoder().CancelMktData(sub.reqId)
	}

	var contracts []*interfaces.OptionContract
	for _, sub := range subs {
		sub.data.mu.Lock()
		contractType := "call"
		if sub.right == "P" {
			contractType = "put"
		}

		year, _ := strconv.Atoi(expirationStr[:4])
		month, _ := strconv.Atoi(expirationStr[4:6])
		day, _ := strconv.Atoi(expirationStr[6:8])

		oc := &interfaces.OptionContract{
			Symbol:           fmt.Sprintf("%s:%s:%s:%s", underContract.Symbol, expirationStr, sub.right, strconv.FormatFloat(sub.strike, 'f', -1, 64)),
			UnderlyingSymbol: underlying,
			ContractType:     contractType,
			StrikePrice:      sub.strike,
			ExpirationDate:   time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC),
			DTE:              dte,
			Bid:              sub.data.bid,
			Ask:              sub.data.ask,
		}

		if sub.data.iv != math.MaxFloat64 && sub.data.iv > 0 {
			oc.ImpliedVolatility = sub.data.iv
		}
		if sub.data.delta != math.MaxFloat64 {
			oc.Delta = sub.data.delta
		}
		if sub.data.gamma != math.MaxFloat64 {
			oc.Gamma = sub.data.gamma
		}
		if sub.data.theta != math.MaxFloat64 {
			oc.Theta = sub.data.theta
		}
		if sub.data.vega != math.MaxFloat64 {
			oc.Vega = sub.data.vega
		}

		if sub.data.hasBid && sub.data.hasAsk && sub.data.ask > 0 {
			oc.Premium = (sub.data.bid + sub.data.ask) / 2
		}

		sub.data.mu.Unlock()
		contracts = append(contracts, oc)
	}

	return contracts, nil
}

type optionDataCollector struct {
	tws.DefaultWrapper
	mu   sync.RWMutex
	subs map[int64]*optionTickData
}

func (c *optionDataCollector) TickPrice(reqId int64, tickType int, price float64, size decimal.Decimal, attr tws.TickAttrib) {
	c.mu.RLock()
	td, ok := c.subs[reqId]
	c.mu.RUnlock()
	if !ok || price <= 0 {
		return
	}
	td.mu.Lock()
	defer td.mu.Unlock()
	switch tickType {
	case tws.TickBidPrice, tws.TickDelayedBid:
		td.bid = price
		td.hasBid = true
	case tws.TickAskPrice, tws.TickDelayedAsk:
		td.ask = price
		td.hasAsk = true
	}
}

func (c *optionDataCollector) TickOptionComputation(reqId int64, tickType int, tickAttrib int, impliedVol, delta, optPrice, pvDividend, gamma, vega, theta, undPrice float64) {
	c.mu.RLock()
	td, ok := c.subs[reqId]
	c.mu.RUnlock()
	if !ok {
		return
	}
	td.mu.Lock()
	defer td.mu.Unlock()
	td.iv = impliedVol
	td.delta = delta
	td.gamma = gamma
	td.vega = vega
	td.theta = theta
	td.hasGreeks = true
}

func (s *IBKRTradingService) GetOptionsQuote(ctx context.Context, symbol string) (*interfaces.OptionsQuote, error) {
	return nil, fmt.Errorf("GetOptionsQuote not implemented in Phase 3")
}

func (s *IBKRTradingService) GetOptionsPosition(ctx context.Context, symbol string) (*interfaces.OptionsPosition, error) {
	return nil, fmt.Errorf("GetOptionsPosition not implemented in Phase 3")
}

func (s *IBKRTradingService) ListOptionsPositions(ctx context.Context) ([]*interfaces.OptionsPosition, error) {
	positions, err := s.GetPositions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*interfaces.OptionsPosition, 0)
	for _, p := range positions {
		c, perr := tws.ParseSymbol(p.Symbol)
		if perr != nil || c.SecType != tws.Option {
			continue
		}
		optType := "call"
		if strings.EqualFold(c.Right, "P") {
			optType = "put"
		}
		var exp time.Time
		if t, e := time.Parse("20060102", c.LastTradeDateOrContractMonth); e == nil {
			exp = t
		}
		out = append(out, &interfaces.OptionsPosition{
			Symbol:         p.Symbol,
			Underlying:     c.Symbol,
			Qty:            p.Qty,
			AvgEntryPrice:  p.AvgEntryPrice,
			MarketValue:    p.MarketValue,
			CostBasis:      p.CostBasis,
			UnrealizedPL:   p.UnrealizedPL,
			UnrealizedPLPC: p.UnrealizedPLPC,
			CurrentPrice:   p.CurrentPrice,
			Side:           p.Side,
			Expiration:     exp,
			Strike:         c.Strike,
			OptionType:     optType,
		})
	}
	return out, nil
}