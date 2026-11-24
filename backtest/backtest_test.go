package backtest

import (
	"testing"
	"time"

	"prophet-trader/interfaces"
)

func TestMaxDrawdown(t *testing.T) {
	if got, want := maxDrawdown([]float64{100, 120, 90, 110}), 25.0; got != want {
		t.Fatalf("maxDrawdown = %.2f, want %.2f", got, want)
	}
}

func syntheticBars(n int, start float64) []*interfaces.Bar {
	base := time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC)
	bars := make([]*interfaces.Bar, n)
	for i := 0; i < n; i++ {
		price := start + float64(i)
		bars[i] = &interfaces.Bar{
			Timestamp: base.AddDate(0, 0, i),
			Open:      price,
			High:      price + 2,
			Low:       price - 2,
			Close:     price,
			Volume:    1000,
		}
	}
	return bars
}

func TestRunReturnsSaneResult(t *testing.T) {
	bars := syntheticBars(60, 100)
	res := Run("QQQ", bars, Params{Strategy: "raschke", Timeframe: "1Day"})
	if res.Bars != 60 {
		t.Fatalf("bars = %d, want 60", res.Bars)
	}
	if len(res.Equity) == 0 {
		t.Fatal("equity curve must not be empty")
	}
	if res.FinalCapital <= 0 {
		t.Fatalf("final capital = %.2f", res.FinalCapital)
	}
}

func TestMeanReversionSignalOnOversoldDrop(t *testing.T) {
	bars := make([]*interfaces.Bar, 30)
	base := time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC)
	for i := 0; i < 29; i++ {
		price := 100.0
		if i%2 == 1 {
			price = 101
		}
		bars[i] = &interfaces.Bar{Timestamp: base.AddDate(0, 0, i), Open: price, High: price + 0.5, Low: price - 0.5, Close: price}
	}
	bars[29] = &interfaces.Bar{Timestamp: base.AddDate(0, 0, 29), Open: 100, High: 100.5, Low: 89.9, Close: 90}

	signals := GenerateSignals("mean_reversion", bars, "1Day")
	if len(signals) == 0 {
		t.Fatal("expected an oversold long signal")
	}
	if signals[0].Direction != "long" || signals[0].Stop >= signals[0].Entry || signals[0].Target <= signals[0].Entry {
		t.Fatalf("unexpected signal: %+v", signals[0])
	}
}
