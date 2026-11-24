package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"prophet-trader/config"
	"prophet-trader/configstore"
	"prophet-trader/interfaces"
	"prophet-trader/tws"
)

// BeatConfig controls which instruments the autonomous beat is allowed to analyse.
type BeatConfig struct {
	Enabled bool     `json:"enabled"`
	Symbols []string `json:"symbols"`
	Notes   string   `json:"notes"`
}

// TradingRulesConfig holds the documented playbook. The rules are only injected
// into the agent prompt when Enabled is true; while false they stay in the file
// as reference and never reach the LLM.
type TradingRulesConfig struct {
	Enabled        bool     `json:"enabled"`
	Version        int      `json:"version"`
	Note           string   `json:"note"`
	MarketMandate  []string `json:"market_mandate"`
	Setups         []string `json:"setups"`
	RiskManagement []string `json:"risk_management"`
}

type StrategyConfig struct {
	SchemaVersion    int                    `json:"schemaVersion"`
	ActiveStrategies []string               `json:"active_strategies"`
	ObserveSymbols   []string               `json:"observe_symbols"`
	Watchlist        []string               `json:"watchlist,omitempty"` // legacy alias for ObserveSymbols
	Beat             BeatConfig             `json:"beat"`
	TradingRules     TradingRulesConfig     `json:"trading_rules"`
	Strategies       map[string]interface{} `json:"strategies"`
	GlobalFilters    map[string]interface{} `json:"global_filters"`
}

// cleanSymbols trims, upper-cases and de-duplicates a symbol list.
func cleanSymbols(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		sym := strings.ToUpper(strings.TrimSpace(s))
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		out = append(out, sym)
	}
	return out
}

// observeSymbols returns the global "Observe" universe. New configs use
// observe_symbols; watchlist is kept as a legacy alias.
func (c *StrategyConfig) observeSymbols() []string {
	if c == nil {
		return nil
	}
	if syms := cleanSymbols(c.ObserveSymbols); len(syms) > 0 {
		return syms
	}
	return cleanSymbols(c.Watchlist)
}

// BeatSymbols returns the instruments the beat must focus on.
// Priority: beat.symbols -> observe_symbols/watchlist -> BEAT_SYMBOL -> ["QQQ"].
func (c *StrategyConfig) BeatSymbols() []string {
	if c != nil {
		if syms := cleanSymbols(c.Beat.Symbols); len(syms) > 0 {
			return syms
		}
		if syms := c.observeSymbols(); len(syms) > 0 {
			return syms
		}
	}
	if raw := os.Getenv("BEAT_SYMBOL"); raw != "" {
		if syms := cleanSymbols(strings.Split(raw, ",")); len(syms) > 0 {
			return syms
		}
	}
	return []string{"QQQ"}
}

// TradingRulesPrompt renders the documented playbook for the system prompt.
// It returns "" when the rules are disabled, so a disabled rule set never
// leaks into the agent's instructions.
func (c *StrategyConfig) TradingRulesPrompt() string {
	if c == nil || !c.TradingRules.Enabled {
		return ""
	}
	var b strings.Builder
	b.WriteString("TRADING RULES (ACTIVE PLAYBOOK):")
	writeSection := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n\n" + title + ":\n")
		for _, l := range lines {
			b.WriteString("- " + strings.TrimSpace(l) + "\n")
		}
	}
	writeSection("MARKET MANDATE", c.TradingRules.MarketMandate)
	writeSection("SETUPS", c.TradingRules.Setups)
	writeSection("RISK MANAGEMENT", c.TradingRules.RiskManagement)
	return strings.TrimSpace(b.String())
}

var (
	strategiesCache   *StrategyConfig
	strategiesCacheMu sync.RWMutex
)

func LoadStrategiesConfig() *StrategyConfig {
	strategiesCacheMu.RLock()
	cached := strategiesCache
	strategiesCacheMu.RUnlock()
	if cached != nil {
		return cached
	}

	data, err := os.ReadFile("data/strategies.json")
	if err != nil {
		cfg := &StrategyConfig{ActiveStrategies: []string{"raschke", "mean_reversion"}}
		strategiesCacheMu.Lock()
		strategiesCache = cfg
		strategiesCacheMu.Unlock()
		return cfg
	}
	var cfg StrategyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		cfg = StrategyConfig{ActiveStrategies: []string{"raschke", "mean_reversion"}}
	}
	strategiesCacheMu.Lock()
	strategiesCache = &cfg
	strategiesCacheMu.Unlock()
	return &cfg
}

// ReloadStrategiesConfig invalidates the in-memory config so dashboard edits
// are visible without restarting the bot.
func ReloadStrategiesConfig() {
	strategiesCacheMu.Lock()
	strategiesCache = nil
	strategiesCacheMu.Unlock()
}

// strategyEngine resolves the execution engine for a strategy id. New
// strategies can reuse an existing engine (raschke, mean_reversion…).
func strategyEngine(cfg *StrategyConfig, id string) string {
	if cfg != nil {
		if m, ok := cfg.Strategies[id].(map[string]interface{}); ok {
			if e, ok := m["engine"].(string); ok && strings.TrimSpace(e) != "" {
				return strings.TrimSpace(e)
			}
		}
	}
	return id
}

// ActiveStrategyIDs merges active_strategies with every strategy whose
// enabled flag is true, so the panel can turn strategies on/off.
func ActiveStrategyIDs(cfg *StrategyConfig) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.ActiveStrategies))
	seen := make(map[string]bool, len(cfg.ActiveStrategies))
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range cfg.ActiveStrategies {
		add(id)
	}
	extras := make([]string, 0)
	for id, raw := range cfg.Strategies {
		if seen[id] {
			continue
		}
		if m, ok := raw.(map[string]interface{}); ok {
			if enabled, _ := m["enabled"].(bool); enabled {
				extras = append(extras, id)
			}
		}
	}
	sort.Strings(extras)
	for _, id := range extras {
		add(id)
	}
	return out
}

func strategySymbolList(stratMap map[string]interface{}, primary, legacy string) []string {
	for _, key := range []string{primary, legacy} {
		raw, ok := stratMap[key]
		if !ok {
			continue
		}
		switch list := raw.(type) {
		case []interface{}:
			out := make([]string, 0, len(list))
			for _, item := range list {
				out = append(out, fmt.Sprint(item))
			}
			return cleanSymbols(out)
		case []string:
			return cleanSymbols(list)
		}
	}
	return nil
}

func isSymbolAllowed(cfg *StrategyConfig, strategyID, symbol string) (bool, string) {
	if cfg == nil {
		return false, "strategy config not loaded"
	}
	strat, ok := cfg.Strategies[strategyID]
	if !ok {
		return false, fmt.Sprintf("strategy %q not configured", strategyID)
	}
	stratMap, ok := strat.(map[string]interface{})
	if !ok {
		return false, "strategy config malformed"
	}

	if blocked := strategySymbolList(stratMap, "operate_block", "blacklist_symbols"); len(blocked) > 0 {
		for _, s := range blocked {
			if strings.EqualFold(s, symbol) {
				return false, fmt.Sprintf("%s is blocked (Operate block)", symbol)
			}
		}
	}
	if allowed := strategySymbolList(stratMap, "operate_allow", "whitelist_symbols"); len(allowed) > 0 {
		for _, s := range allowed {
			if strings.EqualFold(s, symbol) {
				if observe := cfg.observeSymbols(); len(observe) > 0 {
					for _, obs := range observe {
						if strings.EqualFold(obs, symbol) {
							return true, ""
						}
					}
					return false, fmt.Sprintf("%s is in Operate allow but not in the global Observe list", symbol)
				}
				return true, ""
			}
		}
		return false, fmt.Sprintf("%s is not in Operate allow for strategy %s", symbol, strategyID)
	}
	// Empty Operate allow inherits the global Observe list. This is the
	// Option B rule: one Observe source, strategies only narrow it.
	if observe := cfg.observeSymbols(); len(observe) > 0 {
		for _, s := range observe {
			if strings.EqualFold(s, symbol) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%s is not in the global Observe list", symbol)
	}
	return true, ""
}

func predictWithML(features []float64) bool {
	pyPath := "python"
	if config.AppConfig != nil && config.AppConfig.PythonPath != "" {
		pyPath = config.AppConfig.PythonPath
	}
	args := []string{"predict.py"}
	for _, f := range features {
		args = append(args, fmt.Sprintf("%f", f))
	}
	
	cmd := exec.Command(pyPath, args...)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[ML ERROR] Falló la predicción: %v", err)
		return false
	}
	
	result := strings.TrimSpace(string(out))
	return result == "1"
}

func validateOrderDeterministic(ctx context.Context, tc *ToolContext, order *interfaces.Order, isClosing bool) error {
	if tc.Trading == nil { return nil }
	acc, err := tc.Trading.GetAccount(ctx)
	if err != nil || acc == nil { return fmt.Errorf("no se pudo verificar la cuenta") }
	if err := checkWeeklyLossLimit(tc); err != nil { if !isClosing { return err } }
	if err := checkDailyLossLimit(tc); err != nil { if !isClosing { return err } }
	if !isClosing && order.Qty > 0 {
		price := 0.0
		if tc.Data != nil {
			quote, err := tc.Data.GetLatestQuote(ctx, order.Symbol)
			if err == nil && quote != nil { if quote.AskPrice > 0 { price = quote.AskPrice } else if quote.BidPrice > 0 { price = quote.BidPrice } }
		}
		if price > 0 {
			estimatedValue := order.Qty * price
			multiplier := 1.0
			symbolUpper := strings.ToUpper(order.Symbol)
			if strings.Contains(symbolUpper, "MNQ") { multiplier = 2.0 } else if strings.Contains(symbolUpper, "NQ") { multiplier = 20.0 } else if strings.Contains(symbolUpper, "MES") { multiplier = 5.0 } else if strings.Contains(symbolUpper, "ES") { multiplier = 50.0 } else if strings.Contains(symbolUpper, "MYM") { multiplier = 0.50 } else if strings.Contains(symbolUpper, "YM") { multiplier = 5.0 } else if strings.Contains(symbolUpper, "M2K") { multiplier = 5.0 } else if strings.Contains(symbolUpper, "RTY") { multiplier = 50.0 } else if strings.Contains(symbolUpper, "GC") { multiplier = 100.0 }
			realValue := estimatedValue * multiplier
			perms := configstore.Get().Permissions
			maxPosPct := float64(perms.MaxPositionPct)
			if maxPosPct <= 0 { maxPosPct = 20.0 }
			maxAllowed := acc.PortfolioValue * (maxPosPct / 100.0)
			if realValue > maxAllowed && !strings.Contains(strings.ToUpper(order.Symbol), "SPY") {
				return fmt.Errorf("HARD STOP: El valor excede el %.1f%% del NAV permitido", maxPosPct)
			}
			if err := checkPortfolioCaps(ctx, tc.Trading, order.Symbol, realValue); err != nil { return err }
		}
	}
	isFuture := strings.Contains(strings.ToUpper(order.Symbol), "MNQ") || strings.Contains(strings.ToUpper(order.Symbol), "NQ") || strings.Contains(strings.ToUpper(order.Symbol), "ES")
	if !isClosing && isFuture && order.StopLossPrice == nil && order.Type == "market" {
		return fmt.Errorf("HARD STOP: Futuros a mercado REQUIEREN Stop Loss")
	}
	return nil
}

func checkWeeklyLossLimit(tc *ToolContext) error {
	if tc.Trading == nil { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acc, err := tc.Trading.GetAccount(ctx)
	if err != nil || acc == nil { return nil }

	maxWeeklyLossPct := 8.0
	if perms := configstore.Get().Permissions; perms.MaxWeeklyLoss > 0 { maxWeeklyLossPct = perms.MaxWeeklyLoss }

	baseline := weeklyBaselineNAV(acc.PortfolioValue, time.Now())
	if weeklyLossBreached(acc.PortfolioValue, baseline, maxWeeklyLossPct) {
		return fmt.Errorf("WEEKLY CIRCUIT BREAKER: pérdida semanal supera el %.0f%% (NAV actual $%.2f vs baseline semanal $%.2f). El bot no operará hasta el próximo lunes", maxWeeklyLossPct, acc.PortfolioValue, baseline)
	}
	return nil
}

func checkDailyLossLimit(tc *ToolContext) error {
	if tc.Trading == nil { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acc, err := tc.Trading.GetAccount(ctx)
	if err != nil || acc == nil { return nil }
	positions, err := tc.Trading.GetPositions(ctx)
	if err != nil { return nil }
	unrealizedPnL := 0.0
	for _, p := range positions { unrealizedPnL += p.UnrealizedPL }
	realizedPnL := RealizedPnLToday()
	totalPnL := unrealizedPnL + realizedPnL
	maxDailyLossPct := 5.0
	if perms := configstore.Get().Permissions; perms.MaxDailyLoss > 0 { maxDailyLossPct = float64(perms.MaxDailyLoss) }
	maxLoss := acc.PortfolioValue * (maxDailyLossPct / 100.0)
	if totalPnL < -maxLoss {
		return fmt.Errorf("KILL SWITCH: Pérdida diaria supera el límite del %.1f%%", maxDailyLossPct)
	}
	return nil
}

// instrumentMultiplier maps a futures root to its point value. Stocks/ETFs = 1.
func instrumentMultiplier(symbol string) float64 {
	if strings.Contains(symbol, ":") {
		return 100.0 // listed option contract multiplier
	}
	symbolUpper := strings.ToUpper(symbol)
	switch {
	case strings.Contains(symbolUpper, "MNQ"):
		return 2.0
	case strings.Contains(symbolUpper, "NQ"):
		return 20.0
	case strings.Contains(symbolUpper, "MES"):
		return 5.0
	case strings.Contains(symbolUpper, "ES"):
		return 50.0
	case strings.Contains(symbolUpper, "MYM"):
		return 0.50
	case strings.Contains(symbolUpper, "YM"):
		return 5.0
	case strings.Contains(symbolUpper, "M2K"):
		return 5.0
	case strings.Contains(symbolUpper, "RTY"):
		return 50.0
	case strings.Contains(symbolUpper, "GC"):
		return 100.0
	default:
		return 1.0
	}
}

// orderNotional converts qty × price into USD for stocks and futures.
func orderNotional(qty, price float64, symbol string) float64 {
	if qty == 0 || price <= 0 {
		return 0
	}
	return math.Abs(qty) * math.Abs(price) * instrumentMultiplier(symbol)
}

// grossExposure sums the absolute cost basis of every open position.
func grossExposure(positions []*interfaces.Position) float64 {
	total := 0.0
	for _, p := range positions {
		if p == nil || p.Qty == 0 {
			continue
		}
		if p.CostBasis != 0 {
			total += math.Abs(p.CostBasis)
		} else {
			total += math.Abs(p.MarketValue)
		}
	}
	return total
}

func openPositionCount(positions []*interfaces.Position) int {
	n := 0
	for _, p := range positions {
		if p != nil && p.Qty != 0 {
			n++
		}
	}
	return n
}

func holdsSymbol(positions []*interfaces.Position, symbol string) bool {
	target := strings.ToUpper(underlyingSymbol(symbol))
	for _, p := range positions {
		if p == nil || p.Qty == 0 {
			continue
		}
		if strings.EqualFold(underlyingSymbol(p.Symbol), target) {
			return true
		}
	}
	return false
}

// checkPortfolioCaps enforces the two portfolio-level hard limits that used to
// live only in the prompt: max deployed capital and max open positions.
func checkPortfolioCaps(ctx context.Context, trading interfaces.TradingService, symbol string, orderValue float64) error {
	if trading == nil {
		return nil
	}
	acc, err := trading.GetAccount(ctx)
	if err != nil || acc == nil {
		return fmt.Errorf("no se pudo verificar la cuenta para los límites de cartera")
	}
	positions, err := trading.GetPositions(ctx)
	if err != nil {
		return fmt.Errorf("no se pudo verificar las posiciones para los límites de cartera: %w", err)
	}

	perms := configstore.Get().Permissions

	maxOpen := perms.MaxOpenPositions
	if maxOpen <= 0 {
		maxOpen = 10
	}
	if !holdsSymbol(positions, symbol) && openPositionCount(positions) >= maxOpen {
		return fmt.Errorf("HARD STOP: máximo de %d posiciones abiertas alcanzado", maxOpen)
	}

	maxDeployed := perms.MaxDeployedPct
	if maxDeployed <= 0 {
		maxDeployed = 60
	}
	if acc.PortfolioValue > 0 {
		exposure := grossExposure(positions)
		limit := acc.PortfolioValue * maxDeployed / 100
		if exposure+orderValue > limit {
			return fmt.Errorf("HARD STOP: capital desplegado $%.0f + orden $%.0f excede el %.0f%% del NAV ($%.0f)", exposure, orderValue, maxDeployed, limit)
		}
	}
	return nil
}

// checkPerInstrumentCap enforces the % of NAV cap for a single symbol.
func checkPerInstrumentCap(ctx context.Context, trading interfaces.TradingService, symbol string, orderValue float64) error {
	if trading == nil || orderValue <= 0 {
		return nil
	}
	if strings.Contains(strings.ToUpper(symbol), "SPY") {
		return nil
	}
	acc, err := trading.GetAccount(ctx)
	if err != nil || acc == nil || acc.PortfolioValue <= 0 {
		return nil
	}
	positions, err := trading.GetPositions(ctx)
	if err != nil {
		return fmt.Errorf("no se pudo verificar las posiciones: %w", err)
	}
	perms := configstore.Get().Permissions
	maxPos := perms.MaxPositionPct
	if maxPos <= 0 {
		maxPos = 20
	}
	existing := 0.0
	for _, p := range positions {
		if p == nil || p.Qty == 0 {
			continue
		}
		if strings.EqualFold(underlyingSymbol(p.Symbol), underlyingSymbol(symbol)) {
			existing += math.Abs(p.CostBasis)
		}
	}
	limit := acc.PortfolioValue * maxPos / 100
	if existing+orderValue > limit {
		return fmt.Errorf("HARD STOP: exposición a %s $%.0f + orden $%.0f excede el %.1f%% del NAV ($%.0f)", symbol, existing, orderValue, maxPos, limit)
	}
	return nil
}

func BuildAgentTools(mode config.TradingMode) []interfaces.LLMTool {
	isSuggestMode := mode == config.TradingModeSuggest
	tools := []interfaces.LLMTool{
		{Name: "get_account", Description: "Get account status", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{Name: "get_positions", Description: "Get open positions", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{Name: "get_options_positions", Description: "Get options positions", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{Name: "get_active_strategies", Description: "Get active strategies", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{Name: "check_strategy", Description: "Check if a specific strategy (fit_mom, raschke, combined, turtle_breakout, mean_reversion) has a valid setup.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"strategy_id": map[string]interface{}{"type": "string", "enum": []string{"fit_mom", "raschke", "combined", "turtle_breakout", "mean_reversion"}}, "symbol": map[string]interface{}{"type": "string"}}, "required": []string{"strategy_id", "symbol"}}},
		{Name: "place_managed_position", Description: "Open a NEW position with automated SL/TP.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "side": map[string]interface{}{"type": "string", "enum": []string{"buy", "sell"}}, "strategy": map[string]interface{}{"type": "string"}, "entry_strategy": map[string]interface{}{"type": "string", "enum": []string{"market", "limit"}}, "entry_price": map[string]interface{}{"type": "number"}, "stop_loss_percent": map[string]interface{}{"type": "number"}, "take_profit_percent": map[string]interface{}{"type": "number"}, "notes": map[string]interface{}{"type": "string"}, "explicit_quantity": map[string]interface{}{"type": "integer"}, "trailing_stop": map[string]interface{}{"type": "boolean"}, "trailing_activation_points": map[string]interface{}{"type": "number"}, "trailing_distance_points": map[string]interface{}{"type": "number"}}, "required": []string{"symbol", "side", "strategy", "entry_strategy", "explicit_quantity"}}},
		{Name: "liquidate_position", Description: "Close an existing position via market order.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "quantity": map[string]interface{}{"type": "integer"}}, "required": []string{"symbol", "quantity"}}},
		{Name: "place_standalone_order", Description: "Place a standalone Stop Loss or Limit order, optionally with a bracket (SL/TP).", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "side": map[string]interface{}{"type": "string", "enum": []string{"buy", "sell"}}, "qty": map[string]interface{}{"type": "number"}, "order_type": map[string]interface{}{"type": "string", "enum": []string{"stop", "limit", "market"}}, "price": map[string]interface{}{"type": "number"}, "bracket_sl_price": map[string]interface{}{"type": "number"}, "bracket_tp_price": map[string]interface{}{"type": "number"}}, "required": []string{"symbol", "side", "qty", "order_type"}}},
		{Name: "cancel_order", Description: "Cancel an open order by ID.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"order_id": map[string]interface{}{"type": "string"}}, "required": []string{"order_id"}}},
		{Name: "get_quote", Description: "Get quote. Check data_age_seconds. Warn if DELAYED.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "get_options_chain", Description: "Get options chain", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "expiration": map[string]interface{}{"type": "string"}}, "required": []string{"symbol", "expiration"}}},
		{Name: "build_spread", Description: "Build defined-risk spread", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"strategy": map[string]interface{}{"type": "string"}, "underlying": map[string]interface{}{"type": "string"}, "expiration": map[string]interface{}{"type": "string"}, "short_otm_pct": map[string]interface{}{"type": "number"}, "width": map[string]interface{}{"type": "number"}, "risk_budget_pct": map[string]interface{}{"type": "number"}, "strike_step": map[string]interface{}{"type": "number"}}, "required": []string{"strategy", "expiration", "short_otm_pct", "width"}}},
		{Name: "place_options_order", Description: "Place options order", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "action": map[string]interface{}{"type": "string"}, "qty": map[string]interface{}{"type": "integer"}, "order_type": map[string]interface{}{"type": "string"}, "lmt_price": map[string]interface{}{"type": "number"}}, "required": []string{"symbol", "action", "qty", "order_type"}}},
		{Name: "place_combo_order", Description: "Place combo order", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"legs": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "action": map[string]interface{}{"type": "string"}, "ratio": map[string]interface{}{"type": "integer"}}, "required": []string{"symbol", "action"}}}, "action": map[string]interface{}{"type": "string"}, "qty": map[string]interface{}{"type": "integer"}, "order_type": map[string]interface{}{"type": "string"}, "lmt_price": map[string]interface{}{"type": "number"}}, "required": []string{"legs", "qty", "order_type"}}},
		{Name: "set_heartbeat", Description: "Change heartbeat interval", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"seconds": map[string]interface{}{"type": "integer"}, "reason": map[string]interface{}{"type": "string"}}, "required": []string{"seconds", "reason"}}},
		{Name: "search_contract", Description: "Search contracts. Use for FUTURES.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "sec_type": map[string]interface{}{"type": "string"}, "exchange": map[string]interface{}{"type": "string"}, "currency": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "jim_rogers", Description: "Consult another agent", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"target_agent_id": map[string]interface{}{"type": "string"}, "prompt": map[string]interface{}{"type": "string"}}, "required": []string{"target_agent_id", "prompt"}}},
		{Name: "get_mnq_dashboard", Description: "Get a unified snapshot for MNQ.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "get_market_regime", Description: "Get the current market regime.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "calculate_mnq_setup", Description: "Calculate exact entry, stop loss, and take profit levels.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "direction": map[string]interface{}{"type": "string", "enum": []string{"long", "short"}}, "setup_type": map[string]interface{}{"type": "string", "enum": []string{"breakout", "vwap_reclaim"}}}, "required": []string{"symbol", "direction", "setup_type"}}},
		{Name: "get_calendar_events", Description: "Check for high-impact macro events.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "get_social_sentiment", Description: "Get quantitative sentiment score.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "get_correlated_assets", Description: "Check if QQQ and NQ are confirming.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}, "required": []string{}}},
		{Name: "get_order_flow_imbalance", Description: "Approximates buy/sell pressure.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "get_insider_activity", Description: "Get recent insider buying/selling.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}}, "required": []string{"symbol"}}},
		{Name: "save_memory", Description: "Save a lesson or pattern.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"content": map[string]interface{}{"type": "string"}, "tags": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}}}, "required": []string{"content"}}},
		{Name: "get_memory", Description: "Retrieve past trades and lessons. Optionally pass the current context to recall similar setups.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"limit": map[string]interface{}{"type": "integer"}, "context": map[string]interface{}{"type": "string"}, "symbol": map[string]interface{}{"type": "string"}, "setup": map[string]interface{}{"type": "string"}, "rsi": map[string]interface{}{"type": "number"}, "atr_percent": map[string]interface{}{"type": "number"}}, "required": []string{"limit"}}},
	}

	if !mode.AllowsExecution() {
		orderTools := map[string]bool{"place_managed_position": true, "place_options_order": true, "place_combo_order": true, "liquidate_position": true, "place_standalone_order": true, "cancel_order": true}
		filtered := tools[:0]
		for _, t := range tools { if !orderTools[t.Name] { filtered = append(filtered, t) } }
		tools = filtered
	}

	if isSuggestMode {
		tools = append(tools, interfaces.LLMTool{
			Name: "create_suggestion", Description: "Propose trade for human review.", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"symbol": map[string]interface{}{"type": "string"}, "side": map[string]interface{}{"type": "string"}, "action_type": map[string]interface{}{"type": "string"}, "quantity": map[string]interface{}{"type": "number"}, "order_type": map[string]interface{}{"type": "string"}, "limit_price": map[string]interface{}{"type": "number"}, "target_price": map[string]interface{}{"type": "number"}, "stop_price": map[string]interface{}{"type": "number"}, "rationale": map[string]interface{}{"type": "string"}, "confidence": map[string]interface{}{"type": "number"}, "timeframe": map[string]interface{}{"type": "string"}, "legs": map[string]interface{}{"type": "string"}}, "required": []string{"symbol", "side", "action_type", "quantity", "order_type", "rationale", "confidence", "timeframe"}},
		})
	}
	return tools
}

type ToolContext struct {
	Data                 interfaces.DataService
	PM                   *PositionManager
	Trading              interfaces.TradingService
	LLM                  interfaces.LLMProvider
	Intent               *IntentManager
	Beat                 *AutonomousBeat
	Resolver             *tws.ContractResolver
	RequireDoubleConfirm bool
	Suggestion           *SuggestionManager
	TradingMode          config.TradingMode
}

func underlyingSymbol(symbol string) string {
	if c, err := tws.ParseSymbol(symbol); err == nil && c.Symbol != "" { return c.Symbol }
	return symbol
}

func requireFreshSpot(ctx context.Context, tc *ToolContext, instrumentSymbol string) error {
	under := underlyingSymbol(instrumentSymbol)
	if tc.Data == nil { return fmt.Errorf("market data unavailable — refusing to place an order for %s", under) }
	qctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	q, err := tc.Data.GetLatestQuote(qctx, under)
	if err != nil { return fmt.Errorf("could not verify %s spot before order: %v — order blocked", under, err) }
	if ok, warn := q.Tradeability(time.Now(), 30*time.Second); !ok { return fmt.Errorf("refusing to place order: %s spot is not tradeable (%s)", under, warn) }
	return nil
}

func ExecuteAgentTool(ctx context.Context, toolName string, args []byte, tc *ToolContext) (string, error) {
	return HandleToolCall(ctx, toolName, args, tc)
}

func HandleToolCall(ctx context.Context, toolName string, args []byte, tc *ToolContext) (string, error) {
	switch toolName {
	case "get_account":
		acc, err := tc.Trading.GetAccount(ctx); if err != nil { return "", err }
		b, _ := json.Marshal(acc); return string(b), nil
	case "get_positions":
		pos, err := tc.Trading.GetPositions(ctx); if err != nil { return "", err }
		b, _ := json.Marshal(pos); return string(b), nil
	case "get_options_positions":
		pos, err := tc.Trading.ListOptionsPositions(ctx); if err != nil { return "", err }
		b, _ := json.Marshal(pos); return string(b), nil

	case "get_active_strategies":
		cfg := LoadStrategiesConfig()
		out := map[string]interface{}{ "active_strategies": ActiveStrategyIDs(cfg), "global_filters": cfg.GlobalFilters, "instructions": "Use 'check_strategy' with one of the active strategy IDs to see if there is a valid setup." }
		b, _ := json.Marshal(out); return string(b), nil

	case "check_strategy":
		var req struct { StrategyID string `json:"strategy_id"`; Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }

		cfg := LoadStrategiesConfig()
		engine := strategyEngine(cfg, req.StrategyID)
		allowed, blockReason := isSymbolAllowed(cfg, req.StrategyID, req.Symbol)
		if !allowed {
			blocked := map[string]interface{}{ "strategy_id": req.StrategyID, "symbol": req.Symbol, "setup_found": false, "blocked_by": "operate_allow", "reason": fmt.Sprintf("HARD STOP: %s.", blockReason) }
			b, _ := json.Marshal(blocked); return string(b), nil
		}

		dashArgs, _ := json.Marshal(map[string]string{"symbol": req.Symbol})
		dashStr, err := HandleToolCall(ctx, "get_mnq_dashboard", dashArgs, tc)
		if err != nil { return "", fmt.Errorf("failed to get dashboard for strategy check: %w", err) }
		
		var dash map[string]interface{}
		json.Unmarshal([]byte(dashStr), &dash)
		price, _ := dash["price"].(float64)
		pdh, _ := dash["pdh"].(float64)
		pdl, _ := dash["pdl"].(float64)
		pdMid := (pdh + pdl) / 2

		result := map[string]interface{}{ "strategy_id": req.StrategyID, "engine": engine, "symbol": req.Symbol, "setup_found": false, "reason": "No setup detected based on current price action." }

		if price > 0 && pdh > 0 {
			if engine == "fit_mom" {
				if price > pdMid && price < pdh {
					result["setup_found"] = true; result["direction"] = "long"; result["entry"] = price; result["stop_loss"] = pdl; result["take_profit"] = pdh; result["rr"] = math.Abs(pdh-price) / math.Abs(price-pdl)
					result["reason"] = "Trend Continuation Long"
				}
			} else if engine == "raschke" {
				if price >= pdh * 0.999 {
					result["setup_found"] = true; result["direction"] = "long"; result["entry"] = pdh; result["stop_loss"] = pdMid; result["take_profit"] = pdh + (pdh - pdl) * 0.5; result["rr"] = 2.0
					result["reason"] = "Volatility Breakout Long above PDH"
				} else if price <= pdl * 1.001 {
					result["setup_found"] = true; result["direction"] = "short"; result["entry"] = pdl; result["stop_loss"] = pdMid; result["take_profit"] = pdl - (pdh - pdl) * 0.5; result["rr"] = 2.0
					result["reason"] = "Volatility Breakout Short below PDL"
				}
			} else if engine == "mean_reversion" {
				ctxBars, cancel := context.WithTimeout(ctx, 10*time.Second)
				bars, err := tc.Data.GetHistoricalBars(ctxBars, req.Symbol, time.Now().AddDate(0, 0, -50), time.Now(), "1Day")
				cancel()
				if err == nil && len(bars) >= 20 {
					rsi := CalculateRSI(bars, 14)
					sma := CalculateSMA(bars, 20)
					variance := 0.0
					for i := len(bars)-20; i < len(bars); i++ { variance += math.Pow(bars[i].Close - sma, 2) }
					sd := math.Sqrt(variance / 20)
					upperBand := sma + (2 * sd); lowerBand := sma - (2 * sd)
					
					if price <= lowerBand && rsi < 30 {
						result["setup_found"] = true; result["direction"] = "long"; result["entry"] = price; result["stop_loss"] = price - sd; result["take_profit"] = sma; result["rr"] = 1.5
						result["reason"] = fmt.Sprintf("Mean Reversion LONG (Oversold). RSI: %.1f, Price < Lower BB", rsi)
					} else if price >= upperBand && rsi > 70 {
						result["setup_found"] = true; result["direction"] = "short"; result["entry"] = price; result["stop_loss"] = price + sd; result["take_profit"] = sma; result["rr"] = 1.5
						result["reason"] = fmt.Sprintf("Mean Reversion SHORT (Overbought). RSI: %.1f, Price > Upper BB", rsi)
					}
				}
			} else if engine == "turtle_breakout" {
				ctxBars, cancel := context.WithTimeout(ctx, 10*time.Second)
				dailyBars, err := tc.Data.GetHistoricalBars(ctxBars, "QQQ", time.Now().AddDate(0, 0, -40), time.Now(), "1Day")
				cancel()
				if err == nil && len(dailyBars) >= 20 {
					highestHigh20 := 0.0
					for _, b := range dailyBars[len(dailyBars)-20:] { if b.High > highestHigh20 { highestHigh20 = b.High } }
					lowestLow10 := 999999.0
					for _, b := range dailyBars[len(dailyBars)-10:] { if b.Low < lowestLow10 { lowestLow10 = b.Low } }
					trSum := 0.0
					startIdx := len(dailyBars) - 14; if startIdx < 1 { startIdx = 1 }
					for i := startIdx; i < len(dailyBars); i++ {
						high, low, prevClose := dailyBars[i].High, dailyBars[i].Low, dailyBars[i-1].Close
						tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
						trSum += tr
					}
					atr14 := trSum / 14.0
					if price > highestHigh20 {
						result["setup_found"] = true; result["direction"] = "long"; result["entry"] = price; result["stop_loss"] = price - (2.0 * atr14); result["trailing_stop"] = lowestLow10
						result["reason"] = fmt.Sprintf("Turtle Breakout: Ruptura sobre máximo de 20 días (%.2f). Stop fijo en %.2f, Trailing en %.2f", highestHigh20, price - (2.0*atr14), lowestLow10)
					}
				}
			} else if engine == "combined" {
				result["reason"] = "Combined strategy requires both to align."
			}
		}

		if result["setup_found"] == true && engine == "raschke" {
			ctxBars, cancel := context.WithTimeout(ctx, 10*time.Second)
			bars, err := tc.Data.GetHistoricalBars(ctxBars, req.Symbol, time.Now().AddDate(0, 0, -50), time.Now(), "1Day")
			cancel()
			if err == nil && len(bars) >= 20 {
				sma := CalculateSMA(bars, 20)
				atr := 0.0
				for i := len(bars)-14; i < len(bars); i++ { atr += bars[i].High - bars[i].Low }
				atr = atr / 14.0
				rsi := CalculateRSI(bars, 14)
				
				distPdh := (price - pdh) / price
				distPdl := (price - pdl) / price
				volatility := 0.01 
				
				features := []float64{sma, atr, rsi, distPdh, distPdl, volatility}
				
				mlApproves := predictWithML(features)
				if !mlApproves {
					result["setup_found"] = false
					result["reason"] = result["reason"].(string) + " [BLOQUEADO POR IA: Baja probabilidad de éxito]"
				} else {
					result["reason"] = result["reason"].(string) + " [APROBADO POR IA]"
				}
			}
		}

		b, _ := json.Marshal(result); return string(b), nil

	case "place_managed_position":
		if err := checkDailyLossLimit(tc); err != nil { return "", err }
		var req PlaceManagedPositionRequest
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		req.Side = strings.ToLower(req.Side)

		if req.Strategy != "" && req.Strategy != "DAY_TRADE" && req.Notes != "LIQUIDATION" {
			cfgWl := LoadStrategiesConfig()
			allowed, blockReason := isSymbolAllowed(cfgWl, req.Strategy, req.Symbol)
			if !allowed { return "", fmt.Errorf("HARD STOP (Observe/Operate): %s. Orden rechazada para %s con estrategia %s", blockReason, req.Symbol, req.Strategy) }
		}

		if tc.RequireDoubleConfirm && tc.Intent != nil {
			currentPrice := 0.0
			if tc.Data != nil {
				quoteCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				if quote, err := tc.Data.GetLatestQuote(quoteCtx, req.Symbol); err == nil && quote != nil { if quote.AskPrice > 0 { currentPrice = quote.AskPrice } else if quote.BidPrice > 0 { currentPrice = quote.BidPrice } }
				cancel()
			}
			qty := 0.0
			if req.ExplicitQuantity != nil { qty = float64(*req.ExplicitQuantity) } else if currentPrice > 0 { qty = tc.PM.resolveQuantity(&req, currentPrice) }
			id, err := tc.Intent.CreateIntent(IntentTypeManagedPosition, args, req.Symbol, req.Side, qty, currentPrice)
			if err != nil { return "", err }
			
			SendTelegramAlert(fmt.Sprintf("🚨 NUEVA ORDEN PENDIENTE: %s %s. Entra al Hub para aprobarla.", req.Side, req.Symbol))
			
			return fmt.Sprintf("Order intent created (Intent ID: %s). The order is NOT placed yet.", id), nil
		}
		
		totalQty := 0.0
		if req.ExplicitQuantity != nil { totalQty = float64(*req.ExplicitQuantity) }
		
		// Validate the WHOLE order before TWAP chunking so the portfolio caps
		// (deployed %, open positions, per-instrument %) cannot be bypassed by
		// splitting the size into chunks.
		if totalQty > 0 {
			dummyOrder := &interfaces.Order{Symbol: req.Symbol, Qty: totalQty, Side: req.Side, Type: "market", StopLossPrice: req.StopLossPrice, TakeProfitPrice: req.TakeProfitPrice}
			if err := validateOrderDeterministic(ctx, tc, dummyOrder, false); err != nil { return "", err }
		}


		if totalQty > 100 {
			chunks := int(math.Ceil(totalQty / 100.0))
			chunkSize := int(totalQty) / chunks
			
			log.Printf("[TWAP] Orden grande detectada (%d). Dividiendo en %d pedazos de %d.", int(totalQty), chunks, chunkSize)
			
			// Reserve one daily-trade slot for the whole TWAP order.
			if err := ReserveDailyTrade(time.Now()); err != nil { return "", err }


			go func() {
				for i := 0; i < chunks; i++ {
					qtyToSend := chunkSize
					if i == chunks-1 { qtyToSend = int(totalQty) - (chunkSize * (chunks - 1)) }
					
					reqCopy := req
					reqCopy.ExplicitQuantity = &qtyToSend
					reqCopy.SkipDailyCount = true
					
					log.Printf("[TWAP] Enviando pedazo %d/%d (%d acciones)...", i+1, chunks, qtyToSend)
					_, err := tc.PM.PlaceManagedPosition(ctx, &reqCopy)
					if err != nil {
						log.Printf("[TWAP ERROR] Falló el pedazo %d: %v", i+1, err)
						return
					}
					if i < chunks-1 {
						time.Sleep(2 * time.Second)
					}
				}
				log.Printf("[TWAP] Ejecución completada para %s.", req.Symbol)
			}()
			
			return fmt.Sprintf("Ejecución TWAP iniciada: %d acciones divididas en %d pedazos. Revisa el log.", int(totalQty), chunks), nil
		}

		if err := requireFreshSpot(ctx, tc, req.Symbol); err != nil { return "", err }
		pos, err := tc.PM.PlaceManagedPosition(ctx, &req); if err != nil { return "", err }
		b, _ := json.Marshal(pos); return string(b), nil

	case "liquidate_position":
		if err := checkDailyLossLimit(tc); err != nil { return "", err }
		var req struct { Symbol string `json:"symbol"`; Quantity float64 `json:"quantity"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Trading == nil { return "", fmt.Errorf("trading service not initialized") }
		positions, err := tc.Trading.GetPositions(ctx)
		if err != nil { return "", fmt.Errorf("failed to get positions: %w", err) }
		var found bool; var exitSide string
		for _, p := range positions { if p.Symbol == req.Symbol { found = true; if p.Qty > 0 { exitSide = "sell" } else { exitSide = "buy" }; break } }
		if !found { return "", fmt.Errorf("position for %s not found", req.Symbol) }
		order := &interfaces.Order{Symbol: req.Symbol, Qty: req.Quantity, Side: exitSide, Type: "market", TimeInForce: "day", Status: "pending", SubmittedAt: time.Now()}
		if tc.RequireDoubleConfirm && tc.Intent != nil {
			intentArgs := map[string]interface{}{"symbol": req.Symbol, "side": exitSide, "strategy": "DAY_TRADE", "entry_strategy": "market", "explicit_quantity": int(req.Quantity), "notes": "LIQUIDATION"}
			intentArgsBytes, _ := json.Marshal(intentArgs)
			id, err := tc.Intent.CreateIntent(IntentTypeManagedPosition, intentArgsBytes, req.Symbol, exitSide, req.Quantity, 0.0)
			if err != nil { return "", err }
			SendTelegramAlert(fmt.Sprintf("🚨 LIQUIDACIÓN PENDIENTE: %s. Entra al Hub para aprobarla.", req.Symbol))
			return fmt.Sprintf("Liquidation intent created (Intent ID: %s).", id), nil
		}
		res, err := tc.Trading.PlaceOrder(ctx, order); if err != nil { return "", fmt.Errorf("failed to place liquidation order: %w", err) }
		return fmt.Sprintf(`{"order_id": "%s", "message": "Liquidation order placed successfully"}`, res.OrderID), nil

	case "place_standalone_order":
		if err := checkDailyLossLimit(tc); err != nil { return "", err }
		var req struct { Symbol string `json:"symbol"`; Side string `json:"side"`; Qty float64 `json:"qty"`; OrderType string `json:"order_type"`; Price float64 `json:"price"`; BracketSL *float64 `json:"bracket_sl_price"`; BracketTP *float64 `json:"bracket_tp_price"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		side := strings.ToLower(req.Side)
		if side != "buy" && side != "sell" { return "", fmt.Errorf("side must be 'buy' or 'sell'") }
		order := &interfaces.Order{ Symbol: req.Symbol, Qty: req.Qty, Side: side, Type: strings.ToLower(req.OrderType), TimeInForce: "gtc", Status: "pending", SubmittedAt: time.Now() }
		if order.Type == "limit" { order.LimitPrice = &req.Price } else if order.Type == "stop" { order.StopLossPrice = &req.Price }
		if req.BracketSL != nil { order.StopLossPrice = req.BracketSL }
		if req.BracketTP != nil { order.TakeProfitPrice = req.BracketTP }
		if err := validateOrderDeterministic(ctx, tc, order, false); err != nil { return "", err }
		res, err := tc.Trading.PlaceOrder(ctx, order); if err != nil { return "", fmt.Errorf("failed to place standalone order: %w", err) }
		return fmt.Sprintf(`{"order_id": "%s", "message": "Standalone order placed successfully"}`, res.OrderID), nil

	case "cancel_order":
		var req struct { OrderID string `json:"order_id"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if err := tc.Trading.CancelOrder(ctx, req.OrderID); err != nil { return "", fmt.Errorf("failed to cancel order %s: %w", req.OrderID, err) }
		return fmt.Sprintf(`{"message": "Order %s cancelled successfully"}`, req.OrderID), nil

	case "place_options_order":
		if err := checkDailyLossLimit(tc); err != nil { return "", err }
		var req struct { Symbol string `json:"symbol"`; Action string `json:"action"`; Qty float64 `json:"qty"`; OrderType string `json:"order_type"`; LmtPrice float64 `json:"lmt_price"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		req.Action = strings.ToUpper(req.Action)
		if err := ValidateOptionsOrder(ctx, tc.Trading, req.Symbol, req.Action, req.Qty, req.OrderType, req.LmtPrice); err != nil {
			return "", err
		}
		var lmtPricePtr *float64
		if req.OrderType == "LMT" { lmtPricePtr = &req.LmtPrice }
		order := &interfaces.OptionsOrder{Symbol: req.Symbol, Qty: req.Qty, Side: req.Action, Type: req.OrderType, LimitPrice: lmtPricePtr}
		if tc.RequireDoubleConfirm && tc.Intent != nil {
			currentPrice := 0.0
			if tc.Data != nil {
				quoteCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				if quote, err := tc.Data.GetLatestQuote(quoteCtx, req.Symbol); err == nil && quote != nil { if quote.AskPrice > 0 { currentPrice = quote.AskPrice } else if quote.BidPrice > 0 { currentPrice = quote.BidPrice } }
				cancel()
			}
			id, err := tc.Intent.CreateIntent(IntentTypeOptionsOrder, args, req.Symbol, req.Action, req.Qty, currentPrice)
			if err != nil { return "", err }
			SendTelegramAlert(fmt.Sprintf("🚨 ORDEN DE OPCIONES PENDIENTE: %s %s. Entra al Hub para aprobarla.", req.Action, req.Symbol))
			return fmt.Sprintf("Options order intent created (Intent ID: %s).", id), nil
		}
		if err := requireFreshSpot(ctx, tc, req.Symbol); err != nil { return "", err }
		res, err := tc.Trading.PlaceOptionsOrder(ctx, order); if err != nil { return "", err }
		return fmt.Sprintf(`{"order_id": "%s"}`, res.OrderID), nil

	case "place_combo_order":
		if err := checkDailyLossLimit(tc); err != nil { return "", err }
		var req struct { Legs []struct { Symbol string `json:"symbol"`; Action string `json:"action"`; Ratio int `json:"ratio"` } `json:"legs"`; Action string `json:"action"`; Qty float64 `json:"qty"`; OrderType string `json:"order_type"`; LmtPrice float64 `json:"lmt_price"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Trading == nil { return "", fmt.Errorf("trading service not initialized") }
		if len(req.Legs) < 2 { return "", fmt.Errorf("combo order requires at least 2 legs") }
		lmtForCheck := 0.0
		if req.OrderType == "LMT" { lmtForCheck = req.LmtPrice }
		if err := ValidateComboOrder(tc.Trading, req.Action, req.Qty, req.OrderType, lmtForCheck); err != nil { return "", err }
		legs := make([]interfaces.ComboLeg, len(req.Legs))
		for i, l := range req.Legs { ratio := l.Ratio; if ratio <= 0 { ratio = 1 }; legs[i] = interfaces.ComboLeg{Symbol: l.Symbol, Action: strings.ToUpper(l.Action), Ratio: ratio} }
		var lmtPricePtr *float64
		if req.OrderType == "LMT" { lmtPricePtr = &req.LmtPrice }
		comboOrder := &interfaces.ComboOrder{Legs: legs, Action: req.Action, Qty: req.Qty, OrderType: req.OrderType, LimitPrice: lmtPricePtr}
		legDesc := make([]string, len(legs))
		for i, l := range legs { legDesc[i] = fmt.Sprintf("%s %s x%d", l.Action, l.Symbol, l.Ratio) }
		comboSymbol := fmt.Sprintf("COMBO[%s]", strings.Join(legDesc, " / "))
		if tc.RequireDoubleConfirm && tc.Intent != nil {
			currentPrice := 0.0
			if lmtPricePtr != nil { currentPrice = *lmtPricePtr }
			id, err := tc.Intent.CreateIntent(IntentTypeOptionsOrder, args, comboSymbol, req.Action, req.Qty, currentPrice)
			if err != nil { return "", err }
			SendTelegramAlert(fmt.Sprintf("🚨 ORDEN COMBO PENDIENTE: %s. Entra al Hub para aprobarla.", comboSymbol))
			return fmt.Sprintf("Combo order intent created (Intent ID: %s).", id), nil
		}
		if len(req.Legs) > 0 { if err := requireFreshSpot(ctx, tc, req.Legs[0].Symbol); err != nil { return "", err } }
		res, err := tc.Trading.PlaceComboOrder(ctx, comboOrder); if err != nil { return "", err }
		return fmt.Sprintf(`{"order_id": "%s"}`, res.OrderID), nil

	case "get_quote":
		var req struct { Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Data == nil { return "", fmt.Errorf("data service not initialized") }
		quoteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		quote, err := tc.Data.GetLatestQuote(quoteCtx, req.Symbol)
		cancel()
		if err != nil { return "", err }
		b, _ := json.Marshal(quote)
		var quoteMap map[string]interface{}
		if err := json.Unmarshal(b, &quoteMap); err == nil {
			ageSecs := int(time.Since(quote.Timestamp).Seconds())
			quoteMap["data_age_seconds"] = ageSecs
			quoteMap["timestamp"] = quote.Timestamp.Format(time.RFC3339)
			if quote.MarketDataType == 3 || quote.MarketDataType == 4 { quoteMap["data_type"] = "DELAYED"; quoteMap["disclaimer"] = fmt.Sprintf("⚠️ WARNING: THIS QUOTE IS DELAYED. DATA IS %d MINUTES OLD.", ageSecs/60) } else if quote.MarketDataType == 1 { quoteMap["data_type"] = "REAL-TIME" } else { quoteMap["data_type"] = fmt.Sprintf("UNKNOWN (Type %d)", quote.MarketDataType) }
			b, _ = json.Marshal(quoteMap)
		}
		return string(b), nil

	case "get_options_chain":
		var req struct { Symbol string `json:"symbol"`; Expiration string `json:"expiration"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Trading == nil { return "", fmt.Errorf("trading service not initialized") }
		exp, err := time.Parse("20060102", req.Expiration)
		if err != nil { exp = time.Now().Add(45 * 24 * time.Hour) }
		chain, err := tc.Trading.GetOptionsChain(ctx, req.Symbol, exp)
		if err != nil { return "", err }
		return formatOptionsChain(ctx, tc.Data, req.Symbol, chain), nil

	case "build_spread":
		var req struct { Strategy string `json:"strategy"`; Underlying string `json:"underlying"`; Expiration string `json:"expiration"`; ShortOTMPct float64 `json:"short_otm_pct"`; Width float64 `json:"width"`; RiskBudgetPct float64 `json:"risk_budget_pct"`; StrikeStep float64 `json:"strike_step"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Data == nil { return "", fmt.Errorf("market data not initialized") }
		if req.Underlying == "" { return "", fmt.Errorf("build_spread: underlying is required (US markets only)") }
		if u := strings.ToUpper(req.Underlying); strings.Contains(u, "ESTX50") || strings.Contains(u, "OESX") {
			return "", fmt.Errorf("build_spread: European markets are forbidden (US markets only)")
		}
		if _, err := time.Parse("20060102", req.Expiration); err != nil { return "", fmt.Errorf("build_spread: expiration must be YYYYMMDD: %v", err) }
		spotQ, err := tc.Data.GetLatestQuote(ctx, req.Underlying)
		if err != nil || spotQ == nil { return "", fmt.Errorf("build_spread: could not get %s spot: %v", req.Underlying, err) }
		spot := spotQ.BidPrice
		if spotQ.AskPrice > 0 && spotQ.BidPrice > 0 { spot = (spotQ.BidPrice + spotQ.AskPrice) / 2 }
		quote := func(sym string) (float64, bool) { qctx, qcancel := context.WithTimeout(ctx, 3*time.Second); defer qcancel(); q, qerr := tc.Data.GetLatestQuote(qctx, sym); if qerr != nil || q == nil || q.BidPrice <= 0 || q.AskPrice <= 0 { return 0, false }; return (q.BidPrice + q.AskPrice) / 2, true }
		var portfolioValue float64
		if acc, aerr := tc.Trading.GetAccount(ctx); aerr == nil && acc != nil { portfolioValue = acc.PortfolioValue }
		riskBudget := 0.0
		if req.RiskBudgetPct > 0 && portfolioValue > 0 { riskBudget = portfolioValue * req.RiskBudgetPct / 100 }
		mult := 100.0
		plan, err := BuildSpread(spot, quote, SpreadCriteria{Strategy: req.Strategy, UnderlyingSym: req.Underlying, Expiry: req.Expiration, ShortOTMPct: req.ShortOTMPct, WidthPts: req.Width, Multiplier: mult, RiskBudgetEUR: riskBudget, StrikeStep: req.StrikeStep})
		if err != nil { return "", fmt.Errorf("build_spread: %w", err) }
		verdict := CheckSpreadRisk(plan, portfolioValue, configstore.Get().Permissions)
		out := map[string]interface{}{"plan": plan, "risk_check": verdict, "suggested_limit_price": -plan.CreditPts, "how_to_execute": "If you approve and risk_check.pass is true, call place_combo_order with these exact legs and lmt_price = suggested_limit_price."}
		if spotQ.MarketDataType == 3 || spotQ.MarketDataType == 4 { out["data_warning"] = "⚠️ WARNING: SPOT PRICE FOR THIS SPREAD IS DELAYED." }
		b, _ := json.Marshal(out)
		log.Printf("[BUILD_SPREAD] %s qty=%d credit=$%.0f maxLoss=$%.0f pass=%v legs=%d", plan.Strategy, plan.Qty, plan.CreditEUR, plan.MaxLossEUR, verdict.Pass, len(plan.Legs))
		return string(b), nil

	case "set_heartbeat":
		var req struct { Seconds int `json:"seconds"`; Reason string `json:"reason"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Beat == nil { return "", fmt.Errorf("heartbeat control not available") }
		if req.Seconds < 60 { req.Seconds = 60 }
		if req.Seconds > 3600 { req.Seconds = 3600 }
		tc.Beat.SetNextInterval(time.Duration(req.Seconds)*time.Second, req.Reason)
		return fmt.Sprintf("Next heartbeat interval set to %d seconds.", req.Seconds), nil

	case "search_contract":
		var req struct { Symbol string `json:"symbol"`; SecType string `json:"sec_type"`; Exchange string `json:"exchange"`; Currency string `json:"currency"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Resolver == nil { return "", fmt.Errorf("contract resolver not available") }
		switch req.SecType { case "", "STK", "OPT", "FUT", "IND", "FOP", "WAR", "CFD": default: req.SecType = "" }
		searchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		results, err := tc.Resolver.Search(searchCtx, req.Symbol, req.SecType, req.Exchange, req.Currency)
		cancel()
		if err != nil { return "", fmt.Errorf("search failed: %w", err) }
		type contractResult struct { Symbol string `json:"symbol"`; SecType string `json:"sec_type"`; Exchange string `json:"exchange"`; Currency string `json:"currency"`; ConId int64 `json:"con_id"`; LongName string `json:"long_name,omitempty"`; LocalSymbol string `json:"local_symbol,omitempty"`; Expiry string `json:"expiry,omitempty"` }
		var out []contractResult
		for _, cd := range results { out = append(out, contractResult{Symbol: cd.Contract.Symbol, SecType: string(cd.Contract.SecType), Exchange: cd.Contract.Exchange, Currency: cd.Contract.Currency, ConId: cd.Contract.ConId, LongName: cd.LongName, LocalSymbol: cd.Contract.LocalSymbol, Expiry: cd.Contract.LastTradeDateOrContractMonth}) }
		if len(out) == 0 { return `{"results": [], "message": "No contracts found"}`, nil }
		b, _ := json.Marshal(map[string]interface{}{"results": out, "count": len(out)})
		return string(b), nil

	case "jim_rogers":
		var req struct { TargetAgentID string `json:"target_agent_id"`; Prompt string `json:"prompt"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		agent := configstore.GetAgentByID(req.TargetAgentID)
		if agent == nil || agent.CustomSystemPrompt == "" { return "", fmt.Errorf("agent %s not found", req.TargetAgentID) }
		sysPrompt := agent.CustomSystemPrompt
		if tc.LLM == nil { return "", fmt.Errorf("llm provider not initialized") }
		userPrompt := req.Prompt
		if tc.Beat != nil { if snap := tc.Beat.CurrentSnapshot(); snap != "" { userPrompt = snap + "\n\nIMPORTANT: Base your analysis ONLY on the VERIFIED LIVE DATA above and the request below. Do NOT invent or assume any spot price, implied volatility, strike, or Greek. If you need a value that is not shown, state explicitly that it must be fetched — do not guess.\n\n" + req.Prompt } }
		messages := []interfaces.LLMMessage{{Role: "system", Content: sysPrompt}, {Role: "user", Content: userPrompt}}
		resp, err := tc.LLM.GenerateResponse(ctx, messages, nil)
		if err != nil { return "", fmt.Errorf("failed to consult agent: %v", err) }
		return resp.Content, nil

	case "create_suggestion":
		var req CreateSuggestionRequest
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Suggestion == nil { return "", fmt.Errorf("suggestion manager not available") }
		beatID := currentBeatId
		id, err := tc.Suggestion.CreateSuggestion(req, beatID, "")
		if err != nil { return "", err }
		return fmt.Sprintf(`{"suggestion_id": "%s", "status": "PENDING", "message": "Trade suggestion created."}`, id), nil

	case "get_mnq_dashboard":
		var req struct { Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Data == nil { return "", fmt.Errorf("data service not initialized") }
		targetSym := "QQQ"
		
		quoteCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		quote, err := tc.Data.GetLatestQuote(quoteCtx, targetSym)
		cancel()
		
		price := 0.0
		if err == nil && quote != nil { if quote.AskPrice > 0 { price = quote.AskPrice } else if quote.BidPrice > 0 { price = quote.BidPrice } }
		
		histCtx, histCancel := context.WithTimeout(ctx, 5*time.Second)
		dayBars, err := tc.Data.GetHistoricalBars(histCtx, targetSym, time.Now().Add(-48*time.Hour), time.Now(), "1Day")
		histCancel()
		
		var pdh, pdl float64
		if err == nil && len(dayBars) >= 2 {
			pdh = dayBars[len(dayBars)-2].High; pdl = dayBars[len(dayBars)-2].Low
			if price == 0 { price = dayBars[len(dayBars)-1].Close }
		}
		
		ibCtx, ibCancel := context.WithTimeout(ctx, 5*time.Second)
		minBars, err := tc.Data.GetHistoricalBars(ibCtx, targetSym, time.Now().Add(-24*time.Hour), time.Now(), "5Min")
		ibCancel()
		
		var ibHigh, ibLow float64
		if err == nil {
			for i, b := range minBars { if i >= 12 { break }; if b.High > ibHigh { ibHigh = b.High }; if ibLow == 0 || b.Low < ibLow { ibLow = b.Low } }
		}
		if price == 0 && ibHigh > 0 && ibLow > 0 { price = (ibHigh + ibLow) / 2 }
		
		out := map[string]interface{}{ "symbol": req.Symbol, "proxy_used": "QQQ", "price": price, "pdh": pdh, "pdl": pdl, "ib_high": ibHigh, "ib_low": ibLow, "data_age_seconds": 0, "market_data_type": 1 }
		if quote != nil {
			out["data_age_seconds"] = int(time.Since(quote.Timestamp).Seconds())
			out["market_data_type"] = quote.MarketDataType
			if quote.MarketDataType == 3 || quote.MarketDataType == 4 { out["data_warning"] = "⚠️ WARNING: DELAYED DATA." }
		}
		b, _ := json.Marshal(out); return string(b), nil

	case "get_market_regime":
		var req struct { Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Data == nil { return "", fmt.Errorf("data service not initialized") }
		targetSymbol := "QQQ"
		ctxBars, cancel := context.WithTimeout(ctx, 10*time.Second)
		bars, err := tc.Data.GetHistoricalBars(ctxBars, targetSymbol, time.Now().Add(-14*24*time.Hour), time.Now(), "1Day")
		cancel()
		if err != nil || len(bars) < 14 {
			ctxBars, cancel = context.WithTimeout(ctx, 10*time.Second)
			bars, err = tc.Data.GetHistoricalBars(ctxBars, targetSymbol, time.Now().Add(-7*24*time.Hour), time.Now(), "1Hour")
			cancel()
			if err != nil { return "", fmt.Errorf("failed to get bars for regime: %w", err) }
			if len(bars) < 20 { return "", fmt.Errorf("not enough data to determine regime") }
		}
		var trSum float64
		startIdx := len(bars) - 14; if startIdx < 1 { startIdx = 1 }
		for i := startIdx; i < len(bars); i++ {
			high, low, prevClose := bars[i].High, bars[i].Low, bars[i-1].Close
			tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
			trSum += tr
		}
		atr := trSum / 14
		var smaSum float64
		smaLen := 20; if len(bars) < 20 { smaLen = len(bars) }
		for i := len(bars) - smaLen; i < len(bars); i++ { smaSum += bars[i].Close }
		sma := smaSum / float64(smaLen)
		lastClose := bars[len(bars)-1].Close
		vix := 0.0
		for _, s := range []string{"VIX", "VXX", "UVXY"} {
			vix = func() float64 {
				vixCtx, vixCancel := context.WithTimeout(ctx, 2*time.Second)
				defer vixCancel()
				q, err := tc.Data.GetLatestQuote(vixCtx, s)
				if err != nil || q == nil {
					return 0
				}
				if q.AskPrice > 0 {
					return q.AskPrice
				}
				return q.BidPrice
			}()
			if vix > 0 {
				break
			}
		}
		regime := "RANGE"
		if lastClose > sma*1.01 { regime = "TREND_UP" } else if lastClose < sma*0.99 { regime = "TREND_DOWN" }
		if atr > lastClose*0.015 { regime = "HIGH_VOLATILITY" }
		out := map[string]interface{}{ "requested_symbol": req.Symbol, "proxy_used": "QQQ", "regime": regime, "atr_daily": math.Round(atr*100)/100, "atr_percent": math.Round((atr/lastClose)*10000)/100, "sma_20": math.Round(sma*100)/100, "last_close": lastClose, "vix": math.Round(vix*100)/100 }
		b, _ := json.Marshal(out); return string(b), nil

	case "calculate_mnq_setup":
		var req struct { Symbol string `json:"symbol"`; Direction string `json:"direction"`; SetupType string `json:"setup_type"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Data == nil { return "", fmt.Errorf("data service not initialized") }
		dashArgs, _ := json.Marshal(map[string]string{"symbol": req.Symbol})
		dashStr, err := HandleToolCall(ctx, "get_mnq_dashboard", dashArgs, tc)
		if err != nil { return "", fmt.Errorf("failed to get dashboard for setup: %w", err) }
		var dash map[string]interface{}; json.Unmarshal([]byte(dashStr), &dash)
		price, _ := dash["price"].(float64); ibHigh, _ := dash["ib_high"].(float64); ibLow, _ := dash["ib_low"].(float64); ibMid := (ibHigh + ibLow) / 2; proxyUsed, _ := dash["proxy_used"].(string)
		if price <= 0 || ibHigh <= 0 { return "", fmt.Errorf("cannot calculate setup: missing live price or IB levels") }
		direction := strings.ToLower(req.Direction); setupType := strings.ToLower(req.SetupType); if setupType == "" { setupType = "breakout" }
		var entry, sl, tp float64
		if setupType == "breakout" {
			if direction == "long" { entry = ibHigh; sl = ibMid; tp = ibHigh + (ibHigh - ibLow) * 0.5 } else { entry = ibLow; sl = ibMid; tp = ibLow - (ibHigh - ibLow) * 0.5 }
		} else if setupType == "vwap_reclaim" {
			if direction == "long" { entry = price; sl = price - 10; tp = price + 20 } else { entry = price; sl = price + 10; tp = price - 20 }
		}
		out := map[string]interface{}{ "symbol": req.Symbol, "proxy_used": proxyUsed, "direction": direction, "setup_type": setupType, "entry": entry, "stop_loss": sl, "take_profit": tp, "current_price": price, "risk_reward": math.Abs(tp-entry) / math.Abs(entry-sl) }
		b, _ := json.Marshal(out); return string(b), nil

	case "get_correlated_assets":
		if tc.Data == nil { return "", fmt.Errorf("data service not initialized") }
		mnqPrice := 0.0; qqqCtx, qqqCancel := context.WithTimeout(ctx, 3*time.Second)
		if q, err := tc.Data.GetLatestQuote(qqqCtx, "QQQ"); err == nil && q != nil { if q.AskPrice > 0 { mnqPrice = q.AskPrice } else if q.BidPrice > 0 { mnqPrice = q.BidPrice } }
		qqqCancel()
		nqPrice := 0.0; nqCtx, nqCancel := context.WithTimeout(ctx, 3*time.Second)
		if q, err := tc.Data.GetLatestQuote(nqCtx, "NQZ6"); err == nil && q != nil { if q.AskPrice > 0 { nqPrice = q.AskPrice } else if q.BidPrice > 0 { nqPrice = q.BidPrice } }
		nqCancel()
		if mnqPrice == 0 && nqPrice == 0 { return `{"status": "no_data", "message": "No se pudo obtener cotización."}`, nil }
		confirmation := "NEUTRAL"; if mnqPrice > 0 && nqPrice > 0 { confirmation = "DATA_AVAILABLE" }
		out := map[string]interface{}{ "mnq_proxy_price": mnqPrice, "nq_price": nqPrice, "confirmation": confirmation, "note": "Cálculo simplificado." }
		b, _ := json.Marshal(out); return string(b), nil

	case "get_order_flow_imbalance":
		var req struct { Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if tc.Data == nil { return "", fmt.Errorf("data service not initialized") }
		targetSym := "QQQ"; ctxBars, cancel := context.WithTimeout(ctx, 5*time.Second)
		minBars, err := tc.Data.GetHistoricalBars(ctxBars, targetSym, time.Now().Add(-2*time.Hour), time.Now(), "1Min")
		cancel()
		if err != nil || len(minBars) < 10 { return "", fmt.Errorf("failed to get 1min bars") }
		buyVol := 0.0; sellVol := 0.0; limit := 20; if len(minBars) < limit { limit = len(minBars) }
		for i := len(minBars)-limit; i < len(minBars); i++ {
			bar := minBars[i]; if (bar.Close - bar.Low) > (bar.High - bar.Close) { buyVol += float64(bar.Volume) } else { sellVol += float64(bar.Volume) }
		}
		totalVol := buyVol + sellVol; imbalance := 0.0; if totalVol > 0 { imbalance = (buyVol - sellVol) / totalVol }
		pressure := "NEUTRAL"; if imbalance > 0.15 { pressure = "BUYING_PRESSURE" } else if imbalance < -0.15 { pressure = "SELLING_PRESSURE" }
		out := map[string]interface{}{ "symbol": req.Symbol, "proxy_used": "QQQ 1Min Volume Imbalance", "imbalance_score": math.Round(imbalance*100)/100, "pressure": pressure, "note": "Mathematical proxy." }
		b, _ := json.Marshal(out); return string(b), nil

	case "get_calendar_events":
		out := map[string]interface{}{ "status": "simulated", "message": "No high-impact events found.", "events": []string{} }
		b, _ := json.Marshal(out); return string(b), nil

	case "get_social_sentiment":
		var req struct { Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		ns := NewNewsService(); score, source, err := ns.GetSentiment(req.Symbol)
		if err != nil { return "", fmt.Errorf("failed to get sentiment: %w", err) }
		label := "NEUTRAL"; if score > 0.2 { label = "BULLISH" } else if score < -0.2 { label = "BEARISH" }
		out := map[string]interface{}{ "symbol": req.Symbol, "score": math.Round(score*100)/100, "label": label, "source": source, "message": "Quantitative sentiment." }
		b, _ := json.Marshal(out); return string(b), nil

	case "get_insider_activity":
		var req struct { Symbol string `json:"symbol"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		ns := NewNewsService(); result, err := ns.GetInsiderTrading(req.Symbol)
		if err != nil { return fmt.Sprintf(`{"status": "error", "message": "Insider data unavailable: %v"}`, err), nil }
		return fmt.Sprintf(`{"status": "success", "data": "%s"}`, result), nil

	case "save_memory":
		var req struct { Content string `json:"content"`; Tags []string `json:"tags"` }
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if err := SaveMemory(req.Content, req.Tags); err != nil { return "", fmt.Errorf("failed to save memory: %w", err) }
		return `{"status": "success", "message": "Memory saved successfully"}`, nil

	case "get_memory":
		var req struct {
			Limit      int     `json:"limit"`
			Context    string  `json:"context"`
			Symbol     string  `json:"symbol"`
			Setup      string  `json:"setup"`
			RSI        float64 `json:"rsi"`
			ATRPercent float64 `json:"atr_percent"`
		}
		if err := json.Unmarshal(args, &req); err != nil { return "", err }
		if req.Limit <= 0 { req.Limit = 10 }
		store, err := GetMemory(req.Limit); if err != nil { return "", fmt.Errorf("failed to load memory: %w", err) }
		similar := GetSimilarTrades(TradeQuery{
			Symbol: req.Symbol, Setup: req.Setup, Context: req.Context,
			RSI: req.RSI, ATRPercent: req.ATRPercent,
		}, req.Limit)
		out := map[string]interface{}{
			"lessons":       store.Lessons,
			"recent_trades": store.Trades,
			"similar_trades": similar,
		}
		b, _ := json.Marshal(out); return string(b), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

type compactOption struct { Sym string `json:"sym"`; Type string `json:"type"`; K float64 `json:"strike"`; Bid float64 `json:"bid"`; Ask float64 `json:"ask"`; Delta float64 `json:"delta,omitempty"`; IV float64 `json:"iv,omitempty"`; OI int64 `json:"oi,omitempty"`; DTE int `json:"dte,omitempty"` }

func formatOptionsChain(ctx context.Context, data interfaces.DataService, underlying string, chain []*interfaces.OptionContract) string {
	const strikeWindow = 0.15
	const maxContracts = 60
	spot := 0.0
	if data != nil {
		qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if q, err := data.GetLatestQuote(qctx, underlying); err == nil && q != nil {
			switch { case q.AskPrice > 0 && q.BidPrice > 0: spot = (q.AskPrice + q.BidPrice) / 2; case q.AskPrice > 0: spot = q.AskPrice; case q.BidPrice > 0: spot = q.BidPrice }
		}
		cancel()
	}
	out := make([]compactOption, 0, len(chain))
	for _, c := range chain {
		if c == nil { continue }
		if spot > 0 && (c.StrikePrice < spot*(1-strikeWindow) || c.StrikePrice > spot*(1+strikeWindow)) { continue }
		t := "C"
		if c.ContractType == "put" || c.ContractType == "P" || c.ContractType == "p" { t = "P" }
		out = append(out, compactOption{Sym: c.Symbol, Type: t, K: c.StrikePrice, Bid: c.Bid, Ask: c.Ask, Delta: c.Delta, IV: c.ImpliedVolatility, OI: c.OpenInterest, DTE: c.DTE})
	}
	if spot <= 0 && len(out) > maxContracts { out = out[:maxContracts] }
	payload := map[string]interface{}{"underlying": underlying, "spot": spot, "returned": len(out), "total": len(chain), "note": "near-the-money window; fields projected to conserve context", "options": out}
	b, _ := json.Marshal(payload)
	return string(b)
}