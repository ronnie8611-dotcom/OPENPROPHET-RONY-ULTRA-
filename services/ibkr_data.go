package services

import (
	"context"
	"fmt"
	"prophet-trader/interfaces"
	"prophet-trader/tws"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// TickNewsMsg es nuestra propia estructura para manejar las noticias del tick 292,
// ya que la librería tws no la incluye por defecto.
type TickNewsMsg struct {
	Timestamp    int64
	ProviderCode string
	ArticleID    string
	Headline     string
}

type IBKRDataService struct {
	tws.DefaultWrapper
	client   *tws.Client
	resolver *tws.ContractResolver
	mu       sync.RWMutex
	streams  map[int64]chan any
	histBuf  map[int64][]*interfaces.Bar
	
	// [QA] Mutex exclusivo para generar reqIds únicos y evitar colisiones entre hilos
	reqIdMu  sync.Mutex
	
	// Canal dedicado para noticias
	newsChan chan TickNewsMsg 
}

var _ interfaces.DataService = (*IBKRDataService)(nil)

func NewIBKRDataService(client *tws.Client, resolver *tws.ContractResolver) *IBKRDataService {
	s := &IBKRDataService{
		client:   client,
		resolver: resolver,
		streams:  make(map[int64]chan any),
		histBuf:  make(map[int64][]*interfaces.Bar),
		newsChan: make(chan TickNewsMsg, 100),
	}
	client.AddWrapper(s)
	_ = client.Encoder().ReqMarketDataType(4)
	return s
}

// [QA] Generador de reqId único y seguro para hilos
func (s *IBKRDataService) nextReqId() int64 {
	s.reqIdMu.Lock()
	defer s.reqIdMu.Unlock()
	return s.client.NextOrderId()
}

// NewsStream devuelve el canal de noticias para que main.go lo escuche
func (s *IBKRDataService) NewsStream() <-chan TickNewsMsg {
	return s.newsChan
}

func (s *IBKRDataService) SubscribeNews(reqId int64, provider string) error {
	contract := tws.Contract{
		Symbol:  provider,
		SecType: "NEWS",
	}
	genericTickList := "mdoff,292"
	return s.client.Encoder().ReqMktData(reqId, contract, genericTickList, false, false)
}

func (s *IBKRDataService) CancelNews(reqId int64) {
	s.client.Encoder().CancelMktData(reqId)
}

// TickNews es la función que la librería TWS llamará automáticamente cuando llegue
// un mensaje de noticias. Sobrescribimos el método del DefaultWrapper.
func (s *IBKRDataService) TickNews(reqId int64, timeStamp int64, providerCode string, articleID string, headline string, extraData string) {
	select {
	case s.newsChan <- TickNewsMsg{Timestamp: timeStamp, ProviderCode: providerCode, ArticleID: articleID, Headline: headline}:
	default:
	}
}

// [PARCHE] resolveContract ahora detecta futuros y con_id inyectando exchange CME
func (s *IBKRDataService) resolveContract(ctx context.Context, symbol string) (tws.Contract, error) {
	// 1. Si ya es un con_id numérico, asumimos que es un futuro y le inyectamos el exchange
	if conId, err := strconv.ParseInt(symbol, 10, 64); err == nil && conId > 0 {
		return tws.Contract{
			ConId:    conId,
			SecType:  tws.Future,
			Exchange: "CME",
		}, nil
	}

	// 2. Detección automática de Futuros por ticker (MNQ, NQ, ES, etc.)
	upperSym := strings.ToUpper(symbol)
	isFuture := strings.HasPrefix(upperSym, "MNQ") || strings.HasPrefix(upperSym, "NQ") || 
	            strings.HasPrefix(upperSym, "ES") || strings.HasPrefix(upperSym, "MES") || 
	            strings.HasPrefix(upperSym, "YM") || strings.HasPrefix(upperSym, "RTY")

	if isFuture {
		// IBKR requiere que le digamos el exchange y el tipo exacto para futuros
		return tws.Contract{
			Symbol:   upperSym,
			SecType:  tws.Future,
			Exchange: "CME",
			Currency: "USD",
		}, nil
	}

	// 3. Fallback para acciones y ETFs normales
	return s.resolver.Resolve(ctx, symbol)
}

func (s *IBKRDataService) OnDisconnect() {
	s.mu.Lock()
	for id, ch := range s.streams {
		close(ch)
		delete(s.streams, id)
	}
	for id := range s.histBuf {
		delete(s.histBuf, id)
	}
	s.mu.Unlock()
}

func (s *IBKRDataService) OnReconnect() {
	_ = s.client.Encoder().ReqMarketDataType(4)
}

func (s *IBKRDataService) TickPrice(reqId int64, tickType int, price float64, size decimal.Decimal, attr tws.TickAttrib) {
	s.mu.RLock()
	ch, ok := s.streams[reqId]
	s.mu.RUnlock()
	if ok {
		select {
		case ch <- tws.TickPriceMsg{TickType: tickType, Price: price, Size: size, Attr: attr}:
		default:
		}
	}
}

func (s *IBKRDataService) TickSize(reqId int64, tickType int, size decimal.Decimal) {
	s.mu.RLock()
	ch, ok := s.streams[reqId]
	s.mu.RUnlock()
	if ok {
		select {
		case ch <- tws.TickSizeMsg{TickType: tickType, Size: size}:
		default:
		}
	}
}

func (s *IBKRDataService) MarketDataType(reqId int64, marketDataType int) {
	s.mu.RLock()
	ch, ok := s.streams[reqId]
	s.mu.RUnlock()
	if ok {
		select {
		case ch <- tws.MarketDataTypeMsg{Type: marketDataType}:
		default:
		}
	}
}

func (s *IBKRDataService) TickOptionComputation(reqId int64, tickType int, tickAttrib int, impliedVol, delta, optPrice, pvDividend, gamma, vega, theta, undPrice float64) {
	s.mu.RLock()
	ch, ok := s.streams[reqId]
	s.mu.RUnlock()
	if ok {
		select {
		case ch <- tws.TickOptionComputationMsg{
			TickType: tickType, TickAttrib: tickAttrib,
			ImpliedVol: impliedVol, Delta: delta, OptPrice: optPrice,
			PvDividend: pvDividend, Gamma: gamma, Vega: vega,
			Theta: theta, UndPrice: undPrice,
		}:
		default:
		}
	}
}

func (s *IBKRDataService) HistoricalData(reqId int64, bar tws.HistoricalBar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.streams[reqId]; ok {
		s.histBuf[reqId] = append(s.histBuf[reqId], &interfaces.Bar{
			Timestamp: tws.ParseTWSDate(bar.Date),
			Open:      bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close,
			Volume:    bar.Volume.IntPart(), VWAP: bar.WAP.InexactFloat64(),
		})
	}
}

func (s *IBKRDataService) HistoricalDataEnd(reqId int64, startDateStr, endDateStr string) {
	s.mu.Lock()
	ch, ok := s.streams[reqId]
	bars := s.histBuf[reqId]
	delete(s.histBuf, reqId)
	s.mu.Unlock()

	if ok {
		ch <- tws.HistoricalDataEndMsg{ReqId: reqId, Start: startDateStr, End: endDateStr, ExtData: bars}
	}
}

func (s *IBKRDataService) Error(reqId int, code int, msg string) {
	if code == 10089 || code == 10167 || code == 10168 || code == 2104 || code == 2106 || code == 2158 {
		return
	}
	s.mu.RLock()
	ch, ok := s.streams[int64(reqId)]
	s.mu.RUnlock()
	if ok {
		select {
		case ch <- fmt.Errorf("tws error: %d %s", code, msg):
		default:
		}
	}
}

func (s *IBKRDataService) subscribe(reqId int64) chan any {
	ch := make(chan any, 1024)
	s.mu.Lock()
	s.streams[reqId] = ch
	s.mu.Unlock()
	return ch
}

func (s *IBKRDataService) unsubscribe(reqId int64) {
	s.mu.Lock()
	if ch, ok := s.streams[reqId]; ok {
		close(ch)
		delete(s.streams, reqId)
	}
	delete(s.histBuf, reqId)
	s.mu.Unlock()
}

func mapTimeframeToIBKR(tf string) string {
	switch tf {
	case "1Min": return "1 min"
	case "5Min": return "5 mins"
	case "15Min": return "15 mins"
	case "30Min": return "30 mins"
	case "1Hour": return "1 hour"
	case "4Hour": return "4 hours"
	case "1Day": return "1 day"
	default: return "1 hour"
	}
}

func calculateDuration(start, end time.Time, barSize string) string {
	diff := end.Sub(start)
	if diff <= 0 { return "1 D" }
	intraday := strings.HasSuffix(barSize, "min") || strings.HasSuffix(barSize, "mins") || strings.HasSuffix(barSize, "hour") || strings.HasSuffix(barSize, "hours")
	if intraday && diff < 24*time.Hour {
		secs := int(diff.Seconds())
		if secs < 60 { secs = 60 }
		return fmt.Sprintf("%d S", secs)
	}
	days := int(diff.Hours() / 24)
	if days < 1 { days = 1 }
	if strings.HasSuffix(barSize, "min") || strings.HasSuffix(barSize, "mins") {
		cap := 5
		if barSize == "1 min" || barSize == "2 mins" { cap = 2 }
		if days > cap { days = cap }
		return fmt.Sprintf("%d D", days)
	}
	if days > 365 {
		years := days / 365
		if days%365 > 0 { years++ }
		return fmt.Sprintf("%d Y", years)
	}
	return fmt.Sprintf("%d D", days)
}

func getWhatToShow(secType tws.InstrumentType) string {
	switch secType {
	case tws.Option: return "MIDPOINT"
	default: return "TRADES"
	}
}

func (s *IBKRDataService) GetHistoricalBars(ctx context.Context, symbol string, start, end time.Time, timeframe string) ([]*interfaces.Bar, error) {
	contract, err := s.resolveContract(ctx, symbol)
	if err != nil { return nil, fmt.Errorf("GetHistoricalBars: %w", err) }
	
	reqId := s.nextReqId() // [QA] Usar el generador seguro
	ch := s.subscribe(reqId)
	defer s.unsubscribe(reqId)
	
	barSize := mapTimeframeToIBKR(timeframe)
	duration := calculateDuration(start, end, barSize)
	whatToShow := getWhatToShow(contract.SecType)
	endDateTime := end.UTC().Format("20060102-15:04:05")
	err = s.client.Encoder().ReqHistoricalData(s.client.ServerVersion(), reqId, contract, endDateTime, duration, barSize, whatToShow, 1, 2, false)
	if err != nil { return nil, fmt.Errorf("ReqHistoricalData error: %w", err) }
	
	for {
		select {
		case msg := <-ch:
			switch t := msg.(type) {
			case tws.HistoricalDataEndMsg:
				bars, _ := t.ExtData.([]*interfaces.Bar)
				for _, b := range bars { b.Symbol = symbol }
				return bars, nil
			case error:
				return nil, fmt.Errorf("tws error: %w", t)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *IBKRDataService) GetLatestBar(ctx context.Context, symbol string) (*interfaces.Bar, error) {
	bars, err := s.GetHistoricalBars(ctx, symbol, time.Now().Add(-5*24*time.Hour), time.Now(), "5Min")
	if err != nil { return nil, err }
	if len(bars) == 0 { return nil, fmt.Errorf("no bars found") }
	return bars[len(bars)-1], nil
}

func (s *IBKRDataService) StreamBars(ctx context.Context, symbols []string) (<-chan *interfaces.Bar, error) {
	return nil, fmt.Errorf("StreamBars not implemented in Phase 4")
}

func (s *IBKRDataService) GetLatestQuote(ctx context.Context, symbol string) (*interfaces.Quote, error) {
	contract, err := s.resolveContract(ctx, symbol)
	if err != nil { return nil, fmt.Errorf("GetLatestQuote: %w", err) }
	isIndex := contract.SecType == tws.Index
	
	reqId := s.nextReqId() // [QA] Usar el generador seguro
	ch := s.subscribe(reqId)
	defer s.unsubscribe(reqId)
	
	if err := s.client.Encoder().ReqMktData(reqId, contract, "", false, false); err != nil {
		return nil, fmt.Errorf("ReqMktData error: %w", err)
	}
	defer s.client.Encoder().CancelMktData(reqId)
	
	quote := &interfaces.Quote{ Symbol: symbol, Timestamp: time.Now() }
	var hasBid, hasAsk, hasLast bool
	deadline := time.After(15 * time.Second)
	
	for {
		if isIndex && hasLast { return quote, nil }
		if !isIndex && hasBid && hasAsk { return quote, nil }
		select {
		case msg := <-ch:
			switch t := msg.(type) {
			case tws.TickPriceMsg:
				switch t.TickType {
				case tws.TickBidPrice, tws.TickDelayedBid:
					quote.BidPrice = t.Price
					if !t.Size.IsZero() { quote.BidSize = t.Size.IntPart() }
					hasBid = true
				case tws.TickAskPrice, tws.TickDelayedAsk:
					quote.AskPrice = t.Price
					if !t.Size.IsZero() { quote.AskSize = t.Size.IntPart() }
					hasAsk = true
				case tws.TickLastPrice, tws.TickDelayedLast:
					if isIndex && t.Price > 0 {
						quote.BidPrice = t.Price
						quote.AskPrice = t.Price
						hasLast = true
					}
				case tws.TickClose, tws.TickDelayedClose:
					if isIndex && !hasLast && t.Price > 0 {
						quote.BidPrice = t.Price
						quote.AskPrice = t.Price
						hasLast = true
					}
				}
			case tws.TickSizeMsg:
				switch t.TickType {
				case tws.TickBidSize, tws.TickDelayedBidSize:
					quote.BidSize = t.Size.IntPart()
				case tws.TickAskSize, tws.TickDelayedAskSize:
					quote.AskSize = t.Size.IntPart()
				}
			case tws.MarketDataTypeMsg:
				quote.MarketDataType = t.Type
			case error:
				return nil, fmt.Errorf("tws error: %w", t)
			}
		case <-deadline:
			if quote.BidPrice > 0 || quote.AskPrice > 0 { return quote, nil }
			return nil, fmt.Errorf("quote timeout: no ticks received within 15s for %s", symbol)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *IBKRDataService) GetLatestTrade(ctx context.Context, symbol string) (*interfaces.Trade, error) {
	contract, err := s.resolveContract(ctx, symbol)
	if err != nil { return nil, fmt.Errorf("GetLatestTrade: %w", err) }
	
	reqId := s.nextReqId() // [QA] Usar el generador seguro
	ch := s.subscribe(reqId)
	defer s.unsubscribe(reqId)
	
	if err := s.client.Encoder().ReqMktData(reqId, contract, "", false, false); err != nil {
		return nil, fmt.Errorf("ReqMktData error: %w", err)
	}
	defer s.client.Encoder().CancelMktData(reqId)
	
	trade := &interfaces.Trade{ Symbol: symbol, Timestamp: time.Now() }
	var hasPrice, hasSize bool
	// [QA] Añadido timeout para evitar bloqueos indefinidos
	deadline := time.After(15 * time.Second)
	
	for {
		if hasPrice && hasSize { return trade, nil }
		select {
		case msg := <-ch:
			switch t := msg.(type) {
			case tws.TickPriceMsg:
				switch t.TickType {
				case tws.TickLastPrice, tws.TickDelayedLast:
					trade.Price = t.Price
					if !t.Size.IsZero() { trade.Size = t.Size.IntPart(); hasSize = true }
					hasPrice = true
				}
			case tws.TickSizeMsg:
				switch t.TickType {
				case tws.TickLastSize, tws.TickDelayedLastSize:
					trade.Size = t.Size.IntPart()
					hasSize = true
				}
			case error:
				return nil, fmt.Errorf("tws error: %w", t)
			}
		case <-deadline:
			if hasPrice { return trade, nil }
			return nil, fmt.Errorf("trade timeout: no ticks received within 15s for %s", symbol)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}