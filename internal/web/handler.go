package web

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
	"github.com/gin-gonic/gin"
	"github.com/uykb/MartinStrategy-Hedging/internal/storage"
	"github.com/uykb/MartinStrategy-Hedging/internal/strategy"
)

// statusCache caches the status response to avoid expensive ATR fetches on every poll
type statusCache struct {
	mu        sync.RWMutex
	data      StatusResponse
	lastFetch time.Time
}

func (c *statusCache) Get() (StatusResponse, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Since(c.lastFetch) < 3*time.Second {
		return c.data, true
	}
	return StatusResponse{}, false
}

func (c *statusCache) Set(data StatusResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.lastFetch = time.Now()
}

// StatusResponse represents the real-time status API response
type StatusResponse struct {
	Strategies []StrategyStatus `json:"strategies"`
}

// StrategyStatus represents a single strategy's status
type StrategyStatus struct {
	Name          string   `json:"name"`
	Symbol        string   `json:"symbol"`
	Direction     string   `json:"direction"`
	State         string   `json:"state"`
	Position      Position `json:"position"`
	ATR           ATRData  `json:"atr"`
	GridLevels    []Grid   `json:"grid_levels"`
	ActiveOrders  []Order  `json:"active_orders"`
	TPOrderID     int64    `json:"tp_order_id"`
	GridSkipCount int64    `json:"grid_skip_count"`
	TPSkipCount   int64    `json:"tp_skip_count"`
}

// Position represents current position data
type Position struct {
	Amt        float64 `json:"amt"`
	EntryPrice float64 `json:"entry_price"`
	MarkPrice  float64 `json:"mark_price"`
	UnrealPnL  float64 `json:"unreal_pnl"`
}

// ATRData holds ATR values for different timeframes
type ATRData struct {
	ATR30m float64 `json:"atr_30m"`
	ATR1h  float64 `json:"atr_1h"`
	ATR4h  float64 `json:"atr_4h"`
	ATR1d  float64 `json:"atr_1d"`
}

// Grid represents a grid level
type Grid struct {
	Level    int     `json:"level"`
	Price    float64 `json:"price"`
	Qty      float64 `json:"qty"`
	Distance float64 `json:"distance"`
	FibMult  int     `json:"fib_mult"`
}

// Order represents an active order
type Order struct {
	ID       int64   `json:"id"`
	Symbol   string  `json:"symbol"`
	Side     string  `json:"side"`
	Type     string  `json:"type"`
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
	Status   string  `json:"status"`
}

// OrderRecord represents a historical order
type OrderRecord struct {
	ID        uint      `json:"id"`
	Symbol    string    `json:"symbol"`
	OrderID   int64     `json:"order_id"`
	Side      string    `json:"side"`
	Type      string    `json:"type"`
	Price     float64   `json:"price"`
	Quantity  float64   `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// PnLResponse represents PnL data
type PnLResponse struct {
	DailyPnL    []DailyPnL `json:"daily_pnl"`
	TotalPnL    float64    `json:"total_pnl"`
	WinRate     float64    `json:"win_rate"`
	TotalTrades int        `json:"total_trades"`
}

// DailyPnL represents daily PnL
type DailyPnL struct {
	Date   string  `json:"date"`
	Symbol string  `json:"symbol"`
	PnL    float64 `json:"pnl"`
}

func (s *Server) handleStatus(c *gin.Context) {
	// Return cached data if fresh enough
	if cached, ok := s.statusCache.Get(); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	var resp StatusResponse

	for _, strat := range s.strategies {
		status := strat.GetStatus()

		posAmt, _ := strconv.ParseFloat(status.PositionAmt, 64)
		posEntry, _ := strconv.ParseFloat(status.EntryPrice, 64)

		atr30m := strat.FetchATR("30m")
		atr1h := strat.FetchATR("1h")
		atr4h := strat.FetchATR("4h")
		atr1d := strat.FetchATR("1d")

		gridLevels := s.calculateGridLevels(strat, posEntry, atr30m, atr1h, atr4h, atr1d)

		activeOrders, err := s.exchange.GetOpenOrders(strat.GetSymbol())
		if err != nil {
			activeOrders = []*futures.Order{}
		}

		var orderList []Order
		for _, o := range activeOrders {
			price, _ := strconv.ParseFloat(o.Price, 64)
			qty, _ := strconv.ParseFloat(o.OrigQuantity, 64)
			orderList = append(orderList, Order{
				ID:       o.OrderID,
				Symbol:   o.Symbol,
				Side:     string(o.Side),
				Type:     string(o.Type),
				Price:    price,
				Quantity: qty,
				Status:   string(o.Status),
			})
		}

		markPrice := s.getMarkPrice(strat.GetSymbol())

		unrealPnL := 0.0
		if posAmt != 0 {
			unrealPnL = (markPrice - posEntry) * posAmt
		}

		stratStatus := StrategyStatus{
			Name:      strat.GetName(),
			Symbol:    strat.GetSymbol(),
			Direction: string(strat.GetDirection()),
			State:     status.State,
			Position: Position{
				Amt:        posAmt,
				EntryPrice: posEntry,
				MarkPrice:  markPrice,
				UnrealPnL:  unrealPnL,
			},
			ATR: ATRData{
				ATR30m: atr30m,
				ATR1h:  atr1h,
				ATR4h:  atr4h,
				ATR1d:  atr1d,
			},
			GridLevels:    gridLevels,
			ActiveOrders:  orderList,
			TPOrderID:     status.TPOrderID,
			GridSkipCount: status.GridSkipCount,
			TPSkipCount:   status.TPSkipCount,
		}

		resp.Strategies = append(resp.Strategies, stratStatus)
	}

	s.statusCache.Set(resp)
	c.JSON(http.StatusOK, resp)
}

func (s *Server) calculateGridLevels(strat *strategy.MartingaleStrategy, entryPrice float64, atr30m, atr1h, atr4h, atr1d float64) []Grid {
	if entryPrice <= 0 {
		return []Grid{}
	}

	safeATR := func(v float64) float64 {
		if v == 0 {
			return entryPrice * 0.01
		}
		return v
	}

	a30m := safeATR(atr30m)
	a1h := safeATR(atr1h)
	a4h := safeATR(atr4h)
	a1d := safeATR(atr1d)

	gridDistances := []float64{
		a30m,
		a1h,
		a30m + a1h,
		a4h,
		safeATR(strat.FetchATR("6h")),
		safeATR(strat.FetchATR("8h")),
		safeATR(strat.FetchATR("12h")),
		a1d,
	}

	unitQty := strategy.MinNotional / entryPrice

	var levels []Grid
	currentPrice := entryPrice

	for i := 1; i <= strat.GetMaxSafetyOrders(); i++ {
		dist := 0.0
		if i-1 < len(gridDistances) {
			dist = gridDistances[i-1]
		} else {
			dist = gridDistances[len(gridDistances)-1]
		}

		dir := strat.GetDirection()
		var price float64
		if dir == strategy.DirectionLong {
			price = currentPrice - dist
		} else {
			price = currentPrice + dist
		}
		currentPrice = price

		fibMult := strat.GetFibonacci(i)
		qty := unitQty * float64(fibMult)

		if qty*price < strategy.MinNotional {
			qty = strategy.MinNotional / price
		}

		levels = append(levels, Grid{
			Level:    i,
			Price:    price,
			Qty:      qty,
			Distance: dist,
			FibMult:  fibMult,
		})
	}

	return levels
}

func (s *Server) handleOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	symbol := c.Query("symbol")

	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}

	orders, total, err := s.storage.GetOrders(symbol, page, size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var records []OrderRecord
	for _, o := range orders {
		records = append(records, OrderRecord{
			ID:        o.ID,
			Symbol:    o.Symbol,
			OrderID:   o.OrderID,
			Side:      o.Side,
			Type:      o.Type,
			Price:     o.Price,
			Quantity:  o.Quantity,
			Status:    o.Status,
			CreatedAt: o.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"orders": records,
		"total":  total,
		"page":   page,
		"size":   size,
	})
}

func (s *Server) handlePnL(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days < 1 || days > 365 {
		days = 7
	}

	orders, err := s.storage.GetFilledOrders(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type dailyKey struct {
		Date   string
		Symbol string
	}
	dailyMap := make(map[dailyKey]float64)
	totalPnL := 0.0
	wins := 0
	totalTrades := 0

	sellOrders := make(map[string][]storage.Order)
	for _, o := range orders {
		if o.Side == "SELL" {
			sellOrders[o.Symbol] = append(sellOrders[o.Symbol], o)
		}
	}

	for _, o := range orders {
		date := o.CreatedAt.Format("2006-01-02")
		pnl := 0.0
		if o.Side == "SELL" {
			avgBuy := getAvgBuyPrice(orders, o.Symbol, o.CreatedAt)
			pnl = (o.Price - avgBuy) * o.Quantity
			totalTrades++
			if pnl > 0 {
				wins++
			}
		}
		key := dailyKey{Date: date, Symbol: o.Symbol}
		dailyMap[key] += pnl
		totalPnL += pnl
	}

	var dailyPnL []DailyPnL
	for key, pnl := range dailyMap {
		dailyPnL = append(dailyPnL, DailyPnL{
			Date:   key.Date,
			Symbol: key.Symbol,
			PnL:    pnl,
		})
	}

	c.JSON(http.StatusOK, PnLResponse{
		DailyPnL:    dailyPnL,
		TotalPnL:    totalPnL,
		WinRate:     float64(wins) / float64(max(totalTrades, 1)) * 100,
		TotalTrades: totalTrades,
	})
}

func (s *Server) getMarkPrice(symbol string) float64 {
	prices, err := s.exchange.GetPrices()
	if err != nil {
		return 0
	}
	for _, p := range prices {
		if p.Symbol == symbol {
			price, _ := strconv.ParseFloat(p.Price, 64)
			return price
		}
	}
	return 0
}

func getAvgBuyPrice(orders []storage.Order, symbol string, before time.Time) float64 {
	total := 0.0
	count := 0
	for _, o := range orders {
		if o.Symbol == symbol && o.Side == "BUY" && !o.CreatedAt.After(before) {
			total += o.Price
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
