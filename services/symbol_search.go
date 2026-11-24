package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"prophet-trader/configstore"
)

// SymbolSearchResult is one row in the company/ticker search dropdown.
type SymbolSearchResult struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Exchange string `json:"exchange,omitempty"`
	Source   string `json:"source"`
}

type symbolCacheEntry struct {
	at      time.Time
	results []SymbolSearchResult
}

// SymbolSearcher combines the local symbol catalog with Yahoo Finance search.
// Yahoo is a best-effort fallback; local results always work offline.
type SymbolSearcher struct {
	client   *http.Client
	endpoint string
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]symbolCacheEntry
}

func NewSymbolSearcher() *SymbolSearcher {
	return &SymbolSearcher{
		client:   &http.Client{Timeout: 5 * time.Second},
		endpoint: "https://query1.finance.yahoo.com/v1/finance/search?q=",
		ttl:      30 * time.Minute,
		cache:    make(map[string]symbolCacheEntry),
	}
}

func (s *SymbolSearcher) Search(ctx context.Context, query string) []SymbolSearchResult {
	q := strings.TrimSpace(query)
	if len(q) < 2 {
		return nil
	}
	key := strings.ToLower(q)
	s.mu.Lock()
	if entry, ok := s.cache[key]; ok && time.Since(entry.at) < s.ttl {
		results := append([]SymbolSearchResult(nil), entry.results...)
		s.mu.Unlock()
		return results
	}
	s.mu.Unlock()

	results := localSymbolResults(q)
	if len(results) < 10 {
		results = mergeSymbolResults(results, s.searchYahoo(ctx, q))
	}
	if len(results) > 12 {
		results = results[:12]
	}
	s.mu.Lock()
	s.cache[key] = symbolCacheEntry{at: time.Now(), results: append([]SymbolSearchResult(nil), results...)}
	s.mu.Unlock()
	return results
}

func localSymbolResults(query string) []SymbolSearchResult {
	catalog := configstore.GetSymbolCatalog()
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || len(catalog) == 0 {
		return nil
	}
	type scored struct {
		result SymbolSearchResult
		score  int
	}
	matches := make([]scored, 0)
	for sym, meta := range catalog {
		upperSym := strings.ToUpper(sym)
		name := meta.Name
		if name == "" {
			name = upperSym
		}
		lowerSym := strings.ToLower(upperSym)
		lowerName := strings.ToLower(name)
		score := -1
		switch {
		case lowerSym == q:
			score = 0
		case strings.HasPrefix(lowerSym, q):
			score = 1
		case strings.Contains(lowerName, q):
			score = 2
		case strings.Contains(lowerSym, q):
			score = 3
		}
		if score >= 0 {
			matches = append(matches, scored{
				result: SymbolSearchResult{
					Symbol:   upperSym,
					Name:     name,
					Type:     meta.Type,
					Exchange: meta.Exchange,
					Source:   "local",
				},
				score: score,
			})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].result.Symbol < matches[j].result.Symbol
	})
	out := make([]SymbolSearchResult, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.result)
	}
	return out
}

func mergeSymbolResults(local, remote []SymbolSearchResult) []SymbolSearchResult {
	seen := make(map[string]bool, len(local)+len(remote))
	out := make([]SymbolSearchResult, 0, len(local)+len(remote))
	for _, list := range [][]SymbolSearchResult{local, remote} {
		for _, item := range list {
			sym := strings.ToUpper(strings.TrimSpace(item.Symbol))
			if sym == "" || seen[sym] {
				continue
			}
			item.Symbol = sym
			if item.Name == "" {
				item.Name = sym
			}
			seen[sym] = true
			out = append(out, item)
		}
	}
	return out
}

func (s *SymbolSearcher) searchYahoo(ctx context.Context, query string) []SymbolSearchResult {
	if s.client == nil {
		return nil
	}
	searchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(searchCtx, http.MethodGet, s.endpoint+url.QueryEscape(query)+"&quotesCount=12&newsCount=0&listsCount=0", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "OpenProphet/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Quotes []struct {
			Symbol    string `json:"symbol"`
			Shortname string `json:"shortname"`
			Longname  string `json:"longname"`
			QuoteType string `json:"quoteType"`
			Exchange  string `json:"exchange"`
			ExchDisp  string `json:"exchDisp"`
		} `json:"quotes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}
	out := make([]SymbolSearchResult, 0, len(payload.Quotes))
	for _, quote := range payload.Quotes {
		sym := strings.ToUpper(strings.TrimSpace(quote.Symbol))
		if sym == "" {
			continue
		}
		name := strings.TrimSpace(quote.Longname)
		if name == "" {
			name = strings.TrimSpace(quote.Shortname)
		}
		if name == "" {
			name = sym
		}
		typ := strings.ToLower(strings.TrimSpace(quote.QuoteType))
		switch typ {
		case "equity":
			typ = "Stock"
		case "etf":
			typ = "ETF"
		case "future":
			typ = "Future"
		case "index":
			typ = "Index"
		case "mutualfund":
			typ = "Fund"
		case "":
			typ = "Unknown"
		}
		exchange := strings.TrimSpace(quote.ExchDisp)
		if exchange == "" {
			exchange = strings.TrimSpace(quote.Exchange)
		}
		out = append(out, SymbolSearchResult{
			Symbol:   sym,
			Name:     name,
			Type:     typ,
			Exchange: exchange,
			Source:   "yahoo",
		})
	}
	return out
}
