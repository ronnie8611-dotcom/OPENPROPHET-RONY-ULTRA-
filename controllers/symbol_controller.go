package controllers

import (
	"context"
	"strings"
	"time"

	"prophet-trader/services"
	"prophet-trader/tws"

	"github.com/gin-gonic/gin"
)

// SymbolController exposes company/ticker search and a best-effort IBKR
// contract validation for the Watchlist UI.
type SymbolController struct {
	searcher *services.SymbolSearcher
	resolver *tws.ContractResolver
}

func NewSymbolController(searcher *services.SymbolSearcher, resolver *tws.ContractResolver) *SymbolController {
	return &SymbolController{searcher: searcher, resolver: resolver}
}

func (sc *SymbolController) HandleSearch(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if len(query) < 2 {
		c.JSON(200, gin.H{"results": []services.SymbolSearchResult{}})
		return
	}
	results := sc.searcher.Search(c.Request.Context(), query)
	c.JSON(200, gin.H{"results": results})
}

func (sc *SymbolController) HandleValidate(c *gin.Context) {
	symbol := strings.ToUpper(strings.TrimSpace(c.Query("symbol")))
	if symbol == "" {
		c.JSON(400, gin.H{"validated": false, "error": "symbol is required"})
		return
	}
	if sc.resolver == nil {
		c.JSON(200, gin.H{"validated": false, "symbol": symbol, "reason": "contract resolver not available"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	contract, err := sc.resolver.Resolve(ctx, symbol)
	if err != nil {
		c.JSON(200, gin.H{"validated": false, "symbol": symbol, "reason": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"validated": true,
		"symbol":    symbol,
		"con_id":    contract.ConId,
		"sec_type":  string(contract.SecType),
		"exchange":  contract.Exchange,
	})
}
