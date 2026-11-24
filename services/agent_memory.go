package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// [MEJORA 2] Estructuras para la Memoria a Largo Plazo
type TradeMemory struct {
	Timestamp     time.Time `json:"timestamp"`
	Symbol        string    `json:"symbol"`
	Setup         string    `json:"setup"` // Aquí guardaremos el nombre de la estrategia (ej. "fit_mom")
	Result        string    `json:"result"` // "WIN", "LOSS", "BREAKEVEN"
	PnL           float64   `json:"pnl"`
	MarketContext string    `json:"market_context"`
	Regime        string    `json:"regime,omitempty"`
	RSI           float64   `json:"rsi,omitempty"`
	ATRPercent    float64   `json:"atr_percent,omitempty"`
}

// TradeQuery is the current context used to recall similar past trades.
type TradeQuery struct {
	Symbol     string
	Setup      string
	Context    string
	RSI        float64
	ATRPercent float64
}

// ScoredTrade is a past trade with its similarity score (0..1).
type ScoredTrade struct {
	TradeMemory
	Score float64 `json:"score"`
}

type LessonMemory struct {
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
}

type AgentMemoryStore struct {
	Trades      []TradeMemory  `json:"trades"`
	Lessons     []LessonMemory `json:"lessons"`
	LastUpdated time.Time      `json:"last_updated"`
}

// [NUEVO] Estructura para el Dashboard de Rendimiento
type StrategyPerformance struct {
	StrategyName string  `json:"strategy_name"`
	TotalTrades  int     `json:"total_trades"`
	Wins         int     `json:"wins"`
	Losses       int     `json:"losses"`
	WinRate      float64 `json:"win_rate"`
	TotalPnL     float64 `json:"total_pnl"`
}

var (
	memoryMutex sync.Mutex
	memoryPath  = "data/agent_memory.json"
)

func InitMemory() {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	dir := filepath.Dir(memoryPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}
	if _, err := os.Stat(memoryPath); os.IsNotExist(err) {
		emptyStore := AgentMemoryStore{Trades: []TradeMemory{}, Lessons: []LessonMemory{}, LastUpdated: time.Now()}
		saveMemoryFile(emptyStore)
	}
}

func loadMemoryFile() (AgentMemoryStore, error) {
	var store AgentMemoryStore
	data, err := os.ReadFile(memoryPath)
	if err != nil {
		return store, err
	}
	err = json.Unmarshal(data, &store)
	return store, err
}

func saveMemoryFile(store AgentMemoryStore) error {
	store.LastUpdated = time.Now()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := memoryPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, memoryPath)
}

func SaveMemory(content string, tags []string) error {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	store, _ := loadMemoryFile()
	lesson := LessonMemory{Timestamp: time.Now(), Content: content, Tags: tags}
	store.Lessons = append(store.Lessons, lesson)
	if len(store.Lessons) > 50 {
		store.Lessons = store.Lessons[len(store.Lessons)-50:]
	}
	return saveMemoryFile(store)
}

func GetMemory(limit int) (AgentMemoryStore, error) {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	store, err := loadMemoryFile()
	if err != nil {
		return store, err
	}
	if len(store.Lessons) > limit {
		store.Lessons = store.Lessons[len(store.Lessons)-limit:]
	}
	if len(store.Trades) > limit {
		store.Trades = store.Trades[len(store.Trades)-limit:]
	}
	return store, nil
}

func LogTradeResult(symbol, setup, result string, pnl float64, context string) {
	LogTradeResultWithContext(symbol, setup, result, pnl, context, "", 0, 0)
}

// LogTradeResultWithContext stores structured context for similarity recall.
func LogTradeResultWithContext(symbol, setup, result string, pnl float64, marketContext, regime string, rsi, atrPercent float64) {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	store, _ := loadMemoryFile()
	trade := TradeMemory{
		Timestamp:     time.Now(),
		Symbol:        symbol,
		Setup:         setup,
		Result:        result,
		PnL:           pnl,
		MarketContext: marketContext,
		Regime:        regime,
		RSI:           rsi,
		ATRPercent:    atrPercent,
	}
	store.Trades = append(store.Trades, trade)
	if len(store.Trades) > 200 { 
		store.Trades = store.Trades[len(store.Trades)-50:]
	}
	_ = saveMemoryFile(store)
	fmt.Printf("[MEMORY] Trade logged: %s %s -> %s ($%.2f)\n", symbol, setup, result, pnl)
}

func RealizedPnLToday() float64 {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	store, err := loadMemoryFile()
	if err != nil {
		return 0
	}
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	total := 0.0
	for _, t := range store.Trades {
		if t.Timestamp.After(todayStart) {
			total += t.PnL
		}
	}
	return total
}

// [NUEVO] Calcula el rendimiento agrupado por estrategia
func GetStrategiesPerformance() []StrategyPerformance {
	memoryMutex.Lock()
	defer memoryMutex.Unlock()
	store, err := loadMemoryFile()
	if err != nil {
		return []StrategyPerformance{}
	}

	statsMap := make(map[string]*StrategyPerformance)

	// Inicializar las estrategias conocidas para que salgan en el dashboard aunque tengan 0 trades
	for _, name := range []string{"fit_mom", "raschke", "combined"} {
		statsMap[name] = &StrategyPerformance{StrategyName: name}
	}

	for _, t := range store.Trades {
		strat := t.Setup
		if strat == "" { strat = "manual" }
		
		if _, exists := statsMap[strat]; !exists {
			statsMap[strat] = &StrategyPerformance{StrategyName: strat}
		}
		
		stats := statsMap[strat]
		stats.TotalTrades++
		stats.TotalPnL += t.PnL
		
		if t.PnL > 0 {
			stats.Wins++
		} else if t.PnL < 0 {
			stats.Losses++
		}
	}

	var result []StrategyPerformance
	for _, stats := range statsMap {
		if stats.TotalTrades > 0 {
			stats.WinRate = (float64(stats.Wins) / float64(stats.TotalTrades)) * 100
		}
		result = append(result, *stats)
	}

	return result
}