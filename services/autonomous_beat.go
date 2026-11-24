package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"prophet-trader/config"
	"prophet-trader/configstore"
	"prophet-trader/interfaces"
	"prophet-trader/tws"

	"github.com/sirupsen/logrus"
)

type AutonomousBeatConfig struct {
	Interval           time.Duration
	MaxDailyExecutions int
	LLMPollingEnabled  bool
	LLMPollingInterval time.Duration
}

type AutonomousBeat struct {
	data    interfaces.DataService
	trading interfaces.TradingService
	pm      *PositionManager
	logger  *logrus.Logger
	cfg     AutonomousBeatConfig

	llm                  interfaces.LLMProvider
	intentManager        *IntentManager
	requireDoubleConfirm bool
	resolver             *tws.ContractResolver
	suggestionManager    *SuggestionManager
	tradingMode          config.TradingMode

	mu           sync.Mutex
	isRunning    bool
	inTick       bool
	cancel       context.CancelFunc
	messageQueue []beatMessage
	steeringCh   chan beatMessage
	triggerCh    chan struct{}
	lastPollTime time.Time

	overrideInterval time.Duration
	overrideOnce     bool
	beatCounter      int64
	currentSnapshot  string
	newsCache        *NewsCache

	totalBeats int64
	toolCalls  int64
	errors     int64
	paused     bool
}

type beatMessage struct {
	Text     string
	IsDirect bool
}

func NewAutonomousBeat(data interfaces.DataService, pm *PositionManager, trading interfaces.TradingService, logger *logrus.Logger, cfg AutonomousBeatConfig, intentManager *IntentManager, requireDoubleConfirm bool, suggestionManager *SuggestionManager, tradingMode config.TradingMode) *AutonomousBeat {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.MaxDailyExecutions <= 0 {
		cfg.MaxDailyExecutions = 3
	}

	primary, err := NewAnthropicClient()
	if err != nil {
		logger.WithError(err).Warn("Failed to initialize primary DeepSeek client")
	}
	gemini, geminiErr := NewFallbackGeminiClient()
	if geminiErr != nil {
		logger.WithError(geminiErr).Warn("Failed to initialize Gemini fallback client")
	}
	claude, claudeErr := NewFallbackClaudeClient()
	if claudeErr != nil {
		logger.WithError(claudeErr).Warn("Failed to initialize Claude fallback client")
	}

	llm := NewMultiLLMClient(primary, gemini, claude)

	return &AutonomousBeat{
		data:                 data,
		trading:              trading,
		pm:                   pm,
		logger:               logger,
		cfg:                  cfg,
		llm:                  llm,
		intentManager:        intentManager,
		requireDoubleConfirm: requireDoubleConfirm,
		suggestionManager:    suggestionManager,
		tradingMode:          tradingMode,
	}
}

func (b *AutonomousBeat) SetResolver(r *tws.ContractResolver) { b.resolver = r }

func (b *AutonomousBeat) SetNextInterval(d time.Duration, reason string) {
	b.mu.Lock()
	b.overrideInterval = d
	b.overrideOnce = true
	b.mu.Unlock()
	b.logger.WithField("interval", d).WithField("reason", reason).Info("[BEAT] Next interval override scheduled")
}

func (b *AutonomousBeat) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.isRunning {
		return fmt.Errorf("already running")
	}
	if b.llm == nil {
		return fmt.Errorf("LLM provider not initialized")
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.isRunning = true

	go b.Run(ctx)
	return nil
}

func (b *AutonomousBeat) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.isRunning {
		return fmt.Errorf("not running")
	}
	b.cancel()
	b.isRunning = false
	return nil
}

func (b *AutonomousBeat) IsRunning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isRunning
}

func (b *AutonomousBeat) Interval() time.Duration { return b.cfg.Interval }

type BeatStats struct {
	TotalBeats int64 `json:"totalBeats"`
	ToolCalls  int64 `json:"toolCalls"`
	Errors     int64 `json:"errors"`
}

func (b *AutonomousBeat) Stats() BeatStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BeatStats{TotalBeats: b.totalBeats, ToolCalls: b.toolCalls, Errors: b.errors}
}

func (b *AutonomousBeat) IsPaused() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.paused
}

func (b *AutonomousBeat) BeatCount() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.beatCounter
}

func (b *AutonomousBeat) Run(ctx context.Context) {
	b.logger.WithFields(logrus.Fields{"interval": b.cfg.Interval}).Warn("[BEAT] Native AI autonomous beat started")
	b.tick(ctx)
	t := time.NewTicker(b.cfg.Interval)
	defer t.Stop()
	b.triggerCh = make(chan struct{}, 1)
	b.steeringCh = make(chan beatMessage, 8)
	for {
		select {
		case <-ctx.Done():
			b.logger.Info("AI beat loop canceled")
			return
		case <-t.C:
			b.tick(ctx)
			if b.overrideOnce {
				t.Reset(b.overrideInterval)
				b.overrideOnce = false
			}
		case <-b.triggerCh:
			b.tick(ctx)
			if b.overrideOnce {
				t.Reset(b.overrideInterval)
				b.overrideOnce = false
			}
		}
	}
}

func (b *AutonomousBeat) InjectMessage(msg string) { b.injectBeatMessage(beatMessage{Text: msg}) }
func (b *AutonomousBeat) InjectDirectMessage(msg string) {
	b.injectBeatMessage(beatMessage{Text: msg, IsDirect: true})
}

func (b *AutonomousBeat) injectBeatMessage(msg beatMessage) {
	b.mu.Lock()
	if b.inTick && b.steeringCh != nil {
		b.mu.Unlock()
		select {
		case b.steeringCh <- msg:
		default:
		}
		return
	}
	b.messageQueue = append(b.messageQueue, msg)
	b.mu.Unlock()
	select {
	case b.triggerCh <- struct{}{}:
	default:
	}
}

func (b *AutonomousBeat) tick(ctx context.Context) {
	b.mu.Lock()
	b.inTick = true
	b.beatCounter++
	b.totalBeats++
	currentBeatId = b.beatCounter
	beatNum := b.beatCounter
	var pending []beatMessage
	if len(b.messageQueue) > 0 {
		pending = b.messageQueue
		b.messageQueue = nil
	}
	b.mu.Unlock()

	beatToolCalls := 0
	beatErrors := 0
	beatStartTime := time.Now()
	appendBeatEvent("beat_start", beatNum, beatStartTime, 0, 0)

	defer func() {
		appendBeatEvent("beat_end", beatNum, beatStartTime, beatToolCalls, beatErrors)
		b.mu.Lock()
		b.inTick = false
		b.mu.Unlock()
	}()

	if len(pending) == 0 {
		if !config.CurrentPollingEnabled() {
			return
		}
		if time.Since(b.lastPollTime) < config.CurrentPollingInterval() {
			return
		}
		if b.portfolioIsFlat(ctx) {
			b.lastPollTime = time.Now()
			return
		}
		pending = append(pending, beatMessage{Text: "Automated system trigger: Please review my current active positions and suggest any strategic adjustments or exit strategies based on current market data. Use the get_positions tool."})
		b.lastPollTime = time.Now()
	}

	b.logger.Info("[BEAT] AI Heartbeat executing for user prompt/automated review...")
	snapshot := b.buildVerifiedSnapshot(ctx)
	b.mu.Lock()
	b.currentSnapshot = snapshot
	b.mu.Unlock()

	mode := config.CurrentTradingMode()
	activeAgent := configstore.ActiveAgentName()

	strategiesCfgForPrompt := LoadStrategiesConfig()

	var systemPrompt string
	if rulesPrompt := strategiesCfgForPrompt.TradingRulesPrompt(); rulesPrompt != "" {
		systemPrompt = rulesPrompt
	} else {
		systemPrompt = "You are OpenProphet, an autonomous trading agent."
	}

	perms := configstore.Get().Permissions
	systemPrompt += fmt.Sprintf("\n\nACTIVE RISK LIMITS (READ FROM CONFIG):\n- Max Daily Loss (Kill Switch): -%.0f%% of NAV\n- Max Position Size: %.0f%% of NAV\n- Max Deployed Capital: %.0f%% of NAV\n- Allow 0DTE Options: %v", perms.MaxDailyLoss, perms.MaxPositionPct, perms.MaxDeployedPct, perms.Allow0DTE)

	activeIDs := ActiveStrategyIDs(strategiesCfgForPrompt)
	activeList := "none"
	if len(activeIDs) > 0 {
		activeList = strings.Join(activeIDs, ", ")
	}
	systemPrompt += fmt.Sprintf("\n\nACTIVE STRATEGIES: %s", activeList)

	// Focus the analysis on the configured beat watchlist (data/strategies.json).
	if syms := strategiesCfgForPrompt.BeatSymbols(); len(syms) > 0 {
		systemPrompt += fmt.Sprintf("\n\nAUTOMATIC WATCHLIST: %s\n- Focus your analysis ONLY on these instruments.\n- ESTX50 and any European market are FORBIDDEN.\n- Being on the watchlist does NOT grant trading permission: the whitelist below is still enforced by code.", strings.Join(syms, ", "))
	}

	systemPrompt += `
\n\nSTRATEGY EXECUTION RULES (CRITICAL):
1. You MUST call ` + "`get_active_strategies`" + ` at the beginning of your analysis to see which strategies are currently enabled.
2. For each active strategy (see ACTIVE STRATEGIES above), you MUST call ` + "`check_strategy`" + ` with the strategy_id and the symbol being analysed.
3. If a strategy returns ` + "`setup_found: true`" + `, review the entry, stop loss, take profit, and R:R. If it aligns with your risk management, execute it using ` + "`place_managed_position`" + `.
4. ALWAYS set the ` + "`strategy`" + ` parameter in ` + "`place_managed_position`" + ` to the ID of the strategy that triggered the setup (the one that fired).
5. If multiple active strategies align, this is a high-confluence A+ setup. Mention the confluence in the ` + "`notes`" + ` parameter.
`

	// [C2 FIX] Inyectar whitelist/blacklist de las estrategias activas al LLM.
	if strategiesCfgForPrompt != nil && len(activeIDs) > 0 {
		systemPrompt += "\n\nWHITELIST/BLACKLIST (CRITICAL - ENFORCED BY CODE):\n"
		for _, stratID := range activeIDs {
			if stratMap, ok := strategiesCfgForPrompt.Strategies[stratID].(map[string]interface{}); ok {
				wlRaw, _ := stratMap["whitelist_symbols"].([]interface{})
				blRaw, _ := stratMap["blacklist_symbols"].([]interface{})
				if len(wlRaw) > 0 || len(blRaw) > 0 {
					systemPrompt += fmt.Sprintf("- Strategy %q: ", stratID)
					if len(wlRaw) > 0 {
						syms := make([]string, 0, len(wlRaw))
						for _, s := range wlRaw {
							syms = append(syms, fmt.Sprint(s))
						}
						systemPrompt += "WHITELIST=[" + strings.Join(syms, ", ") + "] "
					}
					if len(blRaw) > 0 {
						syms := make([]string, 0, len(blRaw))
						for _, s := range blRaw {
							syms = append(syms, fmt.Sprint(s))
						}
						systemPrompt += "BLACKLIST=[" + strings.Join(syms, ", ") + "]"
					}
					systemPrompt += "\n"
				}
			}
		}
		systemPrompt += `
RULES:
- Only symbols in the WHITELIST can be traded.
- BLACKLIST symbols are ABSOLUTELY FORBIDDEN - the code will reject any order.
- If check_strategy returns blocked_by=whitelist, do NOT retry. Move on.
- Do not "reason around" the whitelist. It is enforced by code, not a suggestion.
`
	}

	systemPrompt += "\n\nCRITICAL CONTEXT:\n- Timezone: CST (Costa Rica Time, UTC-6)\n- Base Currency: USD ($)\nEnsure all price values, portfolio calculations, and temporal reasoning naturally default to US Dollars and Costa Rica Time (CST) without requiring manual prompting."

	userText := fmt.Sprintf("Current Time: %s\n\n", time.Now().Format(time.RFC3339))
	userText += snapshot + "\n\n"
	hasDirect := false
	for _, msg := range pending {
		if msg.IsDirect {
			hasDirect = true
		}
	}
	if hasDirect {
		systemPrompt += "\n\nDIRECT USER ORDER: The user has given a direct instruction below. Consult sub-agents (The Risk Manager, The Strategist) for advisory context only — do NOT allow them to veto or block the user's explicit request. Execute the user's instruction."
	}
	userText += "User terminal instructions:\n"
	for _, msg := range pending {
		userText += "- " + msg.Text + "\n"
	}

	switch mode {
	case config.TradingModeSuggest:
		systemPrompt += "\n\nYOU ARE IN SUGGEST MODE. You CANNOT place orders. Instead, analyze the portfolio and market conditions, then use the create_suggestion tool to propose trades for human review."
	case config.TradingModeAutonomous:
		systemPrompt += "\n\nYOU ARE IN AUTONOMOUS MODE. When your analysis and risk checks approve a trade, EXECUTE IT DIRECTLY using the order tools."
	}

	messages := []interfaces.LLMMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userText}}
	tools := BuildAgentTools(mode)

	concluded := false
	jimRogersCalls := 0
	const maxTurns = 10
	for i := 0; i < maxTurns; i++ {
		if ctx.Err() != nil {
			return
		}
		for {
			select {
			case steer := <-b.steeringCh:
				prefix := "[USER MESSAGE] "
				if steer.IsDirect {
					prefix = "[USER STEERING] "
				}
				messages = append(messages, interfaces.LLMMessage{Role: "user", Content: prefix + steer.Text})
			default:
				goto drained
			}
		}
	drained:

		resp, err := b.llm.GenerateResponse(ctx, messages, tools)
		if err != nil {
			b.logger.WithError(err).Error("[BEAT] LLM API error")
			appendJSONToBotLogAgent("agent_text", fmt.Sprintf("Error interacting with LLM: %v", err), activeAgent)
			return
		}

		messages = append(messages, interfaces.LLMMessage{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls, RawContent: resp.RawContent})

		if resp.Content != "" {
			b.logger.WithField("ai_thought", resp.Content).Info("[BEAT] AI Log")
			appendJSONToBotLogAgent("agent_text", resp.Content, activeAgent)
		}

		if len(resp.ToolCalls) == 0 {
			concluded = true
			break
		}

		for _, toolCall := range resp.ToolCalls {
			if toolCall.Name == "jim_rogers" {
				jimRogersCalls++
				if jimRogersCalls > maxJimRogersPerCycle {
					messages = append(messages, interfaces.LLMMessage{Role: "user", Content: "You have reached the limit for consulting other agents (jim_rogers) this cycle.", ToolResultID: toolCall.ID, ToolResultName: toolCall.Name})
					continue
				}
			}

			b.mu.Lock()
			b.toolCalls++
			b.mu.Unlock()
			beatToolCalls++
			appendToolToBotLog(toolCall.Name, toolCall.Arguments)

			toolCtx, toolCancel := context.WithTimeout(ctx, 90*time.Second)
			resStr, toolErr := HandleToolCall(toolCtx, toolCall.Name, toolCall.Arguments, &ToolContext{
				Data: b.data, PM: b.pm, Trading: b.trading, LLM: b.llm, Intent: b.intentManager, Beat: b, Resolver: b.resolver,
				RequireDoubleConfirm: mode.RequiresDoubleConfirm(), Suggestion: b.suggestionManager, TradingMode: mode,
			})
			toolCancel()

			var resultMsg string
			if toolErr != nil {
				resultMsg = fmt.Sprintf("Error: %s", toolErr.Error())
				b.mu.Lock()
				b.errors++
				b.mu.Unlock()
				beatErrors++
			} else {
				resultMsg = resStr
				if toolCall.Name == "jim_rogers" {
					var jr struct {
						TargetAgentID string `json:"target_agent_id"`
					}
					json.Unmarshal(toolCall.Arguments, &jr)
					appendJSONToBotLogAgent("agent_text", resStr, configstore.AgentName(jr.TargetAgentID))
				}
			}
			messages = append(messages, interfaces.LLMMessage{Role: "user", Content: truncateForHistory(resultMsg), ToolResultID: toolCall.ID, ToolResultName: toolCall.Name})
		}
	}

	if !concluded {
		messages = append(messages, interfaces.LLMMessage{Role: "user", Content: "You have reached your tool-use limit for this cycle. Do NOT call any more tools. Based only on the data you have already gathered, give the user your final recommendation now."})
		summaryCtx, summaryCancel := context.WithTimeout(ctx, 60*time.Second)
		resp, err := b.llm.GenerateResponse(summaryCtx, messages, nil)
		summaryCancel()
		if err == nil && resp.Content != "" {
			appendJSONToBotLogAgent("agent_text", resp.Content, activeAgent)
		}
	}
}

const maxToolResultChars = 6000
const maxJimRogersPerCycle = 2

func (b *AutonomousBeat) portfolioIsFlat(ctx context.Context) bool {
	if b.trading == nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pos, err := b.trading.GetPositions(cctx)
	if err != nil {
		return false
	}
	opos, err := b.trading.ListOptionsPositions(cctx)
	if err != nil {
		return false
	}
	return len(pos) == 0 && len(opos) == 0
}

func truncateForHistory(s string) string {
	if len(s) <= maxToolResultChars {
		return s
	}
	return s[:maxToolResultChars] + fmt.Sprintf("\n...[truncated %d chars]", len(s)-maxToolResultChars)
}

var currentBeatId int64

func appendJSONToBotLog(event string, text string) {
	appendJSONToBotLogAgent(event, text, "")
}

func appendJSONToBotLogAgent(event string, text string, agent string) {
	// [NUEVO] Agregamos el timestamp y la fecha del dia.
	now := time.Now()
	payload := map[string]interface{}{
		"text": text,
		"beatId": currentBeatId,
		"timestamp": now.Format("15:04:05"),
		"date": now.Format("2006-01-02"),
	}
	if agent != "" {
		payload["agent"] = agent
	}
	wrapper := map[string]interface{}{"event": event, "data": payload}
	b, _ := json.Marshal(wrapper)
	if f, err := os.OpenFile("bot.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		f.WriteString(string(b) + "\n")
		f.Close()
	}
	if Hub != nil {
		Hub.Broadcast(event, payload)
	}
}

func appendBeatEvent(event string, beatNum int64, startTime time.Time, toolCalls, errors int) {
	payload := map[string]interface{}{"beat": beatNum, "beatId": beatNum, "timestamp": startTime.Format(time.RFC3339), "toolCalls": toolCalls, "errors": errors}
	wrapper := map[string]interface{}{"event": event, "data": payload}
	b, _ := json.Marshal(wrapper)
	if f, err := os.OpenFile("bot.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		f.WriteString(string(b) + "\n")
		f.Close()
	}
	if Hub != nil {
		Hub.Broadcast(event, payload)
	}
}

func appendToolToBotLog(toolName string, args []byte) {
	var argsMap map[string]interface{}
	json.Unmarshal(args, &argsMap)
	now := time.Now()
	payload := map[string]interface{}{
		"tool": toolName,
		"args": argsMap,
		"beatId": currentBeatId,
		"timestamp": now.Format("15:04:05"),
		"date": now.Format("2006-01-02"),
	}
	wrapper := map[string]interface{}{"event": "agent_tool", "data": payload}
	b, _ := json.Marshal(wrapper)
	if f, err := os.OpenFile("bot.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		f.WriteString(string(b) + "\n")
		f.Close()
	}
	if Hub != nil {
		Hub.Broadcast("agent_tool", payload)
	}
}