package services

import (
	"log"
	"sync"
	"time"
)

// BookmapOrderFlow representa el flujo de órdenes que obtendremos de Bookmap
type BookmapOrderFlow struct {
	Symbol              string    `json:"symbol"`
	Timestamp           time.Time `json:"timestamp"`
	AggressiveBuyVolume float64   `json:"aggressive_buy_volume"`
	AggressiveSellVolume float64  `json:"aggressive_sell_volume"`
	IcebergBidVolume    float64   `json:"iceberg_bid_volume"`
	IcebergAskVolume    float64   `json:"iceberg_ask_volume"`
	DepthImbalance      float64   `json:"depth_imbalance"` // -1.0 a 1.0
}

// BookmapService manejará la conexión con Bookmap (API / Addon / CSV)
type BookmapService struct {
	mu           sync.RWMutex
	latestData   map[string]*BookmapOrderFlow
	isConnected  bool
}

// NewBookmapService inicializa el servicio
func NewBookmapService() *BookmapService {
	return &BookmapService{
		latestData: make(map[string]*BookmapOrderFlow),
		isConnected: false,
	}
}

// Start inicia la escucha de Bookmap (Por ahora es un placeholder)
func (bs *BookmapService) Start() {
	log.Println("[BOOKMAP] Servicio iniciado. Esperando conexión con Bookmap (Addon o CSV)...")
	
	// TODO: Cuando el usuario compre Bookmap, aquí conectaremos:
	// 1. Un WebSocket al Addon de Java (ws://localhost:7777)
	// 2. O leeremos los archivos históricos.
	
	// Por ahora, simulamos que NO está conectado para que el bot use el proxy QQQ.
	bs.mu.Lock()
	bs.isConnected = false
	bs.mu.Unlock()
}

// GetLatestOrderFlow devuelve el flujo de órdenes para un símbolo (si existe)
func (bs *BookmapService) GetLatestOrderFlow(symbol string) (*BookmapOrderFlow, bool) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	
	data, exists := bs.latestData[symbol]
	return data, exists && bs.isConnected
}

// UpdateOrderFlow (Para uso futuro del Addon o CSV)
func (bs *BookmapService) UpdateOrderFlow(data BookmapOrderFlow) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.latestData[data.Symbol] = &data
	bs.isConnected = true
}

// IsAvailable indica si tenemos datos reales de Bookmap
func (bs *BookmapService) IsAvailable() bool {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return bs.isConnected
}