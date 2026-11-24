package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type GeminiService struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

type DeepSeekRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type DeepSeekResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type CleanedNews struct {
	GeneratedAt      time.Time         `json:"generated_at"`
	SourceCount      int               `json:"source_count"`
	ArticleCount     int               `json:"article_count"`
	MarketSentiment  string            `json:"market_sentiment"`
	KeyThemes        []string          `json:"key_themes"`
	StockMentions    map[string]string `json:"stock_mentions"`
	ActionableItems  []string          `json:"actionable_items"`
	ExecutiveSummary string            `json:"executive_summary"`
	FullAnalysis     string            `json:"full_analysis"`
}

func NewGeminiService(apiKey string) *GeminiService {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY") // [QA] Añadido soporte explícito para DeepSeek
	}

	model := os.Getenv("NEWS_CLEAN_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	return &GeminiService{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		model: model,
	}
}

func (gs *GeminiService) CleanNewsForTrading(newsItems []NewsItem) (*CleanedNews, error) {
	if len(newsItems) == 0 {
		return nil, fmt.Errorf("no news items provided")
	}

	var newsText strings.Builder
	for i, item := range newsItems {
		newsText.WriteString(fmt.Sprintf("[%d] %s\n", i+1, item.Title))
		if item.Description != "" {
			cleanDesc := strings.ReplaceAll(item.Description, "<", "")
			cleanDesc = strings.ReplaceAll(cleanDesc, ">", "")
			newsText.WriteString(fmt.Sprintf("   %s\n", cleanDesc[:min(200, len(cleanDesc))]))
		}
		newsText.WriteString(fmt.Sprintf("   Source: %s | Published: %s\n\n", item.Source, item.PubDate))
	}

	prompt := fmt.Sprintf(`You are a financial analyst AI. Analyze the following %d news articles and create a CONCISE trading intelligence report.

NEWS ARTICLES:
%s

Provide a JSON response with this EXACT structure:
{
  "market_sentiment": "BULLISH|BEARISH|NEUTRAL",
  "key_themes": ["theme1", "theme2", "theme3"],
  "stock_mentions": {
    "SYMBOL": "POSITIVE|NEGATIVE|NEUTRAL with 1-sentence reason"
  },
  "actionable_items": ["brief actionable insight 1", "brief actionable insight 2"],
  "executive_summary": "2-3 sentence summary of the market situation"
}

Keep it BRIEF and DENSE. Maximum 200 tokens total.`, len(newsItems), newsText.String())

	response, err := gs.generateContentWithRetry(prompt)
	if err != nil {
		log.Printf("[WARNING] Fallo al limpiar noticias: %v. El agente continuará sin resumen de noticias.\n", err)
		return &CleanedNews{
			GeneratedAt:     time.Now(),
			ArticleCount:    len(newsItems),
			FullAnalysis:    "API de noticias no disponible temporalmente.",
			MarketSentiment: "NEUTRAL",
		}, nil
	}

	var cleanedNews CleanedNews
	cleanedNews.GeneratedAt = time.Now()
	cleanedNews.SourceCount = countUniqueSources(newsItems)
	cleanedNews.ArticleCount = len(newsItems)
	cleanedNews.FullAnalysis = response

	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		jsonStr := response[jsonStart : jsonEnd+1]
		var parsed struct {
			MarketSentiment  string            `json:"market_sentiment"`
			KeyThemes        []string          `json:"key_themes"`
			StockMentions    map[string]string `json:"stock_mentions"`
			ActionableItems  []string          `json:"actionable_items"`
			ExecutiveSummary string            `json:"executive_summary"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &parsed); err == nil {
			cleanedNews.MarketSentiment = parsed.MarketSentiment
			cleanedNews.KeyThemes = parsed.KeyThemes
			cleanedNews.StockMentions = parsed.StockMentions
			cleanedNews.ActionableItems = parsed.ActionableItems
			cleanedNews.ExecutiveSummary = parsed.ExecutiveSummary
		}
	}

	return &cleanedNews, nil
}

func (gs *GeminiService) generateContentWithRetry(prompt string) (string, error) {
	var lastErr error
	maxRetries := 3

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			waitTime := time.Duration(1<<uint(attempt-1)) * time.Second
			time.Sleep(waitTime)
		}
		response, err := gs.generateContent(prompt)
		if err == nil {
			return response, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func (gs *GeminiService) generateContent(prompt string) (string, error) {
	if gs.apiKey == "" {
		return "", fmt.Errorf("missing API key for LLM service")
	}

	url := "https://api.deepseek.com/chat/completions"

	reqBody := DeepSeekRequest{
		Model: gs.model,
		Messages: []Message{
			{Role: "user", Content: prompt},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gs.apiKey)

	resp, err := gs.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var dsResp DeepSeekResponse
	if err := json.Unmarshal(body, &dsResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(dsResp.Choices) == 0 {
		return "", fmt.Errorf("no content in response")
	}

	return dsResp.Choices[0].Message.Content, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func countUniqueSources(items []NewsItem) int {
	sources := make(map[string]bool)
	for _, item := range items {
		if item.Source != "" {
			sources[item.Source] = true
		}
	}
	return len(sources)
}