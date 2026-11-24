package database

import (
	"fmt"
	"os"
	"path/filepath"
	"prophet-trader/interfaces"
	"prophet-trader/models"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// LocalStorage implements the StorageService interface using SQLite
type LocalStorage struct {
	db     *gorm.DB
	logger *logrus.Logger
}

// NewLocalStorage creates a new local storage service
func NewLocalStorage(dbPath string) (*LocalStorage, error) {
	// Ensure the directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open SQLite database
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Auto-migrate schemas
	if err := db.AutoMigrate(
		&models.DBOrder{},
		&models.DBBar{},
		&models.DBPosition{},
		&models.DBTrade{},
		&models.DBAccountSnapshot{},
		&models.DBSignal{},
		&models.DBManagedPosition{},
		&models.DBSuggestion{},
	); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	return &LocalStorage{
		db:     db,
		logger: logger,
	}, nil
}

// SaveBars saves multiple bars to the database
func (s *LocalStorage) SaveBars(bars []*interfaces.Bar) error {
	if len(bars) == 0 {
		return nil
	}

	s.logger.WithField("count", len(bars)).Info("Saving bars to database")

	// Convert interface bars to DB bars
	dbBars := make([]*models.DBBar, len(bars))
	for i, bar := range bars {
		dbBars[i] = &models.DBBar{
			Symbol:    bar.Symbol,
			Timestamp: bar.Timestamp,
			Open:      bar.Open,
			High:      bar.High,
			Low:       bar.Low,
			Close:     bar.Close,
			Volume:    bar.Volume,
			VWAP:      bar.VWAP,
		}
	}

	// Batch insert with upsert on conflict
	result := s.db.Create(&dbBars)
	if result.Error != nil {
		return fmt.Errorf("failed to save bars: %w", result.Error)
	}

	s.logger.WithField("saved", result.RowsAffected).Info("Bars saved successfully")
	return nil
}

// GetBars retrieves bars for a symbol within a time range
func (s *LocalStorage) GetBars(symbol string, start, end time.Time) ([]*interfaces.Bar, error) {
	var dbBars []*models.DBBar

	result := s.db.Where("symbol = ? AND timestamp >= ? AND timestamp <= ?", symbol, start, end).
		Order("timestamp ASC").
		Find(&dbBars)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get bars: %w", result.Error)
	}

	// Convert DB bars to interface bars
	bars := make([]*interfaces.Bar, len(dbBars))
	for i, dbBar := range dbBars {
		bars[i] = &interfaces.Bar{
			Symbol:    dbBar.Symbol,
			Timestamp: dbBar.Timestamp,
			Open:      dbBar.Open,
			High:      dbBar.High,
			Low:       dbBar.Low,
			Close:     dbBar.Close,
			Volume:    dbBar.Volume,
			VWAP:      dbBar.VWAP,
		}
	}

	return bars, nil
}

// SaveOrder saves an order to the database
func (s *LocalStorage) SaveOrder(order *interfaces.Order) error {
	dbOrder := &models.DBOrder{
		OrderID:        order.ID,
		ClientOrderID:  order.ClientOrderID,
		Symbol:         order.Symbol,
		Qty:            order.Qty,
		Side:           order.Side,
		Type:           order.Type,
		TimeInForce:    order.TimeInForce,
		LimitPrice:     order.LimitPrice,
		StopPrice:      order.StopPrice,
		Status:         order.Status,
		SubmitState:    order.SubmitState,
		FilledQty:      order.FilledQty,
		FilledAvgPrice: order.FilledAvgPrice,
		SubmittedAt:    order.SubmittedAt,
		FilledAt:       order.FilledAt,
		CanceledAt:     order.CanceledAt,
	}

	// Upsert by broker OrderID (or by ClientOrderID while the broker ID is unknown).
	var existing models.DBOrder
	lookup := s.db.Where("order_id = ?", order.ID)
	if order.ID == "" && order.ClientOrderID != "" {
		lookup = s.db.Where("client_order_id = ?", order.ClientOrderID)
	}
	if err := lookup.First(&existing).Error; err == nil {
		dbOrder.ID = existing.ID
		dbOrder.Model = existing.Model
	}

	result := s.db.Save(dbOrder)
	if result.Error != nil {
		return fmt.Errorf("failed to save order: %w", result.Error)
	}

	return nil
}

// GetOrder retrieves an order by ID
func (s *LocalStorage) GetOrder(orderID string) (*interfaces.Order, error) {
	var dbOrder models.DBOrder

	result := s.db.Where("order_id = ?", orderID).First(&dbOrder)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get order: %w", result.Error)
	}

	return &interfaces.Order{
		ID:             dbOrder.OrderID,
		ClientOrderID:  dbOrder.ClientOrderID,
		Symbol:         dbOrder.Symbol,
		Qty:            dbOrder.Qty,
		Side:           dbOrder.Side,
		Type:           dbOrder.Type,
		TimeInForce:    dbOrder.TimeInForce,
		LimitPrice:     dbOrder.LimitPrice,
		StopPrice:      dbOrder.StopPrice,
		Status:         dbOrder.Status,
		SubmitState:    dbOrder.SubmitState,
		FilledQty:      dbOrder.FilledQty,
		FilledAvgPrice: dbOrder.FilledAvgPrice,
		SubmittedAt:    dbOrder.SubmittedAt,
		FilledAt:       dbOrder.FilledAt,
		CanceledAt:     dbOrder.CanceledAt,
	}, nil
}

// GetOrderByClientID finds an order by the idempotency key assigned before submission.
func (s *LocalStorage) GetOrderByClientID(clientOrderID string) (*interfaces.Order, error) {
	var dbOrder models.DBOrder
	if err := s.db.Where("client_order_id = ?", clientOrderID).First(&dbOrder).Error; err != nil {
		return nil, fmt.Errorf("failed to get order by client id: %w", err)
	}
	return &interfaces.Order{
		ID:             dbOrder.OrderID,
		ClientOrderID:  dbOrder.ClientOrderID,
		Symbol:         dbOrder.Symbol,
		Qty:            dbOrder.Qty,
		Side:           dbOrder.Side,
		Type:           dbOrder.Type,
		TimeInForce:    dbOrder.TimeInForce,
		LimitPrice:     dbOrder.LimitPrice,
		StopPrice:      dbOrder.StopPrice,
		Status:         dbOrder.Status,
		SubmitState:    dbOrder.SubmitState,
		FilledQty:      dbOrder.FilledQty,
		FilledAvgPrice: dbOrder.FilledAvgPrice,
		SubmittedAt:    dbOrder.SubmittedAt,
		FilledAt:       dbOrder.FilledAt,
		CanceledAt:     dbOrder.CanceledAt,
	}, nil
}

// GetOrdersBySubmitState returns journaled orders in the given submit states.
func (s *LocalStorage) GetOrdersBySubmitState(states []string) ([]*interfaces.Order, error) {
	var dbOrders []*models.DBOrder
	if err := s.db.Where("submit_state IN ?", states).Find(&dbOrders).Error; err != nil {
		return nil, fmt.Errorf("failed to get orders by submit state: %w", err)
	}
	out := make([]*interfaces.Order, 0, len(dbOrders))
	for _, dbOrder := range dbOrders {
		out = append(out, &interfaces.Order{
			ID:             dbOrder.OrderID,
			ClientOrderID:  dbOrder.ClientOrderID,
			Symbol:         dbOrder.Symbol,
			Qty:            dbOrder.Qty,
			Side:           dbOrder.Side,
			Type:           dbOrder.Type,
			TimeInForce:    dbOrder.TimeInForce,
			LimitPrice:     dbOrder.LimitPrice,
			StopPrice:      dbOrder.StopPrice,
			Status:         dbOrder.Status,
			SubmitState:    dbOrder.SubmitState,
			FilledQty:      dbOrder.FilledQty,
			FilledAvgPrice: dbOrder.FilledAvgPrice,
			SubmittedAt:    dbOrder.SubmittedAt,
			FilledAt:       dbOrder.FilledAt,
			CanceledAt:     dbOrder.CanceledAt,
		})
	}
	return out, nil
}

// UpdateOrderSubmission records the broker response for a journaled order.
func (s *LocalStorage) UpdateOrderSubmission(clientOrderID, brokerOrderID, status, submitState, metadata string) error {
	updates := map[string]interface{}{
		"status":       status,
		"submit_state": submitState,
	}
	if brokerOrderID != "" {
		updates["order_id"] = brokerOrderID
	}
	if metadata != "" {
		updates["metadata"] = metadata
	}
	return s.db.Model(&models.DBOrder{}).Where("client_order_id = ?", clientOrderID).Updates(updates).Error
}

// UpdateOrderStatusByBrokerID updates status/submit_state after a cancel or broker event.
func (s *LocalStorage) UpdateOrderStatusByBrokerID(brokerOrderID, status, submitState string) error {
	updates := map[string]interface{}{"status": status}
	if submitState != "" {
		updates["submit_state"] = submitState
	}
	return s.db.Model(&models.DBOrder{}).Where("order_id = ?", brokerOrderID).Updates(updates).Error
}

// GetOrders retrieves orders by status
func (s *LocalStorage) GetOrders(status string) ([]*interfaces.Order, error) {
	var dbOrders []*models.DBOrder

	query := s.db.Model(&models.DBOrder{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	result := query.Order("submitted_at DESC").Find(&dbOrders)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get orders: %w", result.Error)
	}

	orders := make([]*interfaces.Order, len(dbOrders))
	for i, dbOrder := range dbOrders {
		orders[i] = &interfaces.Order{
			ID:             dbOrder.OrderID,
			ClientOrderID:  dbOrder.ClientOrderID,
			Symbol:         dbOrder.Symbol,
			Qty:            dbOrder.Qty,
			Side:           dbOrder.Side,
			Type:           dbOrder.Type,
			TimeInForce:    dbOrder.TimeInForce,
			LimitPrice:     dbOrder.LimitPrice,
			StopPrice:      dbOrder.StopPrice,
			Status:         dbOrder.Status,
			SubmitState:    dbOrder.SubmitState,
			FilledQty:      dbOrder.FilledQty,
			FilledAvgPrice: dbOrder.FilledAvgPrice,
			SubmittedAt:    dbOrder.SubmittedAt,
			FilledAt:       dbOrder.FilledAt,
			CanceledAt:     dbOrder.CanceledAt,
		}
	}

	return orders, nil
}

// CleanupOldData removes data older than the specified time
func (s *LocalStorage) CleanupOldData(before time.Time) error {
	s.logger.WithField("before", before).Info("Cleaning up old data")

	// Delete old bars
	if err := s.db.Where("timestamp < ?", before).Delete(&models.DBBar{}).Error; err != nil {
		return fmt.Errorf("failed to delete old bars: %w", err)
	}

	// Delete old account snapshots
	if err := s.db.Where("snapshot_time < ?", before).Delete(&models.DBAccountSnapshot{}).Error; err != nil {
		return fmt.Errorf("failed to delete old snapshots: %w", err)
	}

	// Delete old signals
	if err := s.db.Where("created_at < ?", before).Delete(&models.DBSignal{}).Error; err != nil {
		return fmt.Errorf("failed to delete old signals: %w", err)
	}

	// Delete old resolved suggestions (keep pending ones regardless of age)
	if err := s.db.Where("status != 'PENDING' AND created_at < ?", before).Delete(&models.DBSuggestion{}).Error; err != nil {
		return fmt.Errorf("failed to delete old suggestions: %w", err)
	}

	s.logger.Info("Old data cleaned up successfully")
	return nil
}

// Additional helper methods

func (s *LocalStorage) SavePosition(position *interfaces.Position) error {
	var dbPosition models.DBPosition
	result := s.db.Where("symbol = ?", position.Symbol).First(&dbPosition)

	dbPosition.Symbol = position.Symbol
	dbPosition.Qty = position.Qty
	dbPosition.AvgEntryPrice = position.AvgEntryPrice
	dbPosition.MarketValue = position.MarketValue
	dbPosition.CostBasis = position.CostBasis
	dbPosition.UnrealizedPL = position.UnrealizedPL
	dbPosition.UnrealizedPLPC = position.UnrealizedPLPC
	dbPosition.CurrentPrice = position.CurrentPrice
	dbPosition.Side = position.Side
	dbPosition.SnapshotTime = time.Now()

	if result.Error == nil {
		// Update existing
		if err := s.db.Save(&dbPosition).Error; err != nil {
			return fmt.Errorf("failed to update position: %w", err)
		}
		return nil
	}

	// Create new
	if err := s.db.Create(&dbPosition).Error; err != nil {
		return fmt.Errorf("failed to create position: %w", err)
	}

	return nil
}

// SaveAccountSnapshot saves an account snapshot
func (s *LocalStorage) SaveAccountSnapshot(account *interfaces.Account) error {
	dbSnapshot := &models.DBAccountSnapshot{
		Cash:             account.Cash,
		PortfolioValue:   account.PortfolioValue,
		BuyingPower:      account.BuyingPower,
		DayTradeCount:    account.DayTradeCount,
		PatternDayTrader: account.PatternDayTrader,
		SnapshotTime:     time.Now(),
	}

	result := s.db.Save(dbSnapshot)
	if result.Error != nil {
		return fmt.Errorf("failed to save account snapshot: %w", result.Error)
	}

	return nil
}

// GetAccountSnapshots returns the most recent snapshots in chronological order.
func (s *LocalStorage) GetAccountSnapshots(limit int) ([]*models.DBAccountSnapshot, error) {
	if limit <= 0 {
		limit = 500
	}
	var snapshots []*models.DBAccountSnapshot
	if err := s.db.Order("snapshot_time desc").Limit(limit).Find(&snapshots).Error; err != nil {
		return nil, fmt.Errorf("failed to load account snapshots: %w", err)
	}
	for i, j := 0, len(snapshots)-1; i < j; i, j = i+1, j-1 {
		snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
	}
	return snapshots, nil
}

// SaveSignal saves a trading signal
func (s *LocalStorage) SaveSignal(symbol, signalType, strategyName, reason string, strength float64) error {
	dbSignal := &models.DBSignal{
		Symbol:       symbol,
		SignalType:   signalType,
		Strength:     strength,
		StrategyName: strategyName,
		Reason:       reason,
		Executed:     false,
	}

	result := s.db.Save(dbSignal)
	if result.Error != nil {
		return fmt.Errorf("failed to save signal: %w", result.Error)
	}

	return nil
}

// SaveManagedPosition saves a managed position to the database
func (s *LocalStorage) SaveManagedPosition(position *models.DBManagedPosition) error {
	result := s.db.Save(position)
	if result.Error != nil {
		return fmt.Errorf("failed to save managed position: %w", result.Error)
	}
	return nil
}

// GetManagedPosition retrieves a managed position by ID
func (s *LocalStorage) GetManagedPosition(positionID string) (*models.DBManagedPosition, error) {
	var dbPosition models.DBManagedPosition

	result := s.db.Where("position_id = ?", positionID).First(&dbPosition)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get managed position: %w", result.Error)
	}

	return &dbPosition, nil
}

// GetAllManagedPositions retrieves all managed positions with optional status filter
func (s *LocalStorage) GetAllManagedPositions(status string) ([]*models.DBManagedPosition, error) {
	var dbPositions []*models.DBManagedPosition

	query := s.db.Model(&models.DBManagedPosition{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	result := query.Order("created_at DESC").Find(&dbPositions)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get managed positions: %w", result.Error)
	}

	return dbPositions, nil
}

// DeleteManagedPosition deletes a managed position by ID
func (s *LocalStorage) DeleteManagedPosition(positionID string) error {
	result := s.db.Where("position_id = ?", positionID).Delete(&models.DBManagedPosition{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete managed position: %w", result.Error)
	}
	return nil
}

// ── Suggestions ──────────────────────────────

func (s *LocalStorage) SaveSuggestion(suggestion *models.DBSuggestion) error {
	result := s.db.Save(suggestion)
	if result.Error != nil {
		return fmt.Errorf("failed to save suggestion: %w", result.Error)
	}
	return nil
}

func (s *LocalStorage) GetSuggestion(suggestionID string) (*models.DBSuggestion, error) {
	var suggestion models.DBSuggestion
	result := s.db.Where("suggestion_id = ?", suggestionID).First(&suggestion)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get suggestion: %w", result.Error)
	}
	return &suggestion, nil
}

func (s *LocalStorage) GetSuggestions(status string) ([]*models.DBSuggestion, error) {
	var suggestions []*models.DBSuggestion
	query := s.db.Model(&models.DBSuggestion{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	result := query.Order("created_at DESC").Find(&suggestions)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get suggestions: %w", result.Error)
	}
	return suggestions, nil
}

func (s *LocalStorage) UpdateSuggestionStatus(suggestionID, status string, resolvedAt *time.Time) error {
	result := s.db.Model(&models.DBSuggestion{}).Where("suggestion_id = ?", suggestionID).
		Updates(map[string]interface{}{"status": status, "resolved_at": resolvedAt})
	if result.Error != nil {
		return fmt.Errorf("failed to update suggestion status: %w", result.Error)
	}
	return nil
}

func (s *LocalStorage) GetSuggestionsForOutcomeCheck() ([]*models.DBSuggestion, error) {
	var suggestions []*models.DBSuggestion
	result := s.db.Where("status IN ? AND outcome_checked_at IS NULL AND created_at > ?",
		[]string{"ACCEPTED", "DISMISSED", "EXPIRED"},
		time.Now().AddDate(0, 0, -7)).
		Find(&suggestions)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get suggestions for outcome check: %w", result.Error)
	}
	return suggestions, nil
}

func (s *LocalStorage) UpdateSuggestionOutcome(suggestionID string, outcomePrice, outcomePnL float64) error {
	now := time.Now()
	result := s.db.Model(&models.DBSuggestion{}).Where("suggestion_id = ?", suggestionID).
		Updates(map[string]interface{}{
			"outcome_price":      outcomePrice,
			"outcome_pn_l":       outcomePnL,
			"outcome_checked_at": &now,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update suggestion outcome: %w", result.Error)
	}
	return nil
}

// Close closes the database connection
func (s *LocalStorage) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}