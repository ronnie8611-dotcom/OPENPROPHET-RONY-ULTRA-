# Project Brief — OpenProphet

> **Documento vivo.** Resume la arquitectura real del código, mis reglas de trading y dónde vive cada función importante.
> **Para una tarea/chat nuevo:** lee primero `HANDOFF.md`; allí está el estado actual, pendientes y reglas de trabajo.
> **Regla de mantenimiento:** cada vez que hagamos un cambio grande (nuevo servicio, cambio de flujo de ejecución, cambio de límites de riesgo, migración de broker, nuevas herramientas del agente), hay que actualizar este archivo en la misma iteración.

**Última actualización:** 2026-11-17 (Tanda 16: tabla Watchlist, Observe/Operate, búsqueda de compañías y validación IBKR; Go 1.27.4, dependencias sin vulnerabilidades y staticcheck limpio)
**Módulo Go:** `prophet-trader` (raíz del repo)

<!-- AUTO_STATUS_START -->
**Estado automatico:** 2026-11-17 14:00 | Go go1.27.4 windows/amd64 | Check: TODO OK (2026-11-17 13:58:11) | Bot: 66 328 848 bytes, 2026-11-17 13:56 | Disco C: 16.36 GB
<!-- AUTO_STATUS_END -->

---

## 1. Resumen ejecutivo

OpenProphet es un **bot de trading para Interactive Brokers** escrito en **Go** (Gin + GORM/SQLite + cliente TWS propio) que expone un **dashboard web** y opera a través de un **agente LLM** (DeepSeek como primario, Gemini/Claude como fallback) que puede llamar herramientas reales (quotes, contratos, histórico, spreads de opciones, memoria, etc.).

El diseño es **"human-in-the-loop" por defecto**:

```
LLM propone  →  IntentManager encola  →  humano autoriza con ADMIN_TOKEN
             →  GatedTradingService (kill-switch)  →  RiskEngine / validaciones
             →  IBKRTradingService  →  IB Gateway (TWS)  →  PositionManager vigila SL/TP
```

En `TRADING_MODE=autonomous` el humano deja de estar en el medio, pero los gates deterministas (kill-switch, límites de pérdida, whitelist/blacklist, hard stop obligatorio en futuros) siguen aplicando en Go.

**Lo que NO es:** no es un backtester, no usa Alpaca (los encabezados de `.env.example` son heredados), y no hay ya un agente Node separado — la carpeta `agent/` solo conserva el HTML del dashboard y las imágenes.

---

## 2. Cómo se arranca

| Acción | Comando / archivo |
|---|---|
| Arrancar el bot | `START_AGENTS.bat` → `set CGO_ENABLED=1` + `go run ./cmd/bot/main.go` |
| Binario ya compilado | `bot.exe` (raíz) |
| Compilar | `go build -o bot.exe ./cmd/bot` |
| Tests | `go test ./...` |
| Reentrenar XGBoost | `auto_retrain.bat` → ejecuta `Backtest_Lab\ml_trainer.py` y copia `model.json` a la raíz |
| Laboratorio manual | `RUN_LAB.bat` → menú: reentrenar, validar Raschke, backtests y abrir la carpeta |
| Watchdog | `WATCHDOG.bat` → revisa cada 60 s que `bot.exe` siga vivo y lo reinicia si se cayó |
| Chequeo de calidad | `CHECK.bat` → gofmt/vet/test -race/staticcheck/govulncheck/UI (ver `check.ps1`) |
| Docker (opcional) | `docker compose up -d --build` (ver `DOCKER.md`) |
| Requisito externo | **IB Gateway / TWS** escuchando en `127.0.0.1:4002` (paper) |
| Puerto HTTP | `4534` (backend Go), dashboard en `3737` por convención |
| Aplicar cambios de reglas | **Panel Watchlist/Strategies** guarda y recarga en caliente (`ReloadStrategiesConfig`); si editas `data/strategies.json` a mano, reinicia el bot |
| Abrir el panel | `http://localhost:4534` (solo desde esta PC; `BIND_ADDR=127.0.0.1` bloquea la LAN) |

Variables clave (`.env`): `TRADING_MODE`, `IBKR_HOST/PORT/CLIENT_ID`, `ADMIN_TOKEN` (definido en `.env`; no se versiona), `BIND_ADDR=127.0.0.1`, `PYTHON_PATH=C:\Users\50688\AppData\Local\Python\pythoncore-3.15-64\python.exe`, `BEAT_ENABLED`, `BEAT_INTERVAL_SECS`, `BEAT_MAX_DAILY_EXECUTIONS`, `DATABASE_PATH=./data/prophet_trader.db`, `DEEPSEEK_API_KEY` / `GEMINI_API_KEY` / `ANTHROPIC_*`.
`BEAT_SYMBOL=QQQ,MNQ` es solo respaldo: primero manda `beat.symbols`, luego `observe_symbols` (o `watchlist` legacy).
`LAB_DIR=./Backtest_Lab` indica dónde está el laboratorio Python (logs y scripts).
`DAILY_REPORT_ENABLED=true` y `DAILY_REPORT_TIME=16:05` controlan el resumen diario por Telegram.

---

## 3. Stack técnico

| Capa | Tecnología | Evidencia |
|---|---|---|
| Lenguaje principal | **Go 1.27.4** (toolchain; módulo `go 1.26.0`) | `go.mod` |
| HTTP | **Gin** `v1.10.0` | `cmd/bot/main.go`, `controllers/` |
| Persistencia | **SQLite + GORM** (`gorm.io/driver/sqlite`) | `database/storage.go`, `data/prophet_trader.db` |
| Broker | **IBKR TWS API implementada a mano** | `tws/` (encoder, decoder, orders, symbology…) |
| LLMs | **Anthropic/DeepSeek**, **Google GenAI** (Gemini) | `services/anthropic_client.go`, `gemini_client.go`, `multi_llm_client.go` |
| ML | **XGBoost** (Python 3.15) invocado por `os/exec` | `predict.py`, `model_info.py`, `model.json` |
| Config dinámica | JSON en `data/` | `configstore/configstore.go` |
| Front | HTML estático servido por Gin | `agent/public/index.html` |
| Logs | `logrus` + JSON de actividad diaria | `services/activity_logger.go`, `activity_logs/` |

---

## 4. Mapa de carpetas

```
OpenProphet/
├─ cmd/bot/main.go            # entrypoint: wiring de TODO (líneas ~24-270)
├─ config/config.go           # env → Config, TradingMode, helpers con live-reload
├─ configstore/configstore.go # agentes, estrategias, modelos, permisos, heartbeat (JSON)
├─ controllers/               # handlers HTTP (12 controladores)
├─ database/storage.go        # SQLite: bars, orders, positions, suggestions, managed positions
├─ data/
│  ├─ agent-config.json       # ⚠️ AQUÍ viven las personas/agentes y los permisos reales
│  ├─ strategies.json         # ⚠️ AQUÍ viven Observe/Beat/Operate y filtros globales
│  ├─ symbols.json            # catálogo local: ticker, nombre, tipo y exchange
│  └─ prophet_trader.db       # SQLite
├─ interfaces/trading.go      # contratos: TradingService, DataService, StorageService, Order, Bar…
├─ models/models.go           # modelos GORM (DBManagedPosition, DBSuggestion…)
├─ services/                  # el "cerebro" y la ejecución (33 archivos + tests)
├─ tws/                       # cliente del protocolo IBKR (no es dependencia externa)
├─ agent/public/              # dashboard + landing + img
├─ activity_logs/             # un JSON por día con toda la actividad
├─ _Archivo_Muerto/           # código/docs viejos (incluye TRADING_RULES.md, ver §11)
├─ _Backups/                  # .env y snapshots previos (ignorada por Go)
├─ model.json                 # modelo XGBoost activo
├─ predict.py / model_info.py # inferencia / feature importances
├─ auto_retrain.bat           # reentrenamiento externo + copia del modelo
├─ RUN_LAB.bat                # menú de reentrenamiento y backtests
├─ Backtest_Lab/              # laboratorio Python (datos, scripts, modelos, logs)
├─ backtest/                  # backtester nativo en Go
├─ Dockerfile / docker-compose.yml / .dockerignore
├─ CHECK.bat / check.ps1      # chequeo local de calidad
├─ DOCKER.md                  # instrucciones de contenedor
└─ START_AGENTS.bat           # arranque
```

---

## 5. Flujo completo de una operación

1. **Arranque** — `cmd/bot/main.go`
   - `godotenv.Load()` → `config.Load()` → valida puerto IBKR (`4002` paper; `4001` live solo con `ALLOW_LIVE_PORT` + `REQUIRE_DOUBLE_CONFIRM` + `ADMIN_TOKEN`, si no `logger.Fatalf`).
   - `configstore.Load("data/agent-config.json")`.
   - Conecta a IB, crea `ContractResolver`, `IBKRTradingService`, lo envuelve en `GatedTradingService`, crea `IBKRDataService`, y arranca la auto-reconexión con backoff (5 s → 5 min; al reconectar re-habilita el gate).
   - Crea `LocalStorage`, `PositionManager`, `IntentManager`, `SuggestionManager`, `AutonomousBeat`, `NewsCache`, `ActivityLogger`.
2. **Ciclo del agente** — `AutonomousBeat.Run` → `tick`
   - Construye el **system prompt**: playbook de `data/strategies.json → trading_rules` (solo si `enabled: true`) + límites de permisos + `ACTIVE STRATEGIES` + `AUTOMATIC WATCHLIST` (`beat.symbols`, por defecto QQQ/MNQ) + whitelist/blacklist + instrucciones por modo.
   - Bucle LLM↔tools (`maxTurns = 10`, hardcodeado en `autonomous_beat.go:370`) vía `ExecuteAgentTool`.
   - La delegación a otros agentes se hace con la tool `jim_rogers(target_agent_id, prompt)`.
3. **Decisión** — el LLM llama a `place_managed_position` / `liquidate_position` / `place_standalone_order` / `place_options_order` / `place_combo_order`.
   - Antes: `validateOrderDeterministic` (whitelist/blacklist, límites de pérdida, hard stop obligatorio en futuros a mercado).
4. **Gate de modo** en `services/agent_tools.go`:
   - `suggest` → `SuggestionManager.CreateSuggestion` (no toca el mercado).
   - `supervised` → `IntentManager.CreateIntent` (TTL 300 s) y espera aprobación humana.
   - `autonomous` → ejecuta directo (sujeto al resto de gates).
5. **Aprobación humana** — `POST /api/v1/intents/authorize/:id` con `Authorization: Bearer <ADMIN_TOKEN>`
   - `IntentManager.ClaimForExecution` (un solo ganador, race-safe), stale-price guard (slippage máx. 0.5 %), luego `PositionManager.PlaceManagedPosition` o la orden correspondiente.
6. **Ejecución** — `GatedTradingService.CanTrade()` = gate ON **y** `TradingMode.AllowsExecution()`; después `IBKRTradingService` → TWS.
7. **Gestión posterior** — `PositionManager.MonitorPositions` cierra por SL/TP/trailing/time-stop y persiste el estado en SQLite.
8. **Observabilidad** — SSE `/api/events`, `activity_logs/activity_YYYY-MM-DD.json`, `bot.log`, opcional Telegram/Slack.

---

## 6. Modos de trading y capas de seguridad

`TRADING_MODE` (`config/config.go`): `off` | `suggest` | `supervised` | `autonomous`

| Modo | Coloca órdenes | Requiere humano | Uso |
|---|---|---|---|
| `off` | No (dry-run) | — | Solo analizar; el gate bloquea y loguea la intención |
| `suggest` | No | — | Genera sugerencias revisables |
| `supervised` | Sí, tras aprobación | **Sí** (`ADMIN_TOKEN`) | Modo actual en `.env` |
| `autonomous` | Sí, directo | No | Solo con límites deterministas activos |

Capas, en orden de defensa:

1. **Puerto IBKR** — `cmd/bot/main.go:~53-61`: 4001 exige `ALLOW_LIVE_PORT=true` + `REQUIRE_DOUBLE_CONFIRM=true` + `ADMIN_TOKEN`; cualquier puerto distinto de 4002/4001 aborta el arranque.
2. **Kill-switch de conexión** — `GatedTradingService` (`services/gated_trading.go`): si IB se desconecta se `Disable()` y **cancela todas las órdenes abiertas**; se re-habilita al reconectar.
3. **Modo de trading** — `TradingMode.AllowsExecution()` (`config/config.go:159`).
4. **Intent/approval** — `IntentManager` + comparación constante del bearer token + TTL + stale-price guard (`controllers/intent_controller.go`).
5. **Validación determinista** — `validateOrderDeterministic` (`services/agent_tools.go:87+`): whitelist/blacklist por estrategia, límite semanal 8 %, límite diario 5 %, **stop-loss obligatorio en futuros a mercado**.
6. **Riesgo de spreads** — `CheckSpreadRisk` (`services/riskengine.go:205`): max-loss ≤ `maxPositionPct` del portfolio y ≤ `maxOrderValue`.
7. **PositionManager** — `validateHardStop`, `validateRequest`, trailing stop y time-stop.
8. **Límites de cartera (Tanda 2, ajustados en Tanda 3)** — `checkPortfolioCaps` (60 % desplegado + 10 posiciones) y `checkPerInstrumentCap` (20 % por símbolo), aplicados en `validateOrderDeterministic` **y** al inicio de `PositionManager.PlaceManagedPosition`, que es el punto por el que pasa la aprobación humana y cada chunk del TWAP.
9. **Red local (Tanda 2)** — el servidor escucha solo en `BIND_ADDR=127.0.0.1` y el CORS rechaza orígenes que no sean `localhost`/`127.0.0.1`/`::1`.
10. **Time stop con aprobación (Tanda 8)** — a los 60 min, `enforceTimeStop` crea una intención de liquidación (`notes: LIQUIDATION`), avisa por Telegram y espera `ADMIN_TOKEN`; no cierra solo.

---

## 7. Mis reglas de trading

### 7.1 Mandato de mercado
- **Solo EE. UU.**: futuros `NQ, MNQ, ES, MES, YM, MYM, RTY, M2K` y acciones US (`SPY, QQQ, AAPL, AMZN, TSLA, IWM, GOOGL, META`…).
- **Prohibido** analizar/operar Europa (ESTX50 incluido). El prompt inyecta siempre `AUTOMATIC WATCHLIST: QQQ, MNQ` con la prohibición explícita de ESTX50.
- Las reglas completas de IB/VWAP/PDH-PDL viven en `data/strategies.json → trading_rules` con `"enabled": false` (documentadas, no aplicadas). Cambiar a `true` las inyecta en el prompt.
- Si los datos están retrasados hay que declararlo: `⚠️ WARNING: DELAYED DATA (15M)` (o los minutos reales).
- Futuros: obtener el `con_id` con `search_contract` y consultar `get_quote` con ese `con_id` (no con el ticker tipo `MNQZ6`).
- Zona horaria operativa: **CST (Costa Rica, UTC-6)**; divisa base **USD**.
- Antes de operar acciones US: revisar `get_insider_activity` (compras de insiders = señal alcista).
- Antes de operar: llamar a `get_memory`; al cerrar: `save_memory`.

### 7.2 Límites de riesgo (valores efectivos)

| Regla | Valor | Fuente | ¿Aplicado por código? |
|---|---|---|---|
| Kill switch diario | **−5 % NAV** | `permissions.maxDailyLoss` | ✅ `checkDailyLossLimit` (tool + `PlaceManagedPosition`) |
| Circuit breaker semanal | **−8 %** desde el NAV del lunes | `permissions.maxWeeklyLoss` + `data/weekly_baseline.json` | ✅ `checkWeeklyLossLimit` (ya no hardcodeado) |
| Máx. por instrumento | **20 % NAV** | `permissions.maxPositionPct` | ✅ `checkPerInstrumentCap` + `CheckSpreadRisk` (SPY exento) |
| Máx. capital desplegado | **60 % NAV** | `permissions.maxDeployedPct` | ✅ `checkPortfolioCaps` (antes solo prompt) |
| Máx. posiciones abiertas | **10** | `permissions.maxOpenPositions` | ✅ `checkPortfolioCaps` (antes solo prompt) |
| Máx. operaciones nuevas/día | **2** | `global_filters.max_trades_per_day` + `data/daily_trades.json` | ✅ `ReserveDailyTrade` (los chunks del TWAP no consumen slots extra) |
| Time stop | **60 min** | `global_filters.time_stop_minutes` | ✅ `PositionManager.enforceTimeStop`: crea una **intención de liquidación** y pide aprobación; nunca cierra solo (0 = desactivado) |
| Máx. valor por orden (opciones) | **premium × 100 × qty** | `permissions.maxOrderValue` | ✅ `ValidateOptionsOrder` (fail-closed si no hay precio) |
| Máx. valor por orden | **0 = sin límite** | `permissions.maxOrderValue` | ✅ `CheckSpreadRisk` |
| Stop loss obligatorio | Futuro a mercado sin SL → rechazo | `agent_tools.go:109-110` | ✅ |
| Slippage máximo al aprobar | **0.5 %** | `MAX_PRICE_SLIPPAGE_PERCENT` | ✅ |
| TTL de intent | **300 s** | `INTENT_TTL_SECS` | ✅ |
| Máx. ejecuciones/día del beat | **3** | `BEAT_MAX_DAILY_EXECUTIONS` | ✅ en `AutonomousBeat` |
| Turnos LLM por beat | **10** (hardcode) | `autonomous_beat.go:370` | ✅ (no lee `maxToolRoundsPerBeat: 15`) |
| Sizing futuros | NQ $20/pt, MNQ $2/pt, ES $50/pt, MES $5/pt | `TRADING_RULES.md` | ⚠️ criterio del LLM |

Flags de permisos: `allowLiveTrading: true`, `allowOptions: true`, `allowStocks: true`, `allow0DTE: true`, `requireConfirmation: false`.

### 7.3 Observe / Operate (`data/strategies.json`)
- **Observe** (`observe_symbols`, legacy `watchlist`): universo global que el sistema puede investigar y vigilar. Es la **única lista global** que se edita en el panel Watchlist.
- **Beat** (`beat.symbols`): subconjunto opcional de Observe que el agente autónomo mira en cada ciclo. Si está vacío, usa todo Observe.
- **`raschke`** (habilitada) — *Volatility Breakout*, descrita como "Sharpe 3.48"; `min_rr 2.5`, `max_risk_pct 0.75`, umbrales ATR `narrow 0.30 / wide 0.50`.
  - **Operate allow** (`operate_allow`, legacy `whitelist_symbols`): `QQQ, SPY, IWM, TSLA, GOOGL, META`.
  - **Operate block** (`operate_block`, legacy `blacklist_symbols`): `AAPL, NVDA, MSFT, AMZN, PLTR, DOGE, BTC, XRP, SOL, SPCX, AI, BOTZ, ARKX, XOVR, RKLB, MNQZ6`.
- **`mean_reversion`** (habilitada, **el bot la opera solo en `1Day`**) — Bollinger + RSI (sobreventa → compra, sobrecompra → venta); `min_rr 1.5`, `max_risk_pct 1.0`; mismo Operate allow, Operate block sin `MNQZ6`. El **backtest sí la prueba en todos los timeframes**.
- Deshabilitadas: `fit_mom`, `combined`, `turtle_breakout`.
- **Filtros globales**: `require_vwap_alignment: true`, `min_volume_multiplier: 1.5`, `max_trades_per_day: 2`, `time_stop_minutes: 60`, `avoid_power_hour: true`.

> **Observe ≠ Operate.** Observe controla lo que el sistema mira; Operate allow/block controla lo que cada estrategia puede operar. `operate_allow` vacío hereda el Observe global; una lista no vacía debe ser subconjunto de Observe. El bloqueo gana siempre.

> La whitelist/blacklist vieja sigue leyéndose como alias, pero el panel nuevo escribe `operate_allow` / `operate_block` y migra el archivo al guardar. Los `global_filters` son guía para el LLM; los límites 20/60/10 y el time stop sí se aplican en Go.
>
> **Buscador:** el panel Watchlist consulta `data/symbols.json` y, si no encuentra suficientes resultados, Yahoo Finance (`/api/symbols/search`). Al añadir un símbolo se consulta `/api/symbols/validate` contra IBKR y se guarda nombre/tipo/exchange en `data/symbols.json`.

### 7.4 Setups y cálculo de niveles
- **Initial Balance (IB)**: rango de la primera hora de NY (9:30–10:30 ET / 7:30–8:30 CST). El IB midpoint es la referencia de stop.
- **VWAP**: filtro de tendencia obligatorio (largo solo sobre VWAP, corto solo bajo VWAP).
- **PDH/PDL**: máximos/mínimos del día previo como "muros".
- **Setups**: breakout confirmado, fade de falso rompimiento y sweep de muros.
- **MNQ** (`calculate_mnq_setup`, `services/agent_tools.go:265-330`):
  - `breakout` → entrada en precio actual, SL = PDL o `precio − 2*ATR14`, TP = PDH o `precio + (PDH−PDL)*0.5`, R:R calculado.
  - `vwap_reclaim` → entrada en SMA20, SL ± 1 desviación, TP = SMA, R:R ~1.5.
  - Rebote en extremos → entrada en PDH/PDL, SL en mid del rango, TP a 0.5 del rango, R:R 2.0.
- **Scalping (THE SCALPER)**: velas de 1 min, micro-rupturas de 3–5 velas, SL máx. 15 pts, TP 20–25 pts, time-stop 3 min, máx. 10 scalps/sesión, parar tras 3 pérdidas seguidas y nunca > 5 min en posición.

### 7.5 Personas (agentes) — `data/agent-config.json`
| ID | Nombre | Rol |
|---|---|---|
| `ceo` **(activo)** | THE BOSS | Portfolio manager y dispatcher; delega en scalper/risk/strategy/news vía `jim_rogers`; para MNQ responde `[BUY]/[SELL]/[WAIT]` + 3 bullets + línea `Data: …` |
| `default` | THE SCALPER | Scalping agresivo MNQ/NQ (reglas en §7.4) |
| `conservative` | THE GUARDIAN | Conservador: R:R > 3:1, 5 % por posición, 30 % desplegado, SL −10 % / TP +30 %, máx. 5 posiciones |
| `487a9f53` | MNQ SCALPER (Standalone) | Variante independiente del scalper |
| `a9533342` | NEWS ANALYST | Noticias macro y sentimiento cuantitativo |
| `stratagem` | THE STRATEGIST | Análisis técnico y detección de setups US |
| `daedalus` | THE RISK MANAGER | Veto de riesgo, time-stops 7–14 días, enforce 60 %/20 %, kill switch −5 % |

Modelo activo: `deepseek-chat`. Todos los agentes usan `strategyId: "raschke"` (el playbook `us_futures` se eliminó). Heartbeat por fase (segundos): `after_hours 2400`, `closed 14400`, `market_close 240`, `market_open 240`, `midday 1200`, `pre_market 1200`.

---

## 8. Mapa de funciones importantes

### 8.1 Arranque, wiring y configuración
| Función / símbolo | Archivo | Qué hace |
|---|---|---|
| `main()` | `cmd/bot/main.go:24` | Carga `.env`, valida config, conecta IB, crea todos los servicios y monta el router |
| `setupRouter` / `registerDashboardRoutes` | `cmd/bot/main.go` | Registro de rutas API y del dashboard |
| `config.Load()` | `config/config.go:~64` | Env → `AppConfig`; resuelve `TradingMode` (nuevo `TRADING_MODE` o legacy `TRADING_ENABLED`+`REQUIRE_DOUBLE_CONFIRM`) |
| `config.CurrentTradingMode()` | `config/config.go:138` | Relee `TRADING_MODE` en caliente (el panel de Settings hace `os.Setenv`) |
| `TradingMode.AllowsExecution()` / `RequiresDoubleConfirm()` | `config/config.go:154-161` | Reglas de qué modo puede enviar órdenes y cuál exige humano |
| `configstore.Load()` / `Get()` / `save()` | `configstore/configstore.go` | Persistencia y lectura del JSON de configuración viva |
| `configstore.GetPermissions()` / `UpdatePermissions()` | `configstore/configstore.go:660-678` | Límites de riesgo editables desde el dashboard |
| `ActiveAgentName()`, `GetActiveAgent()`, `SetActiveAgent()` | `configstore/configstore.go` | Selección de la persona activa |
| `ActiveModelEnv()`, `SetActiveModel()` | `configstore/configstore.go` | Selección del modelo LLM activo |
| `buildStrategyProjection` / `applyStrategyProjection` | `configstore/configstore.go:231-311` | Proyecta la lista del panel desde `data/strategies.json` (estrategias activas; `playbook` es opcional) |

### 8.2 Broker, datos de mercado y contrato TWS
| Función / símbolo | Archivo | Qué hace |
|---|---|---|
| `tws.NewClient` / `Connect` / `Reconnect` / `Closed` | `tws/tws_client.go` | Conexión y reconexión al IB Gateway |
| `tws.NewContractResolver` + `Resolve*` | `tws/resolver.go`, `tws/symbology.go` | Traduce símbolos (`ESTX50:20260619:C:6325`, `MNQZ6`) a contratos IB y resuelve `con_id` |
| `services.NewIBKRTradingService` | `services/ibkr_trading.go` | Implementación de `TradingService` (órdenes, posiciones, cuenta, opciones) |
| `IBKRTradingService.PlaceOrder` / `PlaceOptionsOrder` / `PlaceComboOrder` | `services/ibkr_trading.go` | Envío real de órdenes a TWS |
| `IBKRTradingService.SubscribePositions` / `OnDisconnect` / `OnReconnect` | `services/ibkr_trading.go` | Sincronización de posiciones y estado de conexión |
| `services.NewIBKRDataService` | `services/ibkr_data.go` | Implementación de `DataService` (bars, quotes, trades, news) |
| `IBKRDataService.GetHistoricalBars` / `GetLatestQuote` / `GetLatestBar` | `services/ibkr_data.go` | Datos usados por tools y análisis técnico |
| `SubscribeNews` / `NewsStream` | `services/ibkr_data.go` | Tick 292 de noticias IBKR hacia `NewsCache` |
| `buildVerifiedSnapshot` | `services/beat_snapshot.go:16` | Snapshot por beat: spot de cada símbolo de `beat.symbols` (QQQ/MNQ) con freshness + cuenta + posiciones; se reutiliza en `jim_rogers` |
| `twst/order.go`, `order_decoder.go`, `order_id_manager.go` | `tws/` | Codificación/decodificación de órdenes y gestión de `orderID` |

### 8.3 Riesgo, gates y ejecución
| Función / símbolo | Archivo | Qué hace |
|---|---|---|
| `NewGatedTradingService` | `services/gated_trading.go:22` | Envuelve el broker con el kill-switch maestro |
| `GatedTradingService.CanTrade` / `Disable` / `Enable` / `CancelAllOpenOrders` | `services/gated_trading.go:34-78` | Gate efectivo; al deshabilitar cancela órdenes abiertas |
| `validateOrderDeterministic` | `services/agent_tools.go:165` | Whitelist/blacklist + límites de pérdida + hard stop en futuros |
| `checkDailyLossLimit` / `checkWeeklyLossLimit` | `services/agent_tools.go:200-236` | Kill switch diario −5 % y freno semanal −8 % sobre el baseline persistido |
| `checkPortfolioCaps` | `services/agent_tools.go:314` | Candado real: 60 % de capital desplegado y 10 posiciones abiertas |
| `checkPerInstrumentCap` | `services/agent_tools.go:352` | Candado real: 20 % del NAV por instrumento (SPY exento) |
| `weeklyBaselineNAV` / `weeklyLossBreached` | `services/weekly_baseline.go:60-90` | Baseline semanal en `data/weekly_baseline.json`; rota solo al cambiar de semana ISO |
| `PositionManager.PlaceManagedPosition` (guards) | `services/position_manager.go:227-245` | Revalida pérdidas, 20 %, 60 % y 10 posiciones también en la aprobación humana |
| `isSymbolAllowed` / `LoadStrategiesConfig` | `services/agent_tools.go:110-145` | Lee `data/strategies.json`, aplica Observe/Beat/Operate y recarga la caché sin reiniciar |
| `StrategyConfig.BeatSymbols` / `TradingRulesPrompt` | `services/agent_tools.go:65-108` | Beat efectivo (Observe si `beat.symbols` está vacío) y playbook (solo si `trading_rules.enabled`) |
| `BuildSpread` / `CheckSpreadRisk` / `findPricedStrike` | `services/riskengine.go:67-230` | Selección determinista de strikes, coste del spread y veredicto de riesgo |
| `NewIntentManager` / `CreateIntent` / `ClaimForExecution` / `RejectIntent` / `sweep` | `services/intent_manager.go` | Cola de intenciones con TTL y un único aprobador |
| `NewPositionManager` / `PlaceManagedPosition` | `services/position_manager.go` | Abre posiciones con SL/TP automáticos |
| `PositionManager.MonitorPositions` / `checkPositions` | `services/position_manager.go` | Bucle de vigilancia (SL, TP, trailing, time-stop) |
| `manageRiskOrders` / `updateTrailingStop` / `placePartialExitOrder` | `services/position_manager.go` | Gestión dinámica de la orden de protección |
| `CloseManagedPosition` / `ListManagedPositions` / `GetManagedPosition` | `services/position_manager.go` | Cierre y consulta de posiciones gestionadas |
| `NewSuggestionManager` / `CreateSuggestion` / `AcceptSuggestion` / `ReconcileOutcomes` | `services/suggestion_manager.go` | Modo `suggest`: propuestas, aceptación y track-record |
| `ActivityLogger.LogActivity` / `StartSession` / `EndSession` | `services/activity_logger.go` | Bitácora diaria en `activity_logs/` |
| `ReserveDailyTrade` / `ReleaseDailyTrade` / `shouldTimeStop` | `services/trading_limits.go` | Cap diario de operaciones y time stop |
| `PositionManager.enforceTimeStop` | `services/position_manager.go:448` | Time stop 60 min → crea intención de liquidación y pide aprobación |
| `PositionManager.AdoptPosition` | `services/position_adopt.go` | Adopta una posición existente de IBKR con stop real y SL/TP gestionados |
| `backtest.Run` / `GenerateSignals` | `backtest/backtest.go` | Backtester nativo en Go (raschke, mean_reversion, fit_mom, turtle) |
| `StartDailyReport` / `buildDailyReport` | `services/daily_report.go` | Resumen diario por Telegram (16:05 por defecto) |
| `RotateLogIfNeeded` | `services/log_rotation.go` | Rota `bot.log` a `bot.log.1` si supera 10 MB |
| `ComputeRiskMetrics` | `services/risk_metrics.go` | VaR 95/99, volatilidad ATR, exposición, HHI, drawdown y equity curve |
| `RegisterCurrentModel` / `ListModelVersions` / `ActivateModelVersion` | `services/model_registry.go` | Registro, listado y rollback de modelos en `data/models/` |
| `RunLabScript` / `LabStatusSnapshot` | `services/lab_runner.go` | Ejecuta scripts del Lab desde el panel y reporta el log |
| `strategyEngine` / `ActiveStrategyIDs` | `services/agent_tools.go:126-174` | Motor por estrategia (campo `engine`) y activas (`enabled`) |
| `JournalTradingService.PlaceOrder` / `ReconcilePendingOrders` | `services/order_journal.go` | Idempotencia (`ClientOrderID`) y reconciliación con el broker al arrancar |
| `ValidateOptionsOrder` / `ValidateComboOrder` / `OptionExpiryDate` | `services/options_guard.go` | Valor real de opciones (×100), `maxOrderValue` y 0DTE local |
| `GetSimilarTrades` / `LogTradeResultWithContext` | `services/agent_memory.go`, `services/memory_similarity.go` | Memoria que recuerda trades parecidos (tokens + RSI/ATR) |

### 8.4 Agente, LLM y orquestación
| Función / símbolo | Archivo | Qué hace |
|---|---|---|
| `NewAutonomousBeat` | `services/autonomous_beat.go:68` | Crea el ciclo autónomo y encadena LLM primario (DeepSeek/Anthropic) → Gemini → Claude |
| `AutonomousBeat.Start` | `services/autonomous_beat.go:114` | Lanza `Run` (el playbook ya no se carga desde disco en cada arranque) |
| `AutonomousBeat.Run` / `tick` | `services/autonomous_beat.go:175-360` | Cada tick arma el prompt, ejecuta tools y decide (incluye gate de modo) |
| `AutonomousBeat.InjectMessage` / `InjectDirectMessage` | `services/autonomous_beat.go` | Inyecta feedback humano o del sistema en el próximo ciclo |
| `AutonomousBeat.Stats` / `BeatCount` / `IsPaused` | `services/autonomous_beat.go` | Métricas para el dashboard y el SSE |
| `BuildAgentTools` | `services/agent_tools.go:231` | Declara el catálogo de tools según el modo |
| `ExecuteAgentTool` / `HandleToolCall` | `services/agent_tools.go` | Router de tools y lógica de cada una |
| `predictWithML` | `services/agent_tools.go:147-163` | Llama a `predict.py` con 6 features y devuelve 0/1 |
| `underlyingSymbol` / `requireFreshSpot` | `services/agent_tools.go` | Normaliza subyacente y exige spot fresco antes de operar |
| `NewMultiLLMClient` / `GenerateResponse` | `services/multi_llm_client.go` | Fallback en cadena entre proveedores |
| `NewAnthropicClient` | `services/anthropic_client.go` | Cliente Anthropic/DeepSeek (`ANTHROPIC_API_KEY` o `ANTHROPIC_AUTH_TOKEN`) |
| `NewGeminiClient` / `NewGeminiService` | `services/gemini_client.go`, `gemini_service.go` | Cliente Gemini y también cliente DeepSeek nativo (`DEEPSEEK_API_KEY`) |
| `agent_memory.SaveMemory` / `GetMemory` / `LogTradeResult` | `services/agent_memory.go` | Memoria de trades (base de `get_memory`/`save_memory`) |
| `NewsService` / `NewsCache.Start` / `PushLiveNews` | `services/news_service.go`, `news_cache.go` | Noticias y resumen/limpieza con LLM |
| `NewEconomicFeedsService` | `services/economic_feeds.go` | Calendario macro y feeds (GDELT) |
| `NewTechnicalAnalysisService` + `CalculateSMA/RSI/MACD` | `services/technical_analysis.go` | Indicadores deterministas |
| `NewStockAnalysisService` | `services/stock_analysis_service.go` | Análisis fundamental/técnico asistido por LLM |
| `NewBookmapService` | `services/bookmap_service.go` | Integración con order-flow externo |
| `SendTelegramAlert` | `services/telegram_service.go` | Alertas Telegram |

### 8.5 Modelo ML (XGBoost)
| Función / archivo | Qué hace |
|---|---|
| `predict.py` | Carga `model.json`, recibe 6 features por `argv` y devuelve `0` (perder) o `1` (ganar) |
| `model_info.py` | Devuelve la importancia de cada feature en % |
| Features (orden fijo) | `SMA-20`, `ATR-14`, `RSI-14`, `Dist PDH`, `Dist PDL`, `Volatilidad` |
| `services/agent_tools.go:predictWithML` | Invoca el script con `C:\Users\50688\AppData\Local\Python\pythoncore-3.15-64\python.exe` y `cwd` del proyecto |
| `auto_retrain.bat` | Reentrena en `Backtest_Lab\` (junto al bot) y copia el `model.json` resultante |

### 8.6 Persistencia (SQLite)
| Función | Archivo | Tabla / uso |
|---|---|---|
| `NewLocalStorage` | `database/storage.go` | Abre `data/prophet_trader.db` (GORM, auto-migrate) |
| `SaveBars` / `GetBars` | `database/storage.go` | Histórico OHLCV local |
| `SaveOrder` / `GetOrder` / `GetOrders` | `database/storage.go` | Órdenes |
| `SaveManagedPosition` / `GetManagedPosition` / `GetAllManagedPositions` | `database/storage.go` | Posiciones gestionadas con SL/TP |
| `SaveSuggestion` / `UpdateSuggestionStatus` / `UpdateSuggestionOutcome` | `database/storage.go` | Sugerencias y track-record |
| `SaveAccountSnapshot` / `SaveSignal` | `database/storage.go` | Snapshots de cuenta y señales |

### 8.7 HTTP (`controllers/`)
| Controlador | Responsabilidad |
|---|---|
| `order_controller.go` | Cuenta, posiciones, órdenes, quotes/bars, opciones; `Buy/Sell/QuickBuy/QuickSell` |
| `position_controller.go` | CRUD de posiciones gestionadas |
| `intent_controller.go` | Listar/autorizar/rechazar intenciones; **aquí está el bearer token y el stale-price guard** |
| `suggestion_controller.go` | Sugerencias, historial y track-record |
| `beat_controller.go` | Start/stop/status del ciclo autónomo, mensajes y stream de logs |
| `dashboard_controller.go` | SSE `/api/events`, estado del dashboard, heartbeat, `/api/env`, `/api/health` |
| `config_controller.go` | Agentes, estrategias, modelos, heartbeat, permisos, plugins |
| `activity_controller.go` | Consulta y registro de `activity_logs/` |
| `news_controller.go` | Noticias por tema, búsqueda, MarketWatch |
| `economic_feeds_controller.go` | Calendario y feeds macro |
| `intelligence_controller.go` | Inteligencia de mercado (análisis + noticias + sentimiento) |
| `tradingview_controller.go` | Webhook `POST /api/webhook/tradingview` |
| `auth.go` | `RequireAdminToken` + `AdminAuthMiddleware`: bearer token obligatorio en rutas que cambian estado; falla cerrado si `ADMIN_TOKEN` está vacío |
| `risk_controller.go` | `GET /api/risk/metrics` (VaR, exposición, drawdown, equity curve) |

---

## 9. Catálogo de herramientas del agente (`BuildAgentTools`)

**Cuenta y posiciones:** `get_account`, `get_positions`, `get_options_positions`.

**Estrategia:** `get_active_strategies`, `check_strategy`.

**Ejecución:** `place_managed_position`, `liquidate_position`, `place_standalone_order`, `cancel_order`, `place_options_order`, `place_combo_order`.

**Datos:** `get_quote`, `get_options_chain`, `search_contract`, `build_spread`.

**Análisis MNQ/US:** `get_mnq_dashboard`, `get_market_regime`, `calculate_mnq_setup`, `get_correlated_assets`, `get_order_flow_imbalance`, `get_calendar_events`, `get_social_sentiment`, `get_insider_activity`.

**Orquestación y memoria:** `jim_rogers` (delegar a otro agente), `save_memory`, `get_memory`, `set_heartbeat`, `create_suggestion`.

---

## 10. Endpoints principales

| Grupo | Rutas |
|---|---|
| Salud / broker | `GET /health`, `GET /api/health`, `GET /api/v1/broker/status`, `POST /api/v1/broker/reconnect` |
| SSE / dashboard | `GET /api/events`, `GET /api/config`, `GET/POST /api/env`, `GET/PUT /api/heartbeat`, `GET/PUT /api/permissions` |
| Agente | `POST /api/agent/start`, `POST /api/agent/stop`, `GET /api/agent/status`, `POST /api/agent/message`, `GET /api/agent/stream`, `GET /api/agent/prompt-preview` |
| Intenciones | `GET /api/intents`, `GET /api/intents/history`, `POST /api/intents/authorize/:id`, `POST /api/intents/reject/:id` (alias `POST /api/v1/beat/authorize/:id`) |
| Sugerencias | `GET /api/suggestions`, `GET /api/suggestions/history`, `POST /api/suggestions/accept/:id`, `POST /api/suggestions/dismiss/:id`, `GET /api/suggestions/track-record` |
| Portafolio | `GET /api/portfolio/account`, `GET /api/portfolio/positions`, `GET /api/portfolio/orders`, `GET /api/orders/open`, `POST /api/orders/cancel-all` (token) |
| Posiciones gestionadas | `POST/GET /api/v1/positions/managed`, `GET/DELETE /api/v1/positions/managed/:id`, `POST /api/positions/adopt` (token) |
| Opciones | `GET /api/v1/options/chain/:symbol`, `GET /api/v1/options/position/:symbol` |
| Configuración viva | `/api/agents` (CRUD + activate), `/api/watchlist` (`GET`/`PUT`), `/api/symbols/search?q=`, `/api/symbols/validate?symbol=`, `/api/strategies` (CRUD), `/api/strategies/definition?id=`, `/api/models`, `/api/plugins`, `/api/heartbeat/phases`, `/api/heartbeat/apply-profile` — **las mutaciones exigen `Authorization: Bearer <ADMIN_TOKEN>`** |
| Noticias / macro | `/api/v1/news*`, `/api/v1/intelligence/*`, `/api/v1/feeds/{treasury,gdelt,bls,yfinance,usaspending,comtrade}` |
| Actividad | `/api/v1/activity`, `/api/v1/activity/:date`, `/api/v1/activity/session/start|end`, `/api/v1/activity/log` |
| ML / laboratorio | `GET /api/lab/metrics`, `GET /api/lab/scripts`, `GET /api/lab/status`, `POST /api/lab/run` (token) |
| Riesgo | `GET /api/risk/metrics` (VaR, volatilidad, exposición, drawdown y equity curve) |
| Backtest nativo | `POST /api/backtest/run` (token) — backtester Go con barras de IBKR |
| Modelos | `GET /api/models/versions`, `POST /api/models/activate-version` (token) |
| Webhook | `POST /api/webhook/tradingview` |

> CORS está abierto (`*`) en `setupRouter`. La única ruta con autenticación propia es la de autorizar intenciones (`ADMIN_TOKEN`).

---

## 11. Observaciones y deuda técnica (importante)

1. **RESUELTO (Tanda 1) — reglas unificadas.** El bot ya no lee `TRADING_RULES.md`. El playbook (IB/VWAP/PDH-PDL + sizing) vive en `data/strategies.json → trading_rules` con `"enabled": false`; solo se inyecta en el prompt si se cambia a `true`. El archivo original queda archivado en `_Archivo_Muerto/`.
2. **RESUELTO (Tanda 1/15) — ESTX50 eliminado y watchlist unificada.** `BEAT_SYMBOL` es solo respaldo; la fuente principal es `observe_symbols` (o `watchlist` legacy), con `beat.symbols` como subconjunto opcional. El prompt inyecta `AUTOMATIC WATCHLIST` y prohíbe Europa explícitamente.
3. **RESUELTO (Tanda 2, valores ajustados en Tanda 3) — límites de cartera aplicados en Go.** `checkPortfolioCaps` bloquea >60 % desplegado o >10 posiciones y `checkPerInstrumentCap` bloquea >20 % por instrumento. Se ejecutan en `validateOrderDeterministic` y en `PlaceManagedPosition` (también en la aprobación humana y en cada chunk del TWAP). Siguen siendo solo prompt: `requireConfirmation` y `maxToolRoundsPerBeat` (el bucle usa `maxTurns=10`).
4. **RESUELTO (Tanda 2) — freno semanal dinámico.** Ya no existe `33554.42`; el baseline se guarda por semana ISO en `data/weekly_baseline.json` y el umbral se lee de `permissions.maxWeeklyLoss` (8 %).
5. **RESUELTO (Tanda 3/4/7/15) — fuente única de estrategias y permisos.** `agent-config.json` ya no define estrategias: `configstore` proyecta la lista desde `data/strategies.json` y el panel la muestra. El panel escribe `data/strategies.json` (atómico) vía `Add/Update/RemoveStrategy`; el nuevo panel **Watchlist** escribe `observe_symbols` + `beat.symbols` y cada estrategia usa `operate_allow` / `operate_block`. Los alias viejos se leen y se migran al guardar.
6. **Dos clientes LLM para lo mismo**: `anthropic_client.go` se usa como primario "DeepSeek" vía base URL, y `gemini_service.go` también tiene un cliente DeepSeek nativo. Tenerlo en cuenta al tocar proveedores.
7. **`_Archivo_Muerto/`** contiene el código viejo (probes, tests TWS, docs `CLAUDE.md`, `PROGRESS.md`, `SYSTEM_ARCHITECTURE.md`). Go lo ignora por el prefijo `_`, pero conviene no reutilizarlo sin revisar.
8. **Rutas duplicadas**: `/api/intents/*` y `/api/v1/beat/*` apuntan al mismo handler; `/api/broker/*` y `/api/v1/broker/*` también. Al añadir endpoints, elegir una convención.
9. **RESUELTO (Tanda 4) — ruta de Python configurable.** `PYTHON_PATH` en `.env` (usado por `predictWithML` y `/api/lab/metrics`); si falta, se usa el intérprete conocido y, si no existe, `python` del PATH (`config.defaultPythonPath`).
10. **Tests existentes** (útiles como red de seguridad): `gated_trading_test.go`, `riskengine_test.go`, `intent_manager_test.go`, `position_manager_test.go`, `autonomous_beat_test.go`, `ibkr_*_test.go`, `storage_test.go`, y en `tws/` varios (`decoder`, `encoder`, `symbology`, `order_id_manager`, `tws_client`).
11. **RESUELTO (Tanda 1) — formato del prompt de riesgo.** Los tres porcentajes (`MaxDailyLoss`, `MaxPositionPct`, `MaxDeployedPct`) son `float64` y se imprimían con `%d` → el LLM leía `%!d(float64=5)`. Ahora se usa `%.0f` y se lee `-5% of NAV`, `20%`, `60%`.
12. **RESUELTO (Tanda 4/11) — `vixCancel` y `go vet`.** La suscripción VIX se cancela con `defer` en una closure; `go vet` queda limpio en los paquetes activos. `go vet ./...` ya no incluye `_Archivo_Muerto/twscheck` porque Go ignora carpetas con prefijo `_`.
13. **RESUELTO (Tanda 4/8) — auth de configuración.** `AdminAuthMiddleware` (`controllers/auth.go`) protege `POST /api/env`, permissions, heartbeat, plugins, agents, models, estrategias, `POST /api/orders/cancel-all`, `POST /api/lab/run` y `POST /api/models/activate-version`. Falla cerrado si `ADMIN_TOKEN` está vacío; el panel usa `adminFetch` y pide el token una vez.
14. **Efecto operativo esperado (Tanda 2/3)**: con las 85 acciones de AMZN (~66 % del NAV), cualquier orden nueva que no sea cerrar queda bloqueada por el 60 % desplegado y por el 20 % por instrumento. Es el comportamiento correcto del candado; para volver a operar hay que reducir AMZN (~25 acciones) o subir los límites a propósito.
15. **RESUELTO (Tanda 10) — idempotencia y reconciliación.** Cada orden se guarda antes de enviarse (`ClientOrderID` + `SubmitState`); al arrancar, `ReconcilePendingOrders` la compara con IBKR. Entradas fail-closed, salidas/protectores fail-open.
16. **RESUELTO (Tanda 10) — valor de opciones.** `instrumentMultiplier` usa 100× para símbolos con `:`; `ValidateOptionsOrder`/`ValidateComboOrder` aplican `maxOrderValue`, `allowOptions` y 0DTE local. Si no hay precio, falla cerrado cuando `maxOrderValue > 0`.
17. **Docker preparado, no probado en este PC.** Docker no está instalado, así que `Dockerfile`/`docker-compose.yml` no se han construido. Probar con `docker compose up -d --build` (ver `DOCKER.md`).
18. **gofmt local y CRLF.** En Windows `gofmt -l` marca todos los `.go` por los saltos CRLF; `CHECK.bat` lo trata como aviso y el CI de Linux es el que valida el formato real.
19. **Memoria:** la similitud es local (tokens + RSI/ATR), no embeddings neuronales. Si se quiere similitud semántica real, se puede añadir Gemini/ONNX en una fase posterior.

---

## 12. Cómo mantener este documento

Actualizar `projectbrief.md` **cuando**:

- Se añada/elimine un servicio en `services/` o un controlador en `controllers/`.
- Cambie el flujo de ejecución (orden de gates, nuevo estado, nueva tool del LLM).
- Cambien límites de riesgo, whitelist/blacklist, agentes o el modelo LLM activo (sección 7).
- Se mueva o renombre una función listada en la sección 8 (actualizar el mapa).
- Cambie el broker, el esquema de BD, o el mecanismo de ML.

Checklist rápida de verificación tras un cambio grande:

```powershell
go build ./...          # compila todo
go vet ./...            # olores estáticos
go test ./...           # tests
```

Y revisar que sigan siendo ciertas las afirmaciones de §7.2 (valores efectivos) y §11 (observaciones abiertas).

---

## 13. Historial de cambios

### 2026-11-17 — Tanda 16 (tabla Watchlist + búsqueda)
- **Tabla:** una sola vista `Symbol | Beat | Allow (por estrategia) | Block (por estrategia)` con modo `Inherit`/`Custom`.
- **Búsqueda:** `GET /api/symbols/search?q=Dell` combina `data/symbols.json` + Yahoo Finance; `GET /api/symbols/validate?symbol=DELL` valida el contrato con IBKR.
- **Persistencia:** `data/symbols.json` guarda nombre, tipo y exchange; `/api/watchlist` devuelve y acepta `symbols`.
- **Validación:** build, tests, staticcheck, govulncheck, `node --check` y `check.ps1` **TODO OK (exit 0)**; captura de tabla revisada.

### 2026-11-17 — Tanda 15 (Observe / Operate y Watchlist)
- **Nombres canónicos:** `observe_symbols` (global), `beat.symbols` (subconjunto opcional), `operate_allow` y `operate_block` (por estrategia). Alias legacy: `watchlist`, `whitelist_symbols`, `blacklist_symbols`.
- **API:** `GET /api/watchlist` y `PUT /api/watchlist` (token) escriben Observe/Beat y permisos; `services.ReloadStrategiesConfig()` invalida la caché tras guardar.
- **Panel:** pestaña **Watchlist** con Observe, Beat scope y resumen Operate; editor de estrategia con labels Operate allow/block.
- **Regla:** `operate_allow` vacío hereda Observe; una lista no vacía debe ser subconjunto de Observe; Operate block gana siempre.
- **Validación:** build, tests de configstore/services/controllers, `node --check` y `check.ps1` **TODO OK (exit 0)**.

### 2026-11-16 — Tanda 14 (landing page + Observatory)
- **`agent/public/landing.html`**: landing propia con hero, preview del Observatory, setup en 3 pasos y CTA; sin assets de terceros.
- **`cmd/bot/main.go`**: rutas `GET /landing`, `GET /welcome` y `GET /observatory` (redirige a `/?tab=observatory`).
- **`agent/public/index.html`**: el dashboard interpreta `?tab=...`; Observatory añade sub-pestaña Manager y enlace a la landing desde About.
- **Validación**: `go build ./cmd/bot` y `node --check` **OK**; capturas headless desktop/móvil revisadas y `check.ps1` **TODO OK (exit 0)**. Pendiente prueba de rutas con el servidor real.

### 2026-11-16 — Tanda 13 (Observatory UI)
- **`agent/public/index.html`**: nueva pestaña `Observatory` con stage central, grid, estado vacío, Manager, prompt y chips de investigación.
- **Integración**: reutiliza `POST /api/agent/message` y los eventos SSE existentes; no hay cambios de backend. El Terminal anterior se conserva como pestaña separada.
- **Validación**: `node --check` del JS **OK**, captura headless revisada y `check.ps1` **TODO OK (exit 0)**. Pendiente prueba con bot/IBKR real.
- **Siguiente**: historial por agente y navegación tipo workspace (History/Portfolio) si se decide continuar.

### 2026-11-16 — Ajuste de cadencia de validación
- **`.clinerules`**: `check.ps1` solo al cerrar cambios de código o antes de operar en paper; no para documentación ni preguntas simples.
- **Documentación**: `update_docs.ps1` refresca el estado sin ejecutar tests.
- **Validación:** cambio solo de reglas/documentación; no se ejecutó `CHECK.bat`.

### 2026-11-16 — Tanda 12 (documentación automática)
- **`HANDOFF.md`**: documento de traspaso para chats nuevos, con estado, hecho, pendiente, backups, comandos y prompt listo.
- **`.clinerules`**: reglas permanentes para que Cline lea el HANDOFF y actualice la documentación al cerrar cada tarea.
- **`update_docs.ps1` + `UPDATE_DOCS.bat`**: refrescan fecha, Go, check, bot, disco y backup en los bloques `AUTO_STATUS` de `HANDOFF.md` y `projectbrief.md`.
- **`check.ps1`**: escribe `last_check.json` y llama a `update_docs.ps1` al terminar (pase o falle).
- **Validación:** `check.ps1` **TODO OK (exit 0)**; `last_check.json` y el bloque automático quedaron actualizados.

### 2026-11-16 — Tanda 11 (herramientas, dependencias y Docker base)
- **Go 1.27.4** (`toolchain go1.27.4`, módulo `go 1.26.0`) para corregir la vulnerabilidad de `net/http` de 1.27.3.
- **Dependencias actualizadas**: `golang.org/x/net v0.60.0`, `golang.org/x/text v0.42.0`, `google.golang.org/grpc v1.83.1` (y arrastres de `x/crypto`, `x/sys`, `otel`).
- **`govulncheck`**: 0 vulnerabilidades que afecten al código (antes 9).
- **Herramientas instaladas**: `staticcheck 2026.2.2` y `govulncheck v1.9.0` en `C:\Users\50688\go\bin`; `CHECK.bat` ya las ejecuta.
- **Limpieza**: `go clean -cache` (~2 GB) y backups antiguos `tanda1…tanda5`; se conserva `bot.exe.before_p0_20261116.exe`, `tanda10_post` y el backup final `_Backups/tanda11_post_20261116_211319`. La carpeta `+Backups` se renombró a `_Backups` y `+Archivo_Muerto` a `_Archivo_Muerto` para que `go`/`staticcheck` no escaneen código viejo.
- **`staticcheck`**: limpio. Se corrigieron 11 avisos (comparación siempre verdadera, errores con puntuación/mayúscula, `fmt.Sprintf` innecesario, `idx++` sin uso, funciones muertas y `TrimSuffix`).
- **Docker/CI**: base `golang:1.27-bookworm` y CI con Go 1.27.4. Docker sigue sin instalarse en este PC (~2 GB libres, necesita 8–10 GB y WSL2).
- **Validación**: build OK, tests OK, `govulncheck` 0, `CHECK.bat` **TODO OK (exit 0)**; `bot.exe` recompilado con `go1.27.4`.

### 2026-11-16 — Tanda 10 (P0 + CI + memoria + Docker)
- **Idempotencia y reconciliación**: `JournalTradingService` guarda cada intención con `ClientOrderID`/`SubmitState` antes de enviarla; `ReconcilePendingOrders` compara con IBKR al arrancar. Entradas fail-closed; salidas/protectores fail-open.
- **Opciones**: multiplicador 100× en `instrumentMultiplier`; `ValidateOptionsOrder`/`ValidateComboOrder` aplican `maxOrderValue` real, `allowOptions` y 0DTE en fecha local (fail-closed si no hay precio).
- **Memoria**: `LogTradeResultWithContext` + `GetSimilarTrades` (tokens/Jaccard + RSI/ATR); `get_memory` acepta `context`, `symbol`, `setup`, `rsi`, `atr_percent`.
- **CI local**: `CHECK.bat` + `check.ps1` (gofmt, vet, test -race, staticcheck, govulncheck, UI) y `.github/workflows/ci.yml` + Dependabot para el futuro.
- **Docker**: `Dockerfile`, `docker-compose.yml`, `.dockerignore`, `DOCKER.md` (no probado en este PC porque Docker no está instalado).
- **Tests nuevos**: journal (3), opciones (2) y similitud (1).
- **Validación**: build/test/vet OK; `CHECK.bat` ejecutado.

### 2026-11-16 — Tanda 9 (Bloque C)
- **Adoptar posición existente (8)**: `PositionManager.AdoptPosition` + `POST /api/positions/adopt` + botón "Adopt Position" en Trades. Crea un `ManagedPosition` ACTIVE con SL/TP sobre una posición que ya está en IBKR, coloca un **stop real** en el broker y no abre una entrada nueva.
- **Backtester nativo en Go (10)**: nuevo paquete `backtest/` (raschke, fit_mom, mean_reversion, turtle_breakout) con comisiones, slippage, time stop por barras, drawdown y Sharpe. Endpoint `POST /api/backtest/run` + formulario "Native Go Backtest" en Lab.
- **`mean_reversion`**: el bot la opera solo en `1Day`, pero el backtest Python la prueba en **todos** los timeframes (se quitó la restricción `timeframes: ["1d"]`).
- **Tests nuevos**: `backtest/backtest_test.go` (resultado saneado y señal de sobreventa).
- **Validación**: build/test/vet OK, `node --check` OK.

### 2026-11-16 — Tanda 8 (Bloque B)
- **Time stop 60 min con aprobación humana**: `PositionManager.enforceTimeStop` crea una intención de liquidación (`notes: LIQUIDATION`) y avisa por Telegram; nunca cierra solo. `SetIntentManager` conecta la cola de aprobación.
- **`mean_reversion` solo `1Day` para operar**: `"timeframe": "1Day"` en `strategies.json`; el backtest la sigue probando en todos los timeframes.
- **Registro de modelos**: `services/model_registry.go` + `data/models/registry.json`; endpoints `/api/models/versions` y `/api/models/activate-version`; sección "Model Versions" en Lab.
- **Riesgo/equity (5 y 12)**: `services/risk_metrics.go` + `RiskController` + `/api/risk/metrics` (VaR 95/99, volatilidad ATR, exposición, mayor posición, HHI, drawdown y curva de equity) y gráfico en Risk.
- **Lab runner (6)**: `services/lab_runner.go` + `/api/lab/scripts|status|run` + botones y log en vivo en Lab.
- **Motor por estrategia (7)**: campo `engine` en `strategies.json` y selector en el panel; una estrategia nueva puede reutilizar `raschke`/`mean_reversion`/etc. `ActiveStrategyIDs` incluye las estrategias con `enabled: true`.
- **Tests nuevos**: risk metrics, model registry, lab runner y engine (6 casos).
- **Validación**: build/test/vet OK y `node --check` del panel OK.

### 2026-11-16 — Tanda 7 (playbook fuera + mean_reversion en el Lab)
- **Eliminado el playbook `us_futures`** de `data/strategies.json`; los 7 agentes de `agent-config.json` usan `strategyId: "raschke"`. El panel ya no muestra "US Futures Multi-Index".
- **`Backtest_Lab/backtest_lab.py`**: nueva `strat_mean_reversion` (SMA20 ± 2 desviaciones + RSI14, long < banda inferior con RSI<30, short > banda superior con RSI>70) y entrada en `config_backtest.json`.
- **Resultados previos del Lab**: `raschke` fue el mejor (Sharpe 4.6–8.8 según timeframe); `turtle_breakout` menor; `combined` 0 trades; `NQ=F` 0 trades (problema de datos de futuros en yfinance).
- **Fix pandas 3.1** en `df.index.date` (normalización de índice con `pd.to_datetime(..., utc=True)` y sin tz).

### 2026-11-16 — Tanda 6 (operación + ejecución)
- **Límite real de operaciones nuevas/día**: `global_filters.max_trades_per_day` (2) con contador atómico en `data/daily_trades.json`; los chunks del TWAP no consumen slots extra.
- **Time stop real**: `global_filters.time_stop_minutes` (hoy 60) en `PositionManager.enforceTimeStop`; desde la Tanda 8 **no cierra solo**: crea una intención de liquidación y pide aprobación.
- **Reporte diario por Telegram**: `DAILY_REPORT_ENABLED=true`, `DAILY_REPORT_TIME=16:05` (`services/daily_report.go`).
- **Rotación de `bot.log`** (>10 MB al arrancar) y **`WATCHDOG.bat`** para reiniciar el bot si se cae.
- **`GET /api/orders/open`** y **`POST /api/orders/cancel-all`** (token) + botón "Cancel All Open Orders" en Trades.
- **Fix pandas 3.1** en `Backtest_Lab` (`df.index.date` con `pd.to_datetime`).
- **Tests nuevos**: `services/trading_limits_test.go` (cap diario, release, cambio de día, time stop, rotación de log).

### 2026-11-16 — Tanda 5 (laboratorio integrado)
- **`Backtest_Lab/` movido dentro de OpenProphet** (43 archivos, 11.9 MB). El original quedó renombrado como `C:\Users\50688\Backtest_Lab_OLD_20261116` (reversible).
- **`auto_retrain.bat`** usa rutas relativas (`%~dp0Backtest_Lab`) y lee `PYTHON_PATH` del `.env`; ya no depende de rutas absolutas.
- **`RUN_LAB.bat`** nuevo: menú para reentrenar, validar Raschke, correr backtests y abrir la carpeta.
- **`LAB_DIR=./Backtest_Lab`** en `.env`; `/api/lab/metrics` lee `Backtest_Lab/retrain_log.txt`.
- **Validado**: Python + pandas/numpy/yfinance/xgboost/sklearn/scipy funcionan desde la nueva carpeta; build/tests/vet OK.

### 2026-11-16 — Tanda 4 (fase 2 + cierre de Tanda 3)
- **Fase 2 de unificación**: `AddStrategy` / `UpdateStrategy` / `RemoveStrategy` escriben `data/strategies.json` de forma atómica y refrescan la proyección; `GetStrategyDefinition` + `GET /api/strategies/definition?id=` alimentan el editor.
- **Panel**: editor de estrategias (nombre, descripción, enabled, whitelist, blacklist, min R:R, max risk). El playbook `us_futures` existía entonces y era de solo lectura (se eliminó en la Tanda 7); las estrategias nuevas se crean desactivadas; borrar una activa se rechaza.
- **Seguridad**: `controllers/auth.go` + `AdminAuthMiddleware` en `POST /api/env`, permissions, heartbeat, plugins, agents, models, estrategias y start/stop del agente. UI con `adminFetch` y prompt único de token.
- **Python**: `PYTHON_PATH` en `.env`/`.env.example`; `predictWithML` y `/api/lab/metrics` lo usan con fallback a `python`.
- **vixCancel**: cerrado con `defer` en una closure → `go vet` limpio en paquetes activos.
- **Tests nuevos**: 4 de escritura de estrategias (`configstore/strategies_write_test.go`) + 2 de auth (`controllers/auth_test.go`). Total: 15 pruebas en verde.
- **Validación**: `go build` OK, `go test` OK, `go vet` OK, `node --check` del script del panel OK.
- **Backup**: `+Backups/tanda3_20261116_173319/` (estado previo de JSON/Go) y `+Backups/tanda4_post_*` (estado actual).

### 2026-11-16 — Tanda 3 (aplicada)
- **Límites nuevos**: 20 % por instrumento, 60 % desplegado, 10 posiciones. Actualizados en `data/agent-config.json`, `configstore.DefaultPermissions`, fallbacks de `services/agent_tools.go` y prompts de THE BOSS / THE RISK MANAGER.
- **Unificación fase 1**: `data/strategies.json` es la única fuente. Nuevo bloque `playbook` (`id: us_futures`) y `name` por estrategia. Se eliminó el array `strategies` de `data/agent-config.json`.
- **configstore**: `buildStrategyProjection` + `applyStrategyProjection`; `saveLocked` ya no persiste estrategias; `Add/Update/RemoveStrategy` devuelven error explicativo (panel = solo lectura hasta la fase 2).
- **Tests**: `configstore/strategies_projection_test.go` (4 casos) y `riskengine_test.go` ajustado al 20 %.
- **Backup**: `+Backups/tanda3_20261116_173319/` (incluye `bot.exe` previo).
- **Pendiente fase 2**: que el panel escriba de verdad en `data/strategies.json`.

### 2026-11-16 — Tanda 2 (aplicada)
- **Candados reales de cartera** en `services/agent_tools.go`:
  - `checkPortfolioCaps`: bloquea > `maxDeployedPct` (40 %) o > `maxOpenPositions` (10).
  - `checkPerInstrumentCap`: bloquea > `maxPositionPct` (15 %) por símbolo (SPY exento).
  - `orderNotional`, `grossExposure`, `openPositionCount`, `holdsSymbol`, `instrumentMultiplier` como helpers deterministas.
- **Doble punto de control**: `validateOrderDeterministic` y `PositionManager.PlaceManagedPosition` (cierra el bypass de la aprobación humana y del TWAP). El TWAP ahora valida la orden completa antes de partirla en chunks.
- **Freno semanal dinámico**: nuevo `services/weekly_baseline.go` + `data/weekly_baseline.json`; umbral en `permissions.maxWeeklyLoss` (8 %). Eliminado el `33554.42` hardcodeado.
- **Seguridad**: `BIND_ADDR=127.0.0.1` (solo local), CORS restringido a `localhost`/`127.0.0.1`/`::1`, `ADMIN_TOKEN` regenerado a 64 hex aleatorios (posteriormente revertido a un valor local débil a petición del usuario; riesgo aceptado porque el panel ya solo escucha en localhost).
- **Config**: `Permissions.MaxWeeklyLoss`; `DefaultPermissions.MaxDeployedPct` corregido 80 → 40; `data/agent-config.json` con `"maxWeeklyLoss": 8`.
- **Tests nuevos** `services/guards_test.go` (5 casos: exposición bruta, conteo/tenencia, notional, freno semanal, ciclo del baseline). Total Tanda 1+2: 9 tests en verde.
- **Backup**: `+Backups/tanda2_20261116_170944/` (incluye `bot.exe` previo).
- **Validación**: `go build` OK, `go test ./services/` OK, `go vet` solo deja `vixCancel` (preexistente).

### 2026-11-16 — Tanda 1 (aplicada)
- **Reglas unificadas** en `data/strategies.json` (schemaVersion 4):
  - `trading_rules` (mandato de mercado, setups IB/VWAP/PDH-PDL, gestión de riesgo) con `"enabled": false` → documentadas, no aplicadas.
  - `beat.symbols = ["QQQ","MNQ"]` → fuente principal de la vigilancia.
- **`services/agent_tools.go`**: nuevos tipos `BeatConfig` / `TradingRulesConfig` y métodos `BeatSymbols()` / `TradingRulesPrompt()` / `cleanSymbols()`.
- **`services/autonomous_beat.go`**: se eliminó `cachedRules` y la lectura de `TRADING_RULES.md`; el prompt usa `trading_rules` (si está activo), inyecta `ACTIVE STRATEGIES` y `AUTOMATIC WATCHLIST`, y ya no menciona `fit_mom`/`MNQZ6` como ejemplo.
- **Fix de formato**: `MaxDailyLoss`, `MaxPositionPct`, `MaxDeployedPct` pasan de `%d` a `%.0f`.
- **ESTX50 retirado** de `.env`, `.env.example`, `config/config.go`, `data/agent-config.json` (`rulesFile`) y `configstore/configstore.go`.
- **`services/beat_snapshot.go`**: el snapshot verificado ya no está clavado a ESTX50; recorre `beat.symbols` (QQQ, MNQ) y publica una línea de spot con freshness por símbolo.
- **`build_spread`**: ahora exige `underlying` explícito y rechaza `ESTX50`/`OESX` (mercados europeos).
- **Test nuevo** `services/strategies_config_test.go` (4 casos: reglas apagadas no filtran, reglas activas renderizan, prioridad de watchlist, JSON válido y sin ESTX50).
- **Backup**: `+Backups/tanda1_20261116_165121/`.
- **Validación**: `go build` OK, `go test ./services/` OK, `go vet` solo deja el aviso de `vixCancel` (preexistente).

---

## 14. Resultados del laboratorio (2026-11-16)

- **`validate_raschke.py`** (QQQ 15m): 1a Sharpe 10.23 / 1b 12.15 / 1c 12.18 con 518 trades y WR 76.1 %; short-only 1e Sharpe 10.16; bear markets 2000-02, 2007-09, 2022 y COVID positivos. En diario, otros activos dan Sharpe 2.2–4.9 (AAPL/NVDA con WR 28–34 %).
- **Lectura honesta**: el edge parece simétrico y no exclusivo de bull market, pero un Sharpe >10 en 15m es **sospechosamente alto**; el script solo aplica slippage de salida y no comisiones completas. Antes de confiar, hay que añadir comisiones/slippage de entrada y una prueba out-of-sample.
- **`backtest_lab.py` (última corrida, 5 estrategias)**: `raschke` domina (QQQ 5m: 2036 trades, WR 69.9 %, Sharpe 8.83; 1h: 2360 trades, WR 69.2 %, Sharpe 7.55; 1d: 3265 trades, WR 68.2 %, Sharpe 4.62). `turtle_breakout` queda por debajo; `combined` no genera trades; `NQ=F` devuelve 0 trades (datos de futuros no disponibles en yfinance).
- **`mean_reversion` (solo 1d)**: QQQ 1d 192 trades, WR 45.8 %, Sharpe 1.62, retorno +10.84 %, DD −5.48 %, DSR 0.22; en 5m/1m el Sharpe cae a 1.03/0.49; en 1h/30m/15m es negativo. No es un edge fuerte; mantenerla solo en diario o desactivarla.
- **Fix aplicado**: pandas 3.1 rompía `df.index.date`; ahora se normaliza el índice con `pd.to_datetime(..., utc=True)` y se quita la zona horaria.

