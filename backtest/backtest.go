package backtest

import (
	"math"
	"time"

	"prophet-trader/interfaces"
)

// Signal is one entry proposal generated from historical bars.
type Signal struct {
	Index     int
	Direction string // long | short
	Entry     float64
	Stop      float64
	Target    float64
}

type Trade struct {
	EntryTime time.Time `json:"entry_time"`
	ExitTime  time.Time `json:"exit_time"`
	Direction string    `json:"direction"`
	Entry     float64   `json:"entry"`
	Exit      float64   `json:"exit"`
	Qty       float64   `json:"qty"`
	PnL       float64   `json:"pnl"`
	PnLPct    float64   `json:"pnl_pct"`
	Reason    string    `json:"reason"`
}

type Result struct {
	Symbol         string    `json:"symbol"`
	Strategy       string    `json:"strategy"`
	Timeframe      string    `json:"timeframe"`
	Bars           int       `json:"bars"`
	Signals        int       `json:"signals"`
	Trades         int       `json:"trades"`
	WinRate        float64   `json:"win_rate"`
	Sharpe         float64   `json:"sharpe"`
	ProfitFactor   float64   `json:"profit_factor"`
	TotalReturnPct float64   `json:"total_return_pct"`
	MaxDrawdownPct float64   `json:"max_drawdown_pct"`
	FinalCapital   float64   `json:"final_capital"`
	Equity         []float64 `json:"equity"`
	Closed         []Trade   `json:"closed"`
	Note           string    `json:"note,omitempty"`
}

type Params struct {
	Strategy       string
	Timeframe      string
	InitialCapital float64
	RiskPct        float64
	MaxPositionPct float64
	Commission     float64
	SlippagePct    float64
	TimeStopBars   int
}

func timeframeMinutes(tf string) int {
	switch tf {
	case "1Min":
		return 1
	case "5Min":
		return 5
	case "15Min":
		return 15
	case "30Min":
		return 30
	case "1Hour":
		return 60
	default:
		return 0
	}
}

func defaultTimeStop(tf string) int {
	switch tf {
	case "1Min":
		return 30
	case "5Min":
		return 6
	case "15Min":
		return 2
	case "30Min":
		return 1
	case "1Hour":
		return 1
	case "1Day":
		return 5
	default:
		return 5
	}
}

// sessionLevels returns the Initial Balance high/low for intraday bars.
func sessionLevels(bars []*interfaces.Bar, tf string) ([]float64, []float64) {
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))

	if tf == "1Day" {
		for i := 1; i < len(bars); i++ {
			highs[i] = bars[i-1].High
			lows[i] = bars[i-1].Low
		}
		return highs, lows
	}

	minutes := timeframeMinutes(tf)
	if minutes <= 0 {
		minutes = 60
	}
	barsPerHour := 60 / minutes
	if barsPerHour < 1 {
		barsPerHour = 1
	}

	type session struct {
		start, end int
		high, low  float64
	}
	sessions := make(map[string]*session)
	for i, b := range bars {
		date := b.Timestamp.Format("2006-01-02")
		s := sessions[date]
		if s == nil {
			s = &session{start: i, end: i + barsPerHour - 1, high: b.High, low: b.Low}
			sessions[date] = s
		}
		if i <= s.end {
			if b.High > s.high {
				s.high = b.High
			}
			if b.Low < s.low {
				s.low = b.Low
			}
		}
	}
	for i, b := range bars {
		s := sessions[b.Timestamp.Format("2006-01-02")]
		if s != nil && i > s.end {
			highs[i], lows[i] = s.high, s.low
		}
	}
	return highs, lows
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stddev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	sum := 0.0
	for _, v := range values {
		sum += (v - m) * (v - m)
	}
	return math.Sqrt(sum / float64(len(values)))
}

func rsiAt(bars []*interfaces.Bar, i, period int) float64 {
	if i < period {
		return 50
	}
	gain, loss := 0.0, 0.0
	for j := i - period + 1; j <= i; j++ {
		delta := bars[j].Close - bars[j-1].Close
		if delta > 0 {
			gain += delta
		} else {
			loss -= delta
		}
	}
	gain /= float64(period)
	loss /= float64(period)
	if loss == 0 {
		return 100
	}
	return 100 - (100 / (1 + gain/loss))
}

func GenerateSignals(strategy string, bars []*interfaces.Bar, tf string) []Signal {
	if len(bars) < 25 {
		return nil
	}
	switch strategy {
	case "raschke", "fit_mom":
		return raschkeSignals(bars, tf, strategy == "fit_mom")
	case "mean_reversion":
		return meanReversionSignals(bars)
	case "turtle_breakout":
		return turtleSignals(bars)
	default:
		return nil
	}
}

func raschkeSignals(bars []*interfaces.Bar, tf string, fitMom bool) []Signal {
	highs, lows := sessionLevels(bars, tf)
	out := make([]Signal, 0)
	for i := 21; i < len(bars); i++ {
		ih, il := highs[i], lows[i]
		if ih <= 0 || il <= 0 || ih <= il {
			continue
		}
		price := bars[i].Close
		mid := (ih + il) / 2
		if fitMom {
			if price > mid && price < ih {
				out = append(out, Signal{Index: i, Direction: "long", Entry: price, Stop: il, Target: ih})
			}
			continue
		}
		if price >= ih*0.999 {
			out = append(out, Signal{Index: i, Direction: "long", Entry: price, Stop: mid, Target: ih + (ih-il)*0.5})
		} else if price <= il*1.001 {
			out = append(out, Signal{Index: i, Direction: "short", Entry: price, Stop: mid, Target: il - (ih-il)*0.5})
		}
	}
	return out
}

func meanReversionSignals(bars []*interfaces.Bar) []Signal {
	out := make([]Signal, 0)
	for i := 20; i < len(bars); i++ {
		closes := make([]float64, 0, 20)
		for j := i - 19; j <= i; j++ {
			closes = append(closes, bars[j].Close)
		}
		sma := mean(closes)
		sd := stddev(closes)
		if sd <= 0 {
			continue
		}
		rsi := rsiAt(bars, i, 14)
		price := bars[i].Close
		if price <= sma-2*sd && rsi < 30 {
			out = append(out, Signal{Index: i, Direction: "long", Entry: price, Stop: price - sd, Target: sma})
		} else if price >= sma+2*sd && rsi > 70 {
			out = append(out, Signal{Index: i, Direction: "short", Entry: price, Stop: price + sd, Target: sma})
		}
	}
	return out
}

func turtleSignals(bars []*interfaces.Bar) []Signal {
	out := make([]Signal, 0)
	for i := 30; i < len(bars); i++ {
		highest := 0.0
		for j := i - 20; j < i; j++ {
			if bars[j].High > highest {
				highest = bars[j].High
			}
		}
		if highest <= 0 || bars[i].Close <= highest {
			continue
		}
		trSum := 0.0
		for j := i - 13; j <= i; j++ {
			prev := bars[j-1].Close
			tr := math.Max(bars[j].High-bars[j].Low, math.Max(math.Abs(bars[j].High-prev), math.Abs(bars[j].Low-prev)))
			trSum += tr
		}
		atr := trSum / 14
		if atr <= 0 {
			continue
		}
		price := bars[i].Close
		out = append(out, Signal{Index: i, Direction: "long", Entry: price, Stop: price - 2*atr, Target: price + 2*atr})
	}
	return out
}

func maxDrawdown(equity []float64) float64 {
	peak, worst := 0.0, 0.0
	for _, v := range equity {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			dd := (peak - v) / peak * 100
			if dd > worst {
				worst = dd
			}
		}
	}
	return worst
}


// Run simulates the strategy on historical bars with costs and a bar-based time stop.
func Run(symbol string, bars []*interfaces.Bar, p Params) Result {
	res := Result{Symbol: symbol, Strategy: p.Strategy, Timeframe: p.Timeframe, Bars: len(bars), Equity: []float64{}, Closed: []Trade{}}
	if len(bars) < 25 {
		res.Note = "not enough bars (need at least 25)"
		return res
	}

	capital := p.InitialCapital
	if capital <= 0 {
		capital = 10000
	}
	initial := capital
	riskPct := p.RiskPct
	if riskPct <= 0 {
		riskPct = 1
	}
	maxPosPct := p.MaxPositionPct
	if maxPosPct <= 0 {
		maxPosPct = 20
	}
	commission := p.Commission
	if commission <= 0 {
		commission = 0.005
	}
	slipPct := p.SlippagePct
	if slipPct <= 0 {
		slipPct = 0.01
	}
	timeStop := p.TimeStopBars
	if timeStop <= 0 {
		timeStop = defaultTimeStop(p.Timeframe)
	}

	signals := GenerateSignals(p.Strategy, bars, p.Timeframe)
	res.Signals = len(signals)
	res.Equity = append(res.Equity, capital)

	for _, sig := range signals {
		if sig.Index+1 >= len(bars) || sig.Stop <= 0 || sig.Target <= 0 {
			continue
		}
		entry := sig.Entry
		if sig.Direction == "long" {
			entry *= 1 + slipPct/100
		} else {
			entry *= 1 - slipPct/100
		}
		stopDist := math.Abs(entry - sig.Stop)
		if stopDist <= 0 {
			continue
		}
		qty := math.Floor(math.Min(capital*riskPct/100/stopDist, capital*maxPosPct/100/entry))
		if qty < 1 {
			continue
		}

		exitPrice := 0.0
		exitIdx := sig.Index + timeStop
		reason := "TIME"
		for j := sig.Index + 1; j < len(bars) && j <= sig.Index+timeStop; j++ {
			if sig.Direction == "long" {
				if bars[j].Low <= sig.Stop {
					exitPrice, exitIdx, reason = sig.Stop, j, "SL"
					break
				}
				if bars[j].High >= sig.Target {
					exitPrice, exitIdx, reason = sig.Target, j, "TP"
					break
				}
			} else {
				if bars[j].High >= sig.Stop {
					exitPrice, exitIdx, reason = sig.Stop, j, "SL"
					break
				}
				if bars[j].Low <= sig.Target {
					exitPrice, exitIdx, reason = sig.Target, j, "TP"
					break
				}
			}
		}
		if exitPrice == 0 {
			if exitIdx >= len(bars) {
				exitIdx = len(bars) - 1
			}
			exitPrice = bars[exitIdx].Close
		} else if sig.Direction == "long" {
			exitPrice *= 1 - slipPct/100
		} else {
			exitPrice *= 1 + slipPct/100
		}

		gross := (exitPrice - entry) * qty
		if sig.Direction == "short" {
			gross = (entry - exitPrice) * qty
		}
		pnl := gross - 2*commission*qty
		capital += pnl
		res.Equity = append(res.Equity, capital)
		res.Closed = append(res.Closed, Trade{
			EntryTime: bars[sig.Index].Timestamp,
			ExitTime:  bars[exitIdx].Timestamp,
			Direction: sig.Direction,
			Entry:     entry,
			Exit:      exitPrice,
			Qty:       qty,
			PnL:       pnl,
			PnLPct:    pnl / (entry * qty) * 100,
			Reason:    reason,
		})
	}

	res.Trades = len(res.Closed)
	res.FinalCapital = capital
	res.TotalReturnPct = (capital - initial) / initial * 100
	res.MaxDrawdownPct = maxDrawdown(res.Equity)

	if res.Trades > 0 {
		wins, grossWin, grossLoss := 0, 0.0, 0.0
		returns := make([]float64, 0, res.Trades)
		for _, t := range res.Closed {
			returns = append(returns, t.PnLPct)
			if t.PnL > 0 {
				wins++
				grossWin += t.PnL
			} else {
				grossLoss -= t.PnL
			}
		}
		res.WinRate = float64(wins) / float64(res.Trades) * 100
		if grossLoss > 0 {
			res.ProfitFactor = grossWin / grossLoss
		} else if grossWin > 0 {
			res.ProfitFactor = 999
		}
		if len(returns) > 1 {
			sd := stddev(returns)
			if sd > 0 {
				res.Sharpe = mean(returns) / sd * math.Sqrt(float64(len(returns)))
			}
		}
	}

	return res
}

