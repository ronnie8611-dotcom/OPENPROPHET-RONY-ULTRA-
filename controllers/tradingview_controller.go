package controllers

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"prophet-trader/services"

	"github.com/gin-gonic/gin"
)

type TradingViewAlert struct {
	Secret     string  `json:"secret"`
	Action     string  `json:"action"`
	Symbol     string  `json:"symbol"`
	Price      float64 `json:"price"`
	StrategyID string  `json:"strategy_id"`
	Message    string  `json:"message"`
}

type TradingViewController struct {
	beat *services.AutonomousBeat
}

func NewTradingViewController(beat *services.AutonomousBeat) *TradingViewController {
	return &TradingViewController{beat: beat}
}

func (tc *TradingViewController) HandleWebhook(c *gin.Context) {
	var alert TradingViewAlert
	if err := c.ShouldBindJSON(&alert); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
		return
	}

	expectedSecret := os.Getenv("TRADINGVIEW_SECRET")
	if expectedSecret == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "TRADINGVIEW_SECRET not configured on server"})
		return
	}
	if alert.Secret != expectedSecret {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid secret"})
		return
	}

	action := strings.ToLower(alert.Action)
	if action != "buy" && action != "sell" && action != "close" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid action. Must be buy, sell or close."})
		return
	}

	var promptBuilder strings.Builder
	promptBuilder.WriteString("⚡ ALERTA DE TRADINGVIEW RECIBIDA:\n")
	promptBuilder.WriteString("- Símbolo: " + alert.Symbol + "\n")
	promptBuilder.WriteString("- Acción: " + action + "\n")
	if alert.Price > 0 {
		promptBuilder.WriteString("- Precio: " + strconv.FormatFloat(alert.Price, 'f', 2, 64) + "\n")
	}
	promptBuilder.WriteString("- Estrategia: " + alert.StrategyID + "\n")
	if alert.Message != "" {
		promptBuilder.WriteString("- Contexto: " + alert.Message + "\n")
	}
	promptBuilder.WriteString("\nINSTRUCCIÓN: Analiza esta señal con THE STRATEGIST y valida el riesgo con THE RISK MANAGER. Si es aprobada, genera la intención para que yo la autorice.")

	tc.beat.InjectMessage(promptBuilder.String())

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Alert received and queued for agent"})
}