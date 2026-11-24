package services

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

func memoryTokens(parts ...string) map[string]bool {
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "this": true,
		"that": true, "from": true, "was": true, "were": true, "are": true,
		"por": true, "con": true, "para": true, "una": true, "uno": true,
		"los": true, "las": true, "del": true, "que": true, "como": true,
	}
	out := map[string]bool{}
	for _, p := range parts {
		for _, w := range strings.FieldsFunc(strings.ToLower(p), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if len(w) >= 3 && !stop[w] {
				out[w] = true
			}
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union <= 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func numericCloseness(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale <= 0 {
		return 0
	}
	diff := math.Abs(a-b) / scale
	if diff >= 1 {
		return 0
	}
	return 1 - diff
}

// GetSimilarTrades ranks past trades by similarity to the current context.
// It is a local, dependency-free hybrid: token overlap + symbol + RSI/ATR.
func GetSimilarTrades(q TradeQuery, limit int) []ScoredTrade {
	if limit <= 0 {
		limit = 5
	}

	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	store, err := loadMemoryFile()
	if err != nil {
		return nil
	}

	queryTokens := memoryTokens(q.Symbol, q.Setup, q.Context)
	hasQuery := len(queryTokens) > 0 || q.Symbol != "" || q.RSI > 0 || q.ATRPercent > 0

	scored := make([]ScoredTrade, 0, len(store.Trades))
	for _, t := range store.Trades {
		tokens := memoryTokens(t.Symbol, t.Setup, t.MarketContext, t.Regime)
		j := jaccard(queryTokens, tokens)

		sameSymbol := 0.0
		if q.Symbol != "" && strings.EqualFold(q.Symbol, t.Symbol) {
			sameSymbol = 1
		}

		score := 0.45*j + 0.25*sameSymbol + 0.15*numericCloseness(q.RSI, t.RSI) + 0.15*numericCloseness(q.ATRPercent, t.ATRPercent)
		if !hasQuery {
			score = 0
		}
		scored = append(scored, ScoredTrade{TradeMemory: t, Score: score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].Timestamp.After(scored[j].Timestamp)
		}
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}
