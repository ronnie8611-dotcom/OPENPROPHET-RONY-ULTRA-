package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"prophet-trader/config"
	"prophet-trader/configstore"
	"prophet-trader/controllers"
	"prophet-trader/database"
	"prophet-trader/interfaces"
	"prophet-trader/services"
	"prophet-trader/tws"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
)

func main() {
	// [MEJORA] Cargar archivo .env si existe
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or could not be loaded. Using system environment variables.")
	}

	// [OPTIMIZACIÓN] Modo Release para Gin (Silencia el ruido de peticiones HTTP en consola)
	gin.SetMode(gin.ReleaseMode)

	if err := config.Load(); err != nil { log.Fatal("Failed to load configuration:", err) }
	cfg := config.AppConfig

	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{ FullTimestamp: true })
	if cfg.EnableLogging {
		level, _ := logrus.ParseLevel(cfg.LogLevel)
		logger.SetLevel(level)
	}

	if err := configstore.Load("data/agent-config.json"); err != nil {
		log.Printf("Warning: failed to load agent config: %v (using defaults)", err)
	}
	configstore.SyncRuntimeEnv()
	services.Hub = services.NewSSEHub()

	logger.Info("Starting Prophet Trader Bot...")
	logger.Info("Initializing services...")

	services.RotateLogIfNeeded("bot.log", 10*1024*1024)
	if v, err := services.RegisterCurrentModel("model.json"); err != nil {
		logger.WithError(err).Warn("Could not register current ML model")
	} else if v != nil {
		logger.WithFields(logrus.Fields{"file": v.File, "hash": v.Hash[:8]}).Info("ML model registered")
	}

	var tradingService interfaces.TradingService
	var dataService interfaces.DataService

	if cfg.IBKRPort == 4001 && (!cfg.RequireDoubleConfirm || cfg.AdminToken == "" || !cfg.AllowLivePort) {
		logger.Fatalf("FATAL: Unattended live execution prohibited. Port 4001 requires ALLOW_LIVE_PORT=true, REQUIRE_DOUBLE_CONFIRM=true, and a valid ADMIN_TOKEN.")
	} else if cfg.IBKRPort != 4002 && cfg.IBKRPort != 4001 {
		logger.Fatalf("IBKR_PORT=%d refused — only 4002 (paper) or 4001 (live, guarded) are permitted.", cfg.IBKRPort)
	}
	logger.WithFields(logrus.Fields{ "host": cfg.IBKRHost, "port": cfg.IBKRPort, "clientID": cfg.IBKRClientID }).Info("Connecting to IB Gateway...")

	client := tws.NewClient(cfg.IBKRHost, cfg.IBKRPort, cfg.IBKRClientID)
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := client.Connect(connectCtx); err != nil {
		connectCancel()
		logger.Fatal("Failed to connect to IB Gateway:", err)
	}
	connectCancel()
	logger.Info("Connected to IB Gateway.")

	resolver := tws.NewContractResolver(client)
	ibkrTrading := services.NewIBKRTradingService(client, resolver)
	gated := services.NewGatedTradingService(ibkrTrading, true)
	tradingService = gated
	
	ibkrDataInstance := services.NewIBKRDataService(client, resolver)
	dataService = ibkrDataInstance
	ibkrTrading.SetDataService(dataService)
	ibkrTrading.SubscribePositions()

	// Auto-reconnect
	go func() {
		backoff := 5 * time.Second
		const maxBackoff = 5 * time.Minute
		for {
			<-client.Closed()
			gated.Disable("IB Gateway connection closed")
			logger.Error("IB Gateway disconnected — attempting reconnect...")
			ibkrDataInstance.OnDisconnect()
			ibkrTrading.OnDisconnect()
			for {
				time.Sleep(backoff)
				logger.WithField("backoff", backoff).Info("Reconnecting to IB Gateway...")
				rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
				err := client.Reconnect(rctx)
				rcancel()
				if err != nil {
					logger.WithError(err).Error("Reconnect failed")
					backoff = backoff * 2
					if backoff > maxBackoff { backoff = maxBackoff }
					continue
				}
				logger.Info("Reconnected to IB Gateway successfully")
				ibkrDataInstance.OnReconnect()
				ibkrTrading.OnReconnect()
				gated.Enable("IB Gateway reconnected")
				backoff = 5 * time.Second
				break
			}
		}
	}()

	storageService, err := database.NewLocalStorage(cfg.DatabasePath)
	if err != nil { logger.Fatal("Failed to create storage service:", err) }

	// Idempotency: journal every order intent before it reaches the broker.
	journalTrading := services.NewJournalTradingService(tradingService, storageService, logger)
	tradingService = journalTrading
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := journalTrading.ReconcilePendingOrders(reconcileCtx); err != nil {
		logger.WithError(err).Warn("Order journal reconciliation failed")
	}
	reconcileCancel()

	orderController := controllers.NewOrderController(tradingService, dataService, storageService)
	riskController := controllers.NewRiskController(tradingService, dataService, storageService)
	backtestController := controllers.NewBacktestController(dataService)
	newsService := services.NewNewsService()
	newsController := controllers.NewNewsController(newsService)
	economicFeedsService := services.NewEconomicFeedsService()
	economicFeedsController := controllers.NewEconomicFeedsController(economicFeedsService)
	geminiService := services.NewGeminiService(cfg.GeminiAPIKey)
	analysisService := services.NewTechnicalAnalysisService(dataService)
	stockAnalysisService := services.NewStockAnalysisService(dataService, newsService, geminiService)
	intelligenceController := controllers.NewIntelligenceController(newsService, geminiService, analysisService, stockAnalysisService, dataService)

	logger.Info("Testing broker connection...")
	if account, err := orderController.GetAccount(); err != nil {
		logger.Warn("Failed to read account:", err)
	} else {
		logger.WithFields(logrus.Fields{ "cash": account.Cash, "buying_power": account.BuyingPower, "portfolio_value": account.PortfolioValue }).Info("Successfully read broker account")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	positionManager := services.NewPositionManager(tradingService, dataService, storageService)
	positionController := controllers.NewPositionManagementController(positionManager)
	intentManager := services.NewIntentManager(cfg.IntentTTLSeconds, logger)
	positionManager.SetIntentManager(intentManager)
	go intentManager.StartSweeper(ctx)
	suggestionManager := services.NewSuggestionManager(storageService, dataService, logger)
	go suggestionManager.StartExpirationSweeper(ctx)

	autonomousBeat := services.NewAutonomousBeat(dataService, positionManager, tradingService, logger, services.AutonomousBeatConfig{
		Interval:           time.Duration(cfg.BeatIntervalSecs) * time.Second,
		MaxDailyExecutions: cfg.BeatMaxDailyExecutions,
		LLMPollingEnabled:  cfg.LLMPollingEnabled,
		LLMPollingInterval: time.Duration(cfg.LLMPollingIntervalSecs) * time.Second,
	}, intentManager, cfg.RequireDoubleConfirm, suggestionManager, cfg.TradingMode)
	autonomousBeat.SetResolver(resolver)

	newsCache := services.NewNewsCache()
	newsCache.Start(ctx, 15*time.Minute)
	autonomousBeat.SetNewsCache(newsCache)
	logger.Info("News cache started. Listening to IBKR News Tick 292...")

	go func() {
		reqId := int64(9999)
		if err := ibkrDataInstance.SubscribeNews(reqId, "BRFUPDN"); err != nil {
			logger.WithError(err).Error("Failed to subscribe to IBKR News stream")
			return
		}
		defer ibkrDataInstance.CancelNews(reqId)

		for msg := range ibkrDataInstance.NewsStream() {
			logger.WithField("headline", msg.Headline).Info("Live News Received")
			newsCache.PushLiveNews(msg.Headline)
		}
	}()

	intentManager.SetFeedbackCallback(func(intent *services.Intent, reason string) {
		msg := fmt.Sprintf("System feedback: Your trade intent (ID: %s, Symbol: %s, Side: %s) was %s.", intent.ID, intent.Symbol, intent.Side, reason)
		autonomousBeat.InjectMessage(msg)
	})
	
	beatController := controllers.NewBeatController(autonomousBeat)
	activityLogDir := os.Getenv("ACTIVITY_LOG_DIR")
	if activityLogDir == "" { activityLogDir = "./activity_logs" }
	activityLogger := services.NewActivityLogger(activityLogDir)
	activityController := controllers.NewActivityController(activityLogger)
	intentController := controllers.NewIntentController(intentManager, positionManager, tradingService, dataService, activityLogger)
	suggestionController := controllers.NewSuggestionController(suggestionManager)

	if account, err := orderController.GetAccount(); err == nil {
		activityLogger.StartSession(ctx, account.PortfolioValue)
		logger.Info("Activity logging session started")
	}

	configController := controllers.NewConfigController()
	dashboardController := controllers.NewDashboardController(autonomousBeat, orderController, intentController)
	tradingViewController := controllers.NewTradingViewController(autonomousBeat)
	symbolController := controllers.NewSymbolController(services.NewSymbolSearcher(), resolver)

	router := setupRouter(orderController, newsController, intelligenceController, positionController, activityController, economicFeedsController, beatController, intentController)
	registerDashboardRoutes(router, configController, dashboardController, beatController, orderController, intentController, suggestionController, tradingViewController)
	router.GET("/api/symbols/search", symbolController.HandleSearch)
	router.GET("/api/symbols/validate", symbolController.HandleValidate)
	router.GET("/api/risk/metrics", riskController.HandleGetRiskMetrics)
	router.POST("/api/backtest/run", controllers.AdminAuthMiddleware(), backtestController.HandleRunBacktest)

	// === [NUEVO] ENDPOINT DEL LABORATORIO DE IA (Añadido hoy) ===
	router.GET("/api/lab/metrics", func(c *gin.Context) {
		pyPath := cfg.PythonPath
		if pyPath == "" {
			pyPath = "python"
		}
		cmd := exec.Command(pyPath, "model_info.py")
		out, err := cmd.Output()
		modelInfo := string(out)
		if err != nil { modelInfo = `{"error": "Fallo al leer el modelo"}` }
		
		logPath := filepath.Join(cfg.LabDir, "retrain_log.txt")
		logBytes, _ := os.ReadFile(logPath)
		logContent := string(logBytes)
		if len(logContent) > 2000 { logContent = logContent[len(logContent)-2000:] }

		c.JSON(200, gin.H{"model_info": modelInfo, "retrain_log": logContent})
	})
	// ==============================================================

	brokerReconnectHandler := func(c *gin.Context) {
		logger.Info("Manual reconnect requested via API")
		gated.Disable("manual reconnect requested")
		ibkrDataInstance.OnDisconnect()
		ibkrTrading.OnDisconnect()
		rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer rcancel()
		if err := client.Reconnect(rctx); err != nil {
			logger.WithError(err).Error("Manual reconnect failed")
			c.JSON(500, gin.H{"error": "reconnect failed: " + err.Error()})
			return
		}
		ibkrDataInstance.OnReconnect()
		ibkrTrading.OnReconnect()
		gated.Enable("manual reconnect succeeded")
		logger.Info("Manual reconnect succeeded")
		c.JSON(200, gin.H{"status": "reconnected"})
	}
	router.POST("/api/v1/broker/reconnect", brokerReconnectHandler)
	router.POST("/api/broker/reconnect", brokerReconnectHandler)

	brokerStatusHandler := func(c *gin.Context) {
		connected := client.IsConnected()
		c.JSON(200, gin.H{
			"connected":       connected,
			"trading_enabled": gated.CanTrade(),
			"trading_mode":    string(config.CurrentTradingMode()),
		})
	}
	router.GET("/api/v1/broker/status", brokerStatusHandler)
	router.GET("/api/broker/status", brokerStatusHandler)

	if cfg.BeatEnabled {
		logger.Warn("Autonomous beat ENABLED — Starting native AI agent in background")
		go autonomousBeat.Start()
	}

	go startDataCleanup(ctx, storageService, cfg.DataRetentionDays, logger)
	go startPositionMonitor(ctx, orderController, storageService, logger)
	go positionManager.MonitorPositions(ctx)
	go services.StartDailyReport(ctx, tradingService, logger)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdown
		logger.Info("Shutting down gracefully...")
		cancel()
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	logger.WithFields(logrus.Fields{"bind": cfg.BindAddr, "port": cfg.ServerPort}).Info("Starting HTTP server...")
	if err := router.Run(cfg.BindAddr + ":" + cfg.ServerPort); err != nil {
		logger.Fatal("Failed to start server:", err)
	}
}

func setupRouter(orderController *controllers.OrderController, newsController *controllers.NewsController, intelligenceController *controllers.IntelligenceController, positionController *controllers.PositionManagementController, activityController *controllers.ActivityController, economicFeedsController *controllers.EconomicFeedsController, beatController *controllers.BeatController, intentController *controllers.IntentController) *gin.Engine {
	router := gin.Default()

	router.Use(func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" {
			if !isLocalOrigin(origin) {
				c.AbortWithStatus(403)
				return
			}
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Vary", "Origin")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "healthy"})
	})

	api := router.Group("/api/v1")
	{
		api.POST("/orders/buy", orderController.HandleBuy)
		api.POST("/orders/sell", orderController.HandleSell)
		api.DELETE("/orders/:id", orderController.HandleCancelOrder)
		api.GET("/orders", orderController.HandleGetOrders)
		api.GET("/positions", orderController.HandleGetPositions)
		api.GET("/account", orderController.HandleGetAccount)
		api.GET("/market/quote/:symbol", orderController.HandleGetQuote)
		api.GET("/market/bar/:symbol", orderController.HandleGetBar)
		api.GET("/market/bars/:symbol", orderController.HandleGetBars)
		api.POST("/options/order", orderController.PlaceOptionsOrder)
		api.GET("/options/positions", orderController.ListOptionsPositions)
		api.GET("/options/position/:symbol", orderController.GetOptionsPosition)
		api.GET("/options/chain/:symbol", orderController.GetOptionsChain)
		api.GET("/news", newsController.HandleGetNews)
		api.GET("/news/topic/:topic", newsController.HandleGetNewsByTopic)
		api.GET("/news/search", newsController.HandleSearchNews)
		api.GET("/news/market", newsController.HandleGetMarketNews)
		api.GET("/news/marketwatch/topstories", newsController.HandleGetMarketWatchTopStories)
		api.GET("/news/marketwatch/realtime", newsController.HandleGetMarketWatchRealtimeHeadlines)
		api.GET("/news/marketwatch/bulletins", newsController.HandleGetMarketWatchBulletins)
		api.GET("/news/marketwatch/marketpulse", newsController.HandleGetMarketWatchMarketPulse)
		api.GET("/news/marketwatch/all", newsController.HandleGetAllMarketWatchNews)
		api.POST("/intelligence/cleaned-news", intelligenceController.HandleGetCleanedNews)
		api.GET("/intelligence/quick-market", intelligenceController.HandleGetQuickMarketIntelligence)
		api.GET("/intelligence/analyze/:symbol", intelligenceController.HandleAnalyzeStock)
		api.POST("/intelligence/analyze-multiple", intelligenceController.HandleAnalyzeMultipleStocks)
		api.POST("/agent/start", beatController.HandleStart)
		api.POST("/agent/stop", beatController.HandleStop)
		api.GET("/agent/status", beatController.HandleStatus)
		api.POST("/agent/message", beatController.HandleMessage)
		api.GET("/agent/stream", beatController.HandleStreamLogs)
		api.GET("/beat/intents", intentController.HandleGetIntents)
		api.GET("/beat/intents/history", intentController.HandleGetHistory)
		api.POST("/beat/authorize/:id", intentController.HandleAuthorizeIntent)
		api.POST("/beat/reject/:id", intentController.HandleRejectIntent)
		api.POST("/positions/managed", positionController.HandlePlaceManagedPosition)
		api.GET("/positions/managed", positionController.HandleListManagedPositions)
		api.GET("/positions/managed/:id", positionController.HandleGetManagedPosition)
		api.DELETE("/positions/managed/:id", positionController.HandleCloseManagedPosition)
		api.POST("/positions/adopt", controllers.AdminAuthMiddleware(), positionController.HandleAdoptPosition)
		router.POST("/api/positions/adopt", controllers.AdminAuthMiddleware(), positionController.HandleAdoptPosition)
		api.GET("/feeds/treasury", economicFeedsController.HandleGetTreasury)
		api.GET("/feeds/gdelt", economicFeedsController.HandleGetGDELT)
		api.GET("/feeds/bls", economicFeedsController.HandleGetBLS)
		api.GET("/feeds/yfinance", economicFeedsController.HandleGetYFinance)
		api.GET("/feeds/usaspending", economicFeedsController.HandleGetUSASpending)
		api.GET("/feeds/comtrade", economicFeedsController.HandleGetComtrade)
		api.GET("/activity/current", activityController.HandleGetCurrentActivity)
		api.GET("/activity/:date", activityController.HandleGetActivityByDate)
		api.GET("/activity", activityController.HandleListActivityLogs)
		api.POST("/activity/session/start", activityController.HandleStartSession)
		api.POST("/activity/session/end", activityController.HandleEndSession)
		api.POST("/activity/log", activityController.HandleLogActivity)
	}

	router.Static("/img", "./agent/public/img")
	router.StaticFile("/landing", "./agent/public/landing.html")
	router.StaticFile("/welcome", "./agent/public/landing.html")
	router.GET("/observatory", func(c *gin.Context) {
		c.Redirect(302, "/?tab=observatory")
	})

	router.NoRoute(func(c *gin.Context) {
		if c.Request.Method == "GET" && !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.File("./agent/public/index.html")
			return
		}
		c.JSON(404, gin.H{"error": "not found"})
	})

	return router
}

func registerDashboardRoutes(router *gin.Engine, cc *controllers.ConfigController, dc *controllers.DashboardController, bc *controllers.BeatController, oc *controllers.OrderController, ic *controllers.IntentController, sc *controllers.SuggestionController, tvc *controllers.TradingViewController) {
	router.GET("/api/events", dc.HandleSSEEvents)

	auth := controllers.AdminAuthMiddleware()

	router.POST("/api/agent/start", auth, bc.HandleStart)
	router.POST("/api/agent/stop", auth, bc.HandleStop)
	router.GET("/api/agent/status", bc.HandleStatus)
	router.POST("/api/agent/heartbeat", dc.HandleHeartbeat)
	router.POST("/api/agent/message", dc.HandleMessage)
	router.GET("/api/agent/prompt-preview", dc.HandlePromptPreview)

	// [NUEVA RUTA] Estrategias Performance
	router.GET("/api/strategies/performance", dc.HandleGetStrategiesPerformance)

	router.GET("/api/config", cc.HandleGetConfig)

	router.GET("/api/agents", cc.HandleGetAgents)
	router.POST("/api/agents", auth, cc.HandleCreateAgent)
	router.PUT("/api/agents/:id", auth, cc.HandleUpdateAgent)
	router.DELETE("/api/agents/:id", auth, cc.HandleDeleteAgent)
	router.POST("/api/agents/:id/activate", auth, cc.HandleActivateAgent)

	router.GET("/api/strategies", cc.HandleGetStrategies)
	router.GET("/api/watchlist", cc.HandleGetWatchlist)
	router.PUT("/api/watchlist", auth, cc.HandleUpdateWatchlist)
	router.GET("/api/strategies/definition", cc.HandleGetStrategy)
	router.POST("/api/strategies", auth, cc.HandleCreateStrategy)
	router.PUT("/api/strategies/:id", auth, cc.HandleUpdateStrategy)
	router.DELETE("/api/strategies/:id", auth, cc.HandleDeleteStrategy)

	router.GET("/api/models", cc.HandleGetModels)
	router.POST("/api/models/activate", auth, cc.HandleActivateModel)
	router.POST("/api/models/refresh", auth, cc.HandleRefreshModels)
	router.GET("/api/models/versions", cc.HandleGetModelVersions)
	router.POST("/api/models/activate-version", auth, cc.HandleActivateModelVersion)

	router.GET("/api/lab/scripts", dc.HandleLabScripts)
	router.GET("/api/lab/status", dc.HandleLabStatus)
	router.POST("/api/lab/run", auth, dc.HandleRunLab)

	router.GET("/api/heartbeat", cc.HandleGetHeartbeat)
	router.PUT("/api/heartbeat", auth, cc.HandleUpdateHeartbeat)
	router.GET("/api/heartbeat/profiles", cc.HandleGetHeartbeatProfiles)
	router.POST("/api/heartbeat/apply-profile", auth, cc.HandleApplyHeartbeatProfile)
	router.GET("/api/heartbeat/phases", cc.HandleGetPhases)
	router.PUT("/api/heartbeat/phases", auth, cc.HandleUpdatePhases)

	router.GET("/api/permissions", cc.HandleGetPermissions)
	router.PUT("/api/permissions", auth, cc.HandleUpdatePermissions)

	router.GET("/api/plugins", cc.HandleGetPlugins)
	router.GET("/api/plugins/:name", cc.HandleGetPlugin)
	router.PUT("/api/plugins/:name", auth, cc.HandleUpdatePlugin)
	router.POST("/api/plugins/slack/test", auth, cc.HandleTestSlack)

	router.GET("/api/portfolio/account", oc.HandleGetAccount)
	router.GET("/api/portfolio/positions", oc.HandleGetPositions)
	router.GET("/api/portfolio/orders", oc.HandleGetOrders)
	router.GET("/api/orders/open", oc.HandleGetOpenOrders)
	router.POST("/api/orders/cancel-all", auth, oc.HandleCancelAllOrders)

	router.GET("/api/intents", ic.HandleGetIntents)
	router.GET("/api/intents/history", ic.HandleGetHistory)
	router.POST("/api/intents/authorize/:id", ic.HandleAuthorizeIntent)
	router.POST("/api/intents/reject/:id", ic.HandleRejectIntent)

	router.GET("/api/suggestions", sc.HandleListSuggestions)
	router.GET("/api/suggestions/history", sc.HandleGetHistory)
	router.POST("/api/suggestions/accept/:id", sc.HandleAcceptSuggestion)
	router.POST("/api/suggestions/dismiss/:id", sc.HandleDismissSuggestion)
	router.GET("/api/suggestions/track-record", sc.HandleGetTrackRecord)

	router.POST("/api/webhook/tradingview", tvc.HandleWebhook)

	router.GET("/api/env", dc.HandleGetEnv)
	router.POST("/api/env", auth, dc.HandlePostEnv)

	router.GET("/api/health", dc.HandleHealth)

	router.GET("/api/auth/status", dc.HandleAuthStatus)
	router.POST("/api/auth/login", dc.HandleAuthLogin)
	router.POST("/api/auth/logout", dc.HandleAuthLogout)

	router.POST("/api/backend/restart", dc.HandleBackendRestart)
}

func startDataCleanup(ctx context.Context, storage interfaces.StorageService, retentionDays int, logger *logrus.Logger) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().AddDate(0, 0, -retentionDays)
			logger.WithField("cutoff", cutoff).Info("Running data cleanup")

			if err := storage.CleanupOldData(cutoff); err != nil {
				logger.WithError(err).Error("Failed to cleanup old data")
			}
		}
	}
}

func startPositionMonitor(ctx context.Context, orderController *controllers.OrderController, storage *database.LocalStorage, logger *logrus.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			positions, err := orderController.GetPositions()
			if err != nil {
				logger.WithError(err).Error("Failed to get positions")
				continue
			}

			for _, position := range positions {
				if err := storage.SavePosition(position); err != nil {
					logger.WithError(err).Error("Failed to save position snapshot")
				}
			}

			if account, err := orderController.GetAccount(); err == nil {
				if err := storage.SaveAccountSnapshot(account); err != nil {
					logger.WithError(err).Error("Failed to save account snapshot")
				}
			}

			logger.WithField("positions", len(positions)).Debug("Position monitor update complete")
		}
	}
}

// isLocalOrigin allows only same-machine dashboards to call the API from a
// browser. Anything else gets no CORS headers (and is rejected when it sends an
// Origin header), which keeps the control panel off the LAN.
func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
