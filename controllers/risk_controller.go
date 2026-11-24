package controllers

import (
	"context"
	"net/http"
	"time"

	"prophet-trader/database"
	"prophet-trader/interfaces"
	"prophet-trader/services"

	"github.com/gin-gonic/gin"
)

type RiskController struct {
	trading interfaces.TradingService
	data    interfaces.DataService
	storage *database.LocalStorage
}

func NewRiskController(trading interfaces.TradingService, data interfaces.DataService, storage *database.LocalStorage) *RiskController {
	return &RiskController{trading: trading, data: data, storage: storage}
}

func (rc *RiskController) HandleGetRiskMetrics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, services.ComputeRiskMetrics(ctx, rc.trading, rc.data, rc.storage))
}
