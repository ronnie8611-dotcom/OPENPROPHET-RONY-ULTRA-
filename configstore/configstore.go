package configstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Agent struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	SystemPromptTemplate string            `json:"systemPromptTemplate"`
	CustomSystemPrompt   string            `json:"customSystemPrompt"`
	StrategyID           *string           `json:"strategyId"`
	Model                string            `json:"model"`
	HeartbeatOverrides   map[string]int    `json:"heartbeatOverrides"`
	CreatedAt            string            `json:"createdAt"`
	UpdatedAt            string            `json:"updatedAt,omitempty"`
}

type Strategy struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Engine      string  `json:"engine,omitempty"`
	RulesFile   *string `json:"rulesFile"`
	CustomRules *string `json:"customRules"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt,omitempty"`
}

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type SlackNotifyOn struct {
	TradeExecuted  bool `json:"tradeExecuted"`
	AgentStartStop bool `json:"agentStartStop"`
	Errors         bool `json:"errors"`
	DailySummary   bool `json:"dailySummary"`
	PositionOpened bool `json:"positionOpened"`
	PositionClosed bool `json:"positionClosed"`
	Heartbeat      bool `json:"heartbeat"`
}

type SlackPlugin struct {
	Enabled    bool          `json:"enabled"`
	WebhookURL string        `json:"webhookUrl"`
	Channel    string        `json:"channel"`
	NotifyOn   SlackNotifyOn `json:"notifyOn"`
}

type Plugins struct {
	Slack SlackPlugin `json:"slack"`
}

type Permissions struct {
	AllowLiveTrading     bool     `json:"allowLiveTrading"`
	MaxPositionPct       float64  `json:"maxPositionPct"`
	MaxDeployedPct       float64  `json:"maxDeployedPct"`
	MaxDailyLoss         float64  `json:"maxDailyLoss"`
	MaxWeeklyLoss        float64  `json:"maxWeeklyLoss"`
	MaxOpenPositions     int      `json:"maxOpenPositions"`
	MaxOrderValue        float64  `json:"maxOrderValue"`
	AllowedTools         []string `json:"allowedTools"`
	BlockedTools         []string `json:"blockedTools"`
	AllowOptions         bool     `json:"allowOptions"`
	AllowStocks          bool     `json:"allowStocks"`
	Allow0DTE            bool     `json:"allow0DTE"`
	RequireConfirmation  bool     `json:"requireConfirmation"`
	MaxToolRoundsPerBeat int      `json:"maxToolRoundsPerBeat"`
}

type PhaseTimeRange struct {
	Label string `json:"label"`
	Start *int   `json:"start"`
	End   *int   `json:"end"`
}

type HeartbeatProfile struct {
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Phases      map[string]int `json:"phases"`
}

type Config struct {
	SchemaVersion int            `json:"schemaVersion"`
	ActiveAgentID string         `json:"activeAgentId"`
	ActiveModel   string         `json:"activeModel"`
	Heartbeat     map[string]int `json:"heartbeat"`
	Permissions   Permissions    `json:"permissions"`
	Plugins       Plugins        `json:"plugins"`
	Agents        []Agent        `json:"agents"`
	Strategies    []Strategy     `json:"strategies,omitempty"`
	Models        []Model        `json:"models"`
}

var DefaultHeartbeat = map[string]int{
	"pre_market":   900,
	"market_open":  120,
	"midday":       600,
	"market_close": 120,
	"after_hours":  1800,
	"closed":       3600,
}

var DefaultPermissions = Permissions{
	AllowLiveTrading:     true,
	MaxPositionPct:       20,
	MaxDeployedPct:       60,
	MaxDailyLoss:         5,
	MaxWeeklyLoss:        8,
	MaxOpenPositions:     10,
	MaxOrderValue:        0,
	AllowedTools:         []string{},
	BlockedTools:         []string{},
	AllowOptions:         true,
	AllowStocks:          true,
	Allow0DTE:            false,
	RequireConfirmation:  false,
	MaxToolRoundsPerBeat: 25,
}

var DefaultPlugins = Plugins{
	Slack: SlackPlugin{
		Enabled:    false,
		WebhookURL: "",
		Channel:    "",
		NotifyOn: SlackNotifyOn{
			TradeExecuted:  true,
			AgentStartStop: true,
			Errors:         true,
			DailySummary:   true,
			PositionOpened: true,
			PositionClosed: true,
			Heartbeat:      false,
		},
	},
}

var HeartbeatProfiles = map[string]HeartbeatProfile{
	"active": {
		Label:       "Active Trading",
		Description: "High-frequency monitoring during market hours",
		Phases:      map[string]int{"pre_market": 300, "market_open": 60, "midday": 300, "market_close": 60, "after_hours": 600, "closed": 1800},
	},
	"passive": {
		Label:       "Passive Monitoring",
		Description: "Low-frequency check-ins, hands-off approach",
		Phases:      map[string]int{"pre_market": 1800, "market_open": 600, "midday": 900, "market_close": 600, "after_hours": 3600, "closed": 7200},
	},
	"long_horizon": {
		Label:       "Long Horizon",
		Description: "Weekly/monthly style check-ins for position management",
		Phases:      map[string]int{"pre_market": 7200, "market_open": 3600, "midday": 3600, "market_close": 3600, "after_hours": 7200, "closed": 14400},
	},
	"earnings_season": {
		Label:       "Earnings Season",
		Description: "Heightened vigilance during earnings periods",
		Phases:      map[string]int{"pre_market": 180, "market_open": 30, "midday": 120, "market_close": 30, "after_hours": 300, "closed": 1800},
	},
	"overnight": {
		Label:       "Overnight Hold",
		Description: "Set and forget with minimal overnight checks",
		Phases:      map[string]int{"pre_market": 900, "market_open": 120, "midday": 300, "market_close": 120, "after_hours": 7200, "closed": 10800},
	},
	"scalp": {
		Label:       "Scalp Mode",
		Description: "Rapid-fire execution for day trading",
		Phases:      map[string]int{"pre_market": 60, "market_open": 15, "midday": 30, "market_close": 15, "after_hours": 120, "closed": 600},
	},
}

var PhaseTimeRanges = map[string]PhaseTimeRange{
	"pre_market":   {Label: "Pre-Market", Start: intPtr(240), End: intPtr(570)},
	"market_open":  {Label: "Market Open", Start: intPtr(570), End: intPtr(630)},
	"midday":       {Label: "Midday", Start: intPtr(630), End: intPtr(900)},
	"market_close": {Label: "Market Close", Start: intPtr(900), End: intPtr(960)},
	"after_hours":  {Label: "After Hours", Start: intPtr(960), End: intPtr(1200)},
	"closed":       {Label: "Markets Closed", Start: nil, End: nil},
}

func intPtr(v int) *int { return &v }

var (
	store *configStore
	once  sync.Once
)

type configStore struct {
	mu     sync.RWMutex
	config *Config
	path   string
}

func Load(path string) error {
	var loadErr error
	once.Do(func() {
		store = &configStore{path: path}
		loadErr = store.load()
		if loadErr == nil {
			store.applyStrategyProjection(filepath.Join(filepath.Dir(path), "strategies.json"))
		}
	})
	return loadErr
}

// strategyFileEntry is the subset of data/strategies.json the dashboard needs.
type strategyFileEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Engine      string `json:"engine,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
}

type strategyFile struct {
	ActiveStrategies []string                     `json:"active_strategies"`
	Playbook         *strategyFileEntry           `json:"playbook"`
	Strategies       map[string]strategyFileEntry `json:"strategies"`
}

// buildStrategyProjection turns data/strategies.json into the list the dashboard
// shows. Order: playbook first, then active strategies as declared, then the
// rest alphabetically.
func buildStrategyProjection(raw []byte, rulesPath string) []Strategy {
	var f strategyFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}

	out := make([]Strategy, 0, len(f.Strategies)+1)
	seen := make(map[string]bool, len(f.Strategies)+1)

	if f.Playbook != nil && strings.TrimSpace(f.Playbook.ID) != "" {
		id := strings.TrimSpace(f.Playbook.ID)
		name := strings.TrimSpace(f.Playbook.Name)
		if name == "" {
			name = id
		}
		out = append(out, Strategy{
			ID:          id,
			Name:        name,
			Description: f.Playbook.Description,
			RulesFile:   strPtr(rulesPath),
		})
		seen[id] = true
	}

	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		entry, ok := f.Strategies[id]
		if !ok {
			return
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = id
		}
		out = append(out, Strategy{
			ID:          id,
			Name:        name,
			Description: entry.Description,
			Engine:      entry.Engine,
			RulesFile:   strPtr(rulesPath),
		})
		seen[id] = true
	}

	for _, id := range f.ActiveStrategies {
		add(strings.TrimSpace(id))
	}

	rest := make([]string, 0, len(f.Strategies))
	for id := range f.Strategies {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	for _, id := range rest {
		add(id)
	}

	return out
}

// applyStrategyProjection replaces the strategy list with the one derived from
// data/strategies.json. agent-config.json no longer owns strategy definitions,
// so both the dashboard and the code read the same file.
func (s *configStore) applyStrategyProjection(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	projection := buildStrategyProjection(raw, path)
	if len(projection) == 0 {
		return
	}
	s.mu.Lock()
	s.config.Strategies = projection
	s.mu.Unlock()
}

// --- Phase 2: write support for data/strategies.json ---

// strategiesFileDoc mirrors data/strategies.json for read/write. Unknown nested
// blocks (beat, trading_rules, global_filters) are preserved as raw JSON.
type strategiesFileDoc struct {
	SchemaVersion    int                               `json:"schemaVersion"`
	ActiveStrategies []string                          `json:"active_strategies"`
	ObserveSymbols   []string                          `json:"observe_symbols,omitempty"`
	Watchlist        []string                          `json:"watchlist,omitempty"` // legacy alias
	Playbook         *strategyFileEntry                `json:"playbook,omitempty"`
	Beat             json.RawMessage                   `json:"beat,omitempty"`
	TradingRules     json.RawMessage                   `json:"trading_rules,omitempty"`
	Strategies       map[string]map[string]interface{} `json:"strategies"`
	GlobalFilters    json.RawMessage                   `json:"global_filters,omitempty"`
}

// StrategySymbolPermissions is the "Operate" side of a strategy. Some symbols
// are explicitly allowed; others are explicitly blocked.
type StrategySymbolPermissions struct {
	OperateAllow []string `json:"operate_allow"`
	OperateBlock []string `json:"operate_block"`
}

// SymbolMeta is display metadata for a ticker. It is intentionally separate
// from strategies.json so the trading config stays focused on rules.
type SymbolMeta struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Exchange string `json:"exchange,omitempty"`
}

// WatchlistSnapshot is what the Watchlist panel reads. Observe is the single
// global source; Beat is an optional subset; each strategy narrows Operate.
type WatchlistSnapshot struct {
	ObserveSymbols []string                             `json:"observe_symbols"`
	BeatSymbols    []string                             `json:"beat_symbols"`
	Strategies     map[string]StrategySymbolPermissions `json:"strategies"`
	Symbols        map[string]SymbolMeta                `json:"symbols"`
}

// WatchlistUpdate is the payload accepted by PUT /api/watchlist.
type WatchlistUpdate struct {
	ObserveSymbols []string                             `json:"observe_symbols"`
	BeatSymbols    []string                             `json:"beat_symbols"`
	Strategies     map[string]StrategySymbolPermissions `json:"strategies,omitempty"`
	Symbols        map[string]SymbolMeta                `json:"symbols,omitempty"`
}

var strategyEditableFields = map[string]bool{
	"name":              true,
	"description":       true,
	"engine":            true,
	"enabled":           true,
	"operate_allow":     true,
	"operate_block":     true,
	"whitelist_symbols": true, // legacy alias
	"blacklist_symbols": true, // legacy alias
	"min_rr":            true,
	"max_risk_pct":      true,
	"setups":            true,
	"atr_thresholds":    true,
}

func strategiesPath() string {
	if store == nil {
		return filepath.Join("data", "strategies.json")
	}
	return filepath.Join(filepath.Dir(store.path), "strategies.json")
}

func symbolsPath() string {
	if store == nil {
		return filepath.Join("data", "symbols.json")
	}
	return filepath.Join(filepath.Dir(store.path), "symbols.json")
}

// GetSymbolCatalog returns the display metadata catalog used by the Watchlist
// search and table. A missing file is not an error.
func GetSymbolCatalog() map[string]SymbolMeta {
	out := make(map[string]SymbolMeta)
	raw, err := os.ReadFile(symbolsPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = make(map[string]SymbolMeta)
	}
	return out
}

func writeSymbolCatalog(catalog map[string]SymbolMeta) error {
	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := symbolsPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readStrategiesDoc() (*strategiesFileDoc, error) {
	raw, err := os.ReadFile(strategiesPath())
	if err != nil {
		return nil, err
	}
	var doc strategiesFileDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Strategies == nil {
		doc.Strategies = map[string]map[string]interface{}{}
	}
	return &doc, nil
}

func writeStrategiesDoc(doc *strategiesFileDoc) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := strategiesPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func slugifyStrategyName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('_')
		}
	}
	id := strings.Trim(b.String(), "_")
	for strings.Contains(id, "__") {
		id = strings.ReplaceAll(id, "__", "_")
	}
	if id == "" {
		return shortID()
	}
	return id
}

// GetStrategyDefinition returns the raw editable entry for one strategy.
func GetStrategyDefinition(id string) (map[string]interface{}, error) {
	doc, err := readStrategiesDoc()
	if err != nil {
		return nil, err
	}
	if doc.Playbook != nil && doc.Playbook.ID == id {
		return map[string]interface{}{
			"id":          id,
			"name":        doc.Playbook.Name,
			"description": doc.Playbook.Description,
			"enabled":     true,
			"playbook":    true,
			"readOnly":    true,
		}, nil
	}
	entry, ok := doc.Strategies[id]
	if !ok {
		return nil, fmt.Errorf("strategy %q not found", id)
	}
	out := make(map[string]interface{}, len(entry)+1)
	for k, v := range entry {
		out[k] = v
	}
	out["id"] = id
	return out, nil
}

func normalizeWatchSymbols(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		sym := strings.ToUpper(strings.TrimSpace(raw))
		if sym == "" || strings.ContainsAny(sym, " \t\r\n") || seen[sym] {
			continue
		}
		seen[sym] = true
		out = append(out, sym)
	}
	return out
}

func watchSymbolsFromRaw(raw interface{}) []string {
	switch list := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, fmt.Sprint(item))
		}
		return normalizeWatchSymbols(out)
	case []string:
		return normalizeWatchSymbols(list)
	}
	return nil
}

func strategySymbols(entry map[string]interface{}, primary, legacy string) []string {
	for _, key := range []string{primary, legacy} {
		if raw, ok := entry[key]; ok {
			return watchSymbolsFromRaw(raw)
		}
	}
	return nil
}

func observeSymbolsFromDoc(doc *strategiesFileDoc) []string {
	if len(doc.ObserveSymbols) > 0 {
		return normalizeWatchSymbols(doc.ObserveSymbols)
	}
	return normalizeWatchSymbols(doc.Watchlist)
}

func beatSymbolsFromRaw(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var beat map[string]interface{}
	if err := json.Unmarshal(raw, &beat); err != nil {
		return nil
	}
	return watchSymbolsFromRaw(beat["symbols"])
}

func setBeatSymbols(raw json.RawMessage, symbols []string) json.RawMessage {
	beat := map[string]interface{}{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &beat)
	}
	beat["symbols"] = symbols
	out, _ := json.Marshal(beat)
	return out
}

func strategyPermissionsFromDoc(doc *strategiesFileDoc) map[string]StrategySymbolPermissions {
	out := make(map[string]StrategySymbolPermissions, len(doc.Strategies))
	for id, entry := range doc.Strategies {
		out[id] = StrategySymbolPermissions{
			OperateAllow: strategySymbols(entry, "operate_allow", "whitelist_symbols"),
			OperateBlock: strategySymbols(entry, "operate_block", "blacklist_symbols"),
		}
	}
	return out
}

// GetWatchlist returns the global Observe list, the Beat subset and each
// strategy's Operate permissions.
func GetWatchlist() (WatchlistSnapshot, error) {
	doc, err := readStrategiesDoc()
	if err != nil {
		return WatchlistSnapshot{}, err
	}
	return WatchlistSnapshot{
		ObserveSymbols: observeSymbolsFromDoc(doc),
		BeatSymbols:    beatSymbolsFromRaw(doc.Beat),
		Strategies:     strategyPermissionsFromDoc(doc),
		Symbols:        GetSymbolCatalog(),
	}, nil
}

// UpdateWatchlist writes the single Observe list and the optional Beat subset.
// Beat must be a subset of Observe; per-strategy Operate lists are optional.
func UpdateWatchlist(update WatchlistUpdate) (WatchlistSnapshot, error) {
	observe := normalizeWatchSymbols(update.ObserveSymbols)
	if len(observe) == 0 {
		return WatchlistSnapshot{}, fmt.Errorf("observe list cannot be empty")
	}
	beat := normalizeWatchSymbols(update.BeatSymbols)
	observeSet := make(map[string]bool, len(observe))
	for _, sym := range observe {
		observeSet[sym] = true
	}
	for _, sym := range beat {
		if !observeSet[sym] {
			return WatchlistSnapshot{}, fmt.Errorf("symbol %s is in Beat but not in Observe", sym)
		}
	}

	store.mu.Lock()
	doc, err := readStrategiesDoc()
	if err != nil {
		store.mu.Unlock()
		return WatchlistSnapshot{}, err
	}
	doc.SchemaVersion = 5
	doc.ObserveSymbols = observe
	doc.Watchlist = nil
	doc.Beat = setBeatSymbols(doc.Beat, beat)
	for id, perms := range update.Strategies {
		entry, ok := doc.Strategies[id]
		if !ok {
			continue
		}
		allow := normalizeWatchSymbols(perms.OperateAllow)
		for _, sym := range allow {
			if !observeSet[sym] {
				store.mu.Unlock()
				return WatchlistSnapshot{}, fmt.Errorf("symbol %s is in Operate allow for %s but not in Observe", sym, id)
			}
		}
		entry["operate_allow"] = allow
		entry["operate_block"] = normalizeWatchSymbols(perms.OperateBlock)
		delete(entry, "whitelist_symbols")
		delete(entry, "blacklist_symbols")
	}
	if err := writeStrategiesDoc(doc); err != nil {
		store.mu.Unlock()
		return WatchlistSnapshot{}, err
	}
	if len(update.Symbols) > 0 {
		catalog := GetSymbolCatalog()
		for sym, meta := range update.Symbols {
			sym = strings.ToUpper(strings.TrimSpace(sym))
			if sym == "" {
				continue
			}
			if meta.Name == "" {
				meta.Name = sym
			}
			catalog[sym] = meta
		}
		if err := writeSymbolCatalog(catalog); err != nil {
			store.mu.Unlock()
			return WatchlistSnapshot{}, err
		}
	}
	store.mu.Unlock()

	store.applyStrategyProjection(strategiesPath())
	return GetWatchlist()
}

func (s *configStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.config = createDefaultConfig()
			return s.saveLocked()
		}
		return err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		s.config = createDefaultConfig()
		return s.saveLocked()
	}

	cfg := createDefaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		s.config = createDefaultConfig()
		return s.saveLocked()
	}

	// v2→v3 migration: if old schema has sandboxes, extract config from first sandbox
	if cfg.SchemaVersion != 3 {
		if sandboxes, ok := raw["sandboxes"].(map[string]interface{}); ok {
			for _, sbx := range sandboxes {
				if m, ok := sbx.(map[string]interface{}); ok {
					if agent, ok := m["agent"].(map[string]interface{}); ok {
						if id, ok := agent["activeAgentId"].(string); ok {
							cfg.ActiveAgentID = id
						}
						if model, ok := agent["model"].(string); ok {
							cfg.ActiveModel = model
						}
					}
				}
				break
			}
		}
		cfg.SchemaVersion = 3
	}

	if cfg.Heartbeat == nil {
		cfg.Heartbeat = copyMap(DefaultHeartbeat)
	}
	if cfg.Permissions.AllowedTools == nil {
		cfg.Permissions.AllowedTools = []string{}
	}
	if cfg.Permissions.BlockedTools == nil {
		cfg.Permissions.BlockedTools = []string{}
	}

	s.config = cfg
	return s.saveLocked()
}

func (s *configStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	// Strategies are projected from data/strategies.json; never write them back
	// into agent-config.json or the duplication returns.
	cfg := *s.config
	cfg.Strategies = nil
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func save() error {
	return store.saveLocked()
}

// IsInitialized reports whether the runtime config has been loaded.
func IsInitialized() bool {
	if store == nil {
		return false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.config != nil
}

// Get returns a copy of the current config.
func Get() Config {
	if store == nil {
		return Config{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	cfg := *store.config
	cfg.Agents = make([]Agent, len(store.config.Agents))
	copy(cfg.Agents, store.config.Agents)
	cfg.Strategies = make([]Strategy, len(store.config.Strategies))
	copy(cfg.Strategies, store.config.Strategies)
	cfg.Models = make([]Model, len(store.config.Models))
	copy(cfg.Models, store.config.Models)
	cfg.Heartbeat = copyMap(store.config.Heartbeat)
	return cfg
}

// --- Agents ---

func AddAgent(a Agent) (Agent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	a.ID = shortID()
	if a.Name == "" {
		a.Name = "New Agent"
	}
	if a.SystemPromptTemplate == "" {
		a.SystemPromptTemplate = "custom"
	}
	if a.Model == "" {
		a.Model = store.config.ActiveModel
	}
	if a.HeartbeatOverrides == nil {
		a.HeartbeatOverrides = map[string]int{}
	}
	a.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	store.config.Agents = append(store.config.Agents, a)
	return a, save()
}

func UpdateAgent(id string, updates map[string]interface{}) (Agent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	idx := -1
	for i, a := range store.config.Agents {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return Agent{}, fmt.Errorf("agent not found")
	}

	// Marshal the existing agent, merge updates, unmarshal back
	existing, _ := json.Marshal(store.config.Agents[idx])
	var merged map[string]interface{}
	json.Unmarshal(existing, &merged)
	for k, v := range updates {
		merged[k] = v
	}
	merged["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, _ := json.Marshal(merged)
	var updated Agent
	json.Unmarshal(data, &updated)
	store.config.Agents[idx] = updated

	if store.config.ActiveAgentID == id {
		if m, ok := updates["model"].(string); ok && m != "" {
			store.config.ActiveModel = m
		}
	}

	return updated, save()
}

func RemoveAgent(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if id == "default" {
		return fmt.Errorf("cannot remove default agent")
	}

	agents := make([]Agent, 0, len(store.config.Agents))
	for _, a := range store.config.Agents {
		if a.ID != id {
			agents = append(agents, a)
		}
	}
	store.config.Agents = agents

	if store.config.ActiveAgentID == id {
		store.config.ActiveAgentID = "default"
	}
	return save()
}

func SetActiveAgent(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	var found *Agent
	for i := range store.config.Agents {
		if store.config.Agents[i].ID == id {
			found = &store.config.Agents[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("agent not found")
	}
	store.config.ActiveAgentID = id
	if found.Model != "" {
		store.config.ActiveModel = found.Model
	}
	return save()
}

func GetActiveAgent() *Agent {
	if store == nil {
		return nil
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, a := range store.config.Agents {
		if a.ID == store.config.ActiveAgentID {
			cp := a
			return &cp
		}
	}
	if len(store.config.Agents) > 0 {
		cp := store.config.Agents[0]
		return &cp
	}
	return nil
}

func GetAgentByID(id string) *Agent {
	if store == nil {
		return nil
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, a := range store.config.Agents {
		if a.ID == id {
			cp := a
			return &cp
		}
	}
	return nil
}

func ActiveAgentName() string {
	a := GetActiveAgent()
	if a != nil {
		return a.Name
	}
	return ""
}

func AgentName(id string) string {
	a := GetAgentByID(id)
	if a != nil {
		return a.Name
	}
	return id
}

// --- Strategies (Phase 2: writes go to data/strategies.json) ---

func findProjectedStrategy(id string) (Strategy, bool) {
	for _, s := range Get().Strategies {
		if s.ID == id {
			return s, true
		}
	}
	return Strategy{}, false
}

// AddStrategy creates a disabled strategy entry in data/strategies.json. New
// ids have no execution engine until code supports them, so they ship disabled.
func AddStrategy(s Strategy) (Strategy, error) {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return Strategy{}, fmt.Errorf("el nombre de la estrategia es obligatorio")
	}

	store.mu.Lock()
	doc, err := readStrategiesDoc()
	if err != nil {
		store.mu.Unlock()
		return Strategy{}, err
	}
	id := slugifyStrategyName(name)
	if _, exists := doc.Strategies[id]; exists {
		id = id + "_" + shortID()
	}
	doc.Strategies[id] = map[string]interface{}{
		"name":           name,
		"description":    s.Description,
		"engine":         s.Engine,
		"enabled":        false,
		"min_rr":         2.0,
		"max_risk_pct":   1.0,
		"operate_allow":  []string{},
		"operate_block":  []string{},
	}
	if err := writeStrategiesDoc(doc); err != nil {
		store.mu.Unlock()
		return Strategy{}, err
	}
	store.mu.Unlock()

	store.applyStrategyProjection(strategiesPath())
	if out, ok := findProjectedStrategy(id); ok {
		return out, nil
	}
	return Strategy{ID: id, Name: name}, nil
}

func UpdateStrategy(id string, updates map[string]interface{}) (Strategy, error) {
	if strings.TrimSpace(id) == "" {
		return Strategy{}, fmt.Errorf("strategy id is required")
	}

	store.mu.Lock()
	doc, err := readStrategiesDoc()
	if err != nil {
		store.mu.Unlock()
		return Strategy{}, err
	}
	entry, ok := doc.Strategies[id]
	if !ok {
		isPlaybook := doc.Playbook != nil && doc.Playbook.ID == id
		store.mu.Unlock()
		if isPlaybook {
			return Strategy{}, fmt.Errorf("el playbook %q es de solo lectura", id)
		}
		return Strategy{}, fmt.Errorf("strategy %q not found", id)
	}
	for k, v := range updates {
		if strategyEditableFields[k] {
			entry[k] = v
		}
	}
	entry["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	doc.Strategies[id] = entry
	if err := writeStrategiesDoc(doc); err != nil {
		store.mu.Unlock()
		return Strategy{}, err
	}
	store.mu.Unlock()

	store.applyStrategyProjection(strategiesPath())
	if out, ok := findProjectedStrategy(id); ok {
		return out, nil
	}
	return Strategy{ID: id, Name: fmt.Sprint(entry["name"])}, nil
}

func RemoveStrategy(id string) error {
	store.mu.Lock()
	doc, err := readStrategiesDoc()
	if err != nil {
		store.mu.Unlock()
		return err
	}
	if doc.Playbook != nil && doc.Playbook.ID == id {
		store.mu.Unlock()
		return fmt.Errorf("el playbook %q no se puede borrar", id)
	}
	for _, a := range doc.ActiveStrategies {
		if a == id {
			store.mu.Unlock()
			return fmt.Errorf("la estrategia %q está activa; desactívala antes de borrarla", id)
		}
	}
	if _, ok := doc.Strategies[id]; !ok {
		store.mu.Unlock()
		return fmt.Errorf("strategy %q not found", id)
	}
	delete(doc.Strategies, id)
	if err := writeStrategiesDoc(doc); err != nil {
		store.mu.Unlock()
		return err
	}
	store.mu.Unlock()

	store.applyStrategyProjection(strategiesPath())
	return nil
}

func GetStrategyByID(id string) *Strategy {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, s := range store.config.Strategies {
		if s.ID == id {
			cp := s
			return &cp
		}
	}
	return nil
}

// --- Models ---

func SetActiveModel(modelID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.config.ActiveModel = modelID
	return save()
}

// SetActiveAgentModel changes the model of the currently-active agent (and the
// fallback ActiveModel). The active agent is the single source of truth for
// which model runs, so any path that wants to change "the model" — the model
// picker, the Settings dropdown — routes through here.
func SetActiveAgentModel(modelID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.config.ActiveModel = modelID
	for i := range store.config.Agents {
		if store.config.Agents[i].ID == store.config.ActiveAgentID {
			store.config.Agents[i].Model = modelID
			break
		}
	}
	return save()
}

// ActiveModelID returns the effective model identifier ("provider/model") that
// drives the agent: the active agent's model, falling back to ActiveModel.
// This mirrors what the dashboard footer displays.
func ActiveModelID() string {
	if store == nil {
		return ""
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	model := store.config.ActiveModel
	for _, a := range store.config.Agents {
		if a.ID == store.config.ActiveAgentID && a.Model != "" {
			model = a.Model
			break
		}
	}
	return model
}

// ActiveModelEnv splits the active model into the LLM_PROVIDER / LLM_MODEL pair
// that the LLM clients and dashboard expect (e.g. "google/gemini-2.5-pro" →
// "gemini", "gemini-2.5-pro"). An empty provider means the id had no prefix.
func ActiveModelEnv() (provider, model string) {
	full := ActiveModelID()
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", full
	}
	if parts[0] == "google" {
		return "gemini", parts[1]
	}
	return "anthropic", parts[1]
}

// SyncRuntimeEnv pushes the active model into the LLM_PROVIDER / LLM_MODEL
// environment variables that the LLM clients read at call time, keeping the
// running model, the dashboard footer, and the Settings panel in agreement.
// Call after any change to the active agent or its model, and once at startup.
func SyncRuntimeEnv() {
	provider, model := ActiveModelEnv()
	if model == "" {
		return
	}
	if provider != "" {
		os.Setenv("LLM_PROVIDER", provider)
	}
	os.Setenv("LLM_MODEL", model)
}

func SetModels(models []Model) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.config.Models = models
	return save()
}

// --- Heartbeat ---

func UpdateHeartbeat(phases map[string]int) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for k, v := range phases {
		store.config.Heartbeat[k] = v
	}
	return save()
}

func GetHeartbeatForPhase(phase string) int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if v, ok := store.config.Heartbeat[phase]; ok {
		return v
	}
	if v, ok := DefaultHeartbeat[phase]; ok {
		return v
	}
	return 600
}

func ApplyHeartbeatProfile(key string) error {
	profile, ok := HeartbeatProfiles[key]
	if !ok {
		return fmt.Errorf("unknown heartbeat profile: %s", key)
	}
	return UpdateHeartbeat(profile.Phases)
}

func UpdatePhaseTimeRange(phase string, start, end *int) error {
	ptr, ok := PhaseTimeRanges[phase]
	if !ok {
		return fmt.Errorf("unknown phase: %s", phase)
	}
	if start != nil {
		ptr.Start = start
	}
	if end != nil {
		ptr.End = end
	}
	PhaseTimeRanges[phase] = ptr
	return nil
}

// --- Permissions ---

func UpdatePermissions(perms map[string]interface{}) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	existing, _ := json.Marshal(store.config.Permissions)
	var merged map[string]interface{}
	json.Unmarshal(existing, &merged)
	for k, v := range perms {
		merged[k] = v
	}
	data, _ := json.Marshal(merged)
	json.Unmarshal(data, &store.config.Permissions)
	return save()
}

func GetPermissions() Permissions {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.config.Permissions
}

// --- Plugins ---

func UpdatePlugin(name string, updates map[string]interface{}) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	existing, _ := json.Marshal(store.config.Plugins)
	var pluginsMap map[string]interface{}
	json.Unmarshal(existing, &pluginsMap)

	if pluginsMap[name] == nil {
		pluginsMap[name] = map[string]interface{}{}
	}
	plug := pluginsMap[name].(map[string]interface{})
	for k, v := range updates {
		plug[k] = v
	}
	pluginsMap[name] = plug

	data, _ := json.Marshal(pluginsMap)
	json.Unmarshal(data, &store.config.Plugins)
	return save()
}

func GetPlugin(name string) interface{} {
	store.mu.RLock()
	defer store.mu.RUnlock()
	switch name {
	case "slack":
		return store.config.Plugins.Slack
	}
	return nil
}

// --- Helpers ---

func createDefaultConfig() *Config {
	defaultStrategyID := "default"
	return &Config{
		SchemaVersion: 3,
		ActiveAgentID: "default",
		ActiveModel:   "anthropic/claude-sonnet-4-6",
		Heartbeat:     copyMap(DefaultHeartbeat),
		Permissions:   DefaultPermissions,
		Plugins:       DefaultPlugins,
		Agents: []Agent{
			{
				ID: "default", Name: "Prophet",
				Description:          "Aggressive discretionary options trader with scalping overlay",
				SystemPromptTemplate: "default",
				StrategyID:           &defaultStrategyID,
				Model:                "anthropic/claude-sonnet-4-6",
				HeartbeatOverrides:   map[string]int{},
				CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
			},
		},
		Strategies: []Strategy{
			{
				ID: "default", Name: "Aggressive Options",
				Description: "Multi-timeframe options with scalping overlay",
				RulesFile:   strPtr("data/strategies.json"),
				CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
			},
		},
		Models: defaultModels(),
	}
}

func defaultModels() []Model {
	return []Model{
		{ID: "anthropic/claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Description: "Best speed + intelligence, $3/$15 per MTok"},
		{ID: "anthropic/claude-opus-4-8", Name: "Claude Opus 4.8", Description: "Next generation Opus, $15/$75 per MTok"},
		{ID: "google/gemini-2.5-pro", Name: "Gemini 2.5 Pro", Description: "Google Advanced Pro Model"},
		{ID: "anthropic/claude-opus-4-6", Name: "Claude Opus 4.6", Description: "Most intelligent, best for agents, $5/$25 per MTok"},
		{ID: "anthropic/claude-haiku-4-5", Name: "Claude Haiku 4.5", Description: "Fastest, near-frontier, $1/$5 per MTok"},
	}
}

func shortID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func strPtr(s string) *string { return &s }

func copyMap(m map[string]int) map[string]int {
	cp := make(map[string]int, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
