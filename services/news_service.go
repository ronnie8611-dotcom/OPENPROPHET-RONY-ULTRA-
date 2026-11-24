package services

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// NewsItem represents a single news article from the RSS feed
type NewsItem struct {
	Title       string    `xml:"title" json:"title"`
	Link        string    `xml:"link" json:"link"`
	Description string    `xml:"description" json:"description,omitempty"`
	PubDate     string    `xml:"pubDate" json:"pub_date,omitempty"`
	Source      string    `xml:"source" json:"source,omitempty"`
	GUID        string    `xml:"guid" json:"guid,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
}

// NewsItemCompact represents a compact news article with only essential fields
type NewsItemCompact struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Source string `json:"source,omitempty"`
}

// ToCompact converts a NewsItem to a compact version
func (n *NewsItem) ToCompact() NewsItemCompact {
	return NewsItemCompact{
		Title:  n.Title,
		Link:   n.Link,
		Source: n.Source,
	}
}

// NewsChannel represents the RSS channel
type NewsChannel struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	Description string      `xml:"description"`
	Items       []NewsItem  `xml:"item"`
}

// RSSFeed represents the root RSS structure
type RSSFeed struct {
	XMLName xml.Name    `xml:"rss"`
	Channel NewsChannel `xml:"channel"`
}

// NewsService handles fetching news from various sources
type NewsService struct {
	httpClient *http.Client
}

// NewNewsService creates a new news service
func NewNewsService() *NewsService {
	return &NewsService{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetGoogleNews fetches the latest news from Google News RSS feed
func (ns *NewsService) GetGoogleNews() ([]NewsItem, error) {
	url := "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en"
	return ns.fetchRSSFeed(url)
}

// GetGoogleNewsByTopic fetches news for a specific topic
func (ns *NewsService) GetGoogleNewsByTopic(topic string) ([]NewsItem, error) {
	url := "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNRGx6TVdZU0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en"

	topicURLs := map[string]string{
		"WORLD":         "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNRGx1YlY4U0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en",
		"NATION":        "https://news.google.com/rss/topics/CAAqIQgKIhtDQkFTRGdvSUwyMHZNRGxqTjNjd0VnSmxiaWdBUAE?hl=en-US&gl=US&ceid=US:en",
		"BUSINESS":      "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNRGx6TVdZU0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en",
		"TECHNOLOGY":    "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNRGRqTVhZU0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en",
		"ENTERTAINMENT": "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNREpxYW5RU0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en",
		"SPORTS":        "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNRFp1ZEdvU0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en",
		"SCIENCE":       "https://news.google.com/rss/topics/CAAqJggKIiBDQkFTRWdvSUwyMHZNRFp0Y1RjU0FtVnVHZ0pWVXlnQVAB?hl=en-US&gl=US&ceid=US:en",
		"HEALTH":        "https://news.google.com/rss/topics/CAAqIQgKIhtDQkFTRGdvSUwyMHZNR3QwTlRFd0VnSmxiaWdBUAE?hl=en-US&gl=US&ceid=US:en",
	}

	if topicURL, ok := topicURLs[topic]; ok {
		url = topicURL
	}

	return ns.fetchRSSFeed(url)
}

// GetGoogleNewsSearch fetches news for a specific search query
func (ns *NewsService) GetGoogleNewsSearch(query string) ([]NewsItem, error) {
	encodedQuery := url.QueryEscape(query)
	urlString := fmt.Sprintf("https://news.google.com/rss/search?q=%s&hl=en-US&gl=US&ceid=US:en", encodedQuery)
	return ns.fetchRSSFeed(urlString)
}

// --- MARKETWATCH FEEDS ---
func (ns *NewsService) GetMarketWatchTopStories() ([]NewsItem, error) {
	return ns.fetchRSSFeed("https://feeds.content.dowjones.io/public/rss/mw_topstories")
}

func (ns *NewsService) GetMarketWatchRealtimeHeadlines() ([]NewsItem, error) {
	return ns.fetchRSSFeed("https://feeds.content.dowjones.io/public/rss/mw_realtimeheadlines")
}

func (ns *NewsService) GetMarketWatchBulletins() ([]NewsItem, error) {
	return ns.fetchRSSFeed("https://feeds.content.dowjones.io/public/rss/mw_bulletins")
}

func (ns *NewsService) GetMarketWatchMarketPulse() ([]NewsItem, error) {
	return ns.fetchRSSFeed("https://feeds.content.dowjones.io/public/rss/mw_marketpulse")
}

func (ns *NewsService) GetAllMarketWatchNews() ([]NewsItem, error) {
	allNews := make([]NewsItem, 0)
	feeds := []func() ([]NewsItem, error){
		ns.GetMarketWatchTopStories,
		ns.GetMarketWatchRealtimeHeadlines,
		ns.GetMarketWatchBulletins,
		ns.GetMarketWatchMarketPulse,
	}
	for _, fetchFunc := range feeds {
		items, err := fetchFunc()
		if err == nil {
			allNews = append(allNews, items...)
		}
	}
	return allNews, nil
}

// --- YAHOO FINANCE & YAHOO NEWS FEEDS ---
func (ns *NewsService) GetYahooFinanceNews() ([]NewsItem, error) {
	return ns.fetchRSSFeed("https://finance.yahoo.com/news/rssindex")
}

func (ns *NewsService) GetYahooNews() ([]NewsItem, error) {
	return ns.fetchRSSFeed("https://news.yahoo.com/rss/")
}

func (ns *NewsService) GetAllYahooNews() ([]NewsItem, error) {
	allNews := make([]NewsItem, 0)
	
	yfItems, err := ns.GetYahooFinanceNews()
	if err == nil {
		allNews = append(allNews, yfItems...)
	}
	
	ynItems, err := ns.GetYahooNews()
	if err == nil {
		allNews = append(allNews, ynItems...)
	}
	
	return allNews, nil
}

// fetchRSSFeed is a helper method to fetch and parse any RSS feed
func (ns *NewsService) fetchRSSFeed(url string) ([]NewsItem, error) {
	resp, err := ns.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch RSS feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var feed RSSFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse RSS feed: %w", err)
	}

	for i := range feed.Channel.Items {
		if feed.Channel.Items[i].PubDate != "" {
			if t, err := time.Parse(time.RFC1123, feed.Channel.Items[i].PubDate); err == nil {
				feed.Channel.Items[i].PublishedAt = t
			} else if t, err := time.Parse(time.RFC1123Z, feed.Channel.Items[i].PubDate); err == nil {
				feed.Channel.Items[i].PublishedAt = t
			}
		}
	}

	return feed.Channel.Items, nil
}

func (ns *NewsService) GetLatestNews(limit int) ([]NewsItem, error) {
	items, err := ns.GetGoogleNews()
	if err != nil {
		return nil, err
	}
	if limit > 0 && limit < len(items) {
		return items[:limit], nil
	}
	return items, nil
}

func (ns *NewsService) FilterNewsByKeywords(items []NewsItem, keywords []string) []NewsItem {
	if len(keywords) == 0 {
		return items
	}
	filtered := make([]NewsItem, 0)
	for _, item := range items {
		for _, keyword := range keywords {
			if contains(item.Title, keyword) || contains(item.Description, keyword) {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ============================================================================
// [FIX BUG #15] SENTIMIENTO CUANTITATIVO (FINNHUB API + FALLBACK)
// ============================================================================

// FinnhubSentimentResponse modela la respuesta de la API de Finnhub
type FinnhubSentimentResponse struct {
	Sentiment struct {
		BearishPercent float64 `json:"bearishPercent"`
		BullishPercent float64 `json:"bullishPercent"`
	} `json:"sentiment"`
}

// GetSentiment obtiene el score de sentimiento. Intenta Finnhub primero, luego usa el fallback interno.
func (ns *NewsService) GetSentiment(symbol string) (float64, string, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	
	// 1. Intentar con Finnhub si hay API Key
	if apiKey != "" {
		score, err := ns.getFinnhubSentiment(symbol, apiKey)
		if err == nil {
			return score, "Finnhub (API)", nil
		}
		log.Printf("⚠️ [NEWS] Finnhub sentiment falló (usando fallback interno): %v", err)
	}

	// 2. Fallback: Análisis interno de palabras clave
	log.Println("⚠️ [NEWS] Usando análisis interno de keywords (Fallback).")
	score, err := ns.calculateInternalSentiment(symbol)
	if err != nil {
		return 0.0, "Fallback (Error)", err
	}
	return score, "Internal Keyword Analysis (Fallback)", nil
}

// getFinnhubSentiment llama a la API de Finnhub
func (ns *NewsService) getFinnhubSentiment(symbol string, apiKey string) (float64, error) {
	cleanSymbol := strings.ToUpper(strings.Split(symbol, ":")[0])
	url := fmt.Sprintf("https://finnhub.io/api/v1/news-sentiment?symbol=%s&token=%s", cleanSymbol, apiKey)
	
	resp, err := ns.httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("finnhub API error: status %d", resp.StatusCode)
	}

	var result FinnhubSentimentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	// Finnhub devuelve porcentajes de 0 a 100.
	// Convertimos a escala de -1.0 a +1.0: (Bullish - Bearish) / 100
	score := (result.Sentiment.BullishPercent - result.Sentiment.BearishPercent) / 100.0
	
	// Clamping por seguridad
	if score > 1.0 { score = 1.0 }
	if score < -1.0 { score = -1.0 }
	
	return score, nil
}

// calculateInternalSentiment hace un análisis de palabras clave en los titulares (Fallback)
func (ns *NewsService) calculateInternalSentiment(symbol string) (float64, error) {
	// Buscar noticias específicas del símbolo
	items, err := ns.GetGoogleNewsSearch(symbol + " stock OR futures")
	if err != nil || len(items) == 0 {
		// Si falla, buscar noticias generales de mercado
		items, err = ns.GetGoogleNews()
		if err != nil {
			return 0, err
		}
	}

	// Limitar a las 20 noticias más recientes para el análisis
	limit := 20
	if len(items) < limit { limit = len(items) }

	scoreSum := 0.0
	wordCount := 0

	// Diccionarios simples de palabras alcistas y bajistas
	bullishWords := []string{"surge", "rally", "jump", "gain", "record", "high", "beat", "up", "bull", "growth", "strong"}
	bearishWords := []string{"drop", "fall", "plunge", "crash", "loss", "low", "miss", "down", "bear", "weak", "fear", "slump"}

	for i := 0; i < limit; i++ {
		title := strings.ToLower(items[i].Title)
		
		for _, w := range bullishWords {
			if strings.Contains(title, w) { scoreSum += 0.1; wordCount++ }
		}
		for _, w := range bearishWords {
			if strings.Contains(title, w) { scoreSum -= 0.1; wordCount++ }
		}
	}

	if wordCount == 0 {
		return 0.0, nil // Neutral si no hay palabras clave
	}

	// Normalizar el score entre -1 y 1
	finalScore := scoreSum / float64(wordCount)
	return math.Max(-1.0, math.Min(1.0, finalScore)), nil
}

// ============================================================================
// [MEJORA INSIDER] INSIDER TRADING (FINNHUB)
// ============================================================================

type FinnhubInsiderResponse struct {
	Data []struct {
		Name              string  `json:"name"`
		Share             float64 `json:"share"`
		Change            float64 `json:"change"`
		FilingDate        string  `json:"filingDate"`
		TransactionDate   string  `json:"transactionDate"`
		TransactionPrice  float64 `json:"transactionPrice"`
		TransactionCode   string  `json:"transactionCode"`
	} `json:"data"`
}

// GetInsiderTrading obtiene las últimas transacciones de insiders (CEO, directores) usando Finnhub.
func (ns *NewsService) GetInsiderTrading(symbol string) (string, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("FINNHUB_API_KEY no está configurada")
	}

	// Limpiar el símbolo (ej. AAPL)
	cleanSymbol := strings.ToUpper(strings.Split(symbol, ":")[0])
	
	url := fmt.Sprintf("https://finnhub.io/api/v1/stock/insider-transactions?symbol=%s&token=%s", cleanSymbol, apiKey)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := ns.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("finnhub API error: status %d", resp.StatusCode)
	}

	var result FinnhubInsiderResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse insider data: %w", err)
	}

	if len(result.Data) == 0 {
		return fmt.Sprintf("No hay transacciones de insiders recientes reportadas para %s.", cleanSymbol), nil
	}

	// Resumir las últimas 5 transacciones para no saturar el contexto del LLM
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Últimas transacciones de insiders para %s:\n", cleanSymbol))
	
	limit := 5
	if len(result.Data) < limit {
		limit = len(result.Data)
	}

	for i := 0; i < limit; i++ {
		tx := result.Data[i]
		action := "operó"
		if tx.TransactionCode == "P" {
			action = "COMPRÓ (Bullish)"
		} else if tx.TransactionCode == "S" {
			action = "VENDIÓ (Bearish)"
		}

		sb.WriteString(fmt.Sprintf("- %s %s %.0f acciones a $%.2f el %s\n", 
			tx.Name, action, math.Abs(tx.Change), tx.TransactionPrice, tx.TransactionDate))
	}

	return sb.String(), nil
}