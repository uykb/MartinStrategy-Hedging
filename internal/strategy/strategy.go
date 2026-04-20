package strategy

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
	"github.com/uykb/MartinStrategy-Hedging/internal/config"
	"github.com/uykb/MartinStrategy-Hedging/internal/core"
	"github.com/uykb/MartinStrategy-Hedging/internal/exchange"
	"github.com/uykb/MartinStrategy-Hedging/internal/storage"
	"github.com/uykb/MartinStrategy-Hedging/internal/utils"
	"go.uber.org/zap"
)

// State definition
type State string

const (
	StateIdle        State = "IDLE"
	StateInPosition  State = "IN_POSITION"
	StatePlacingGrid State = "PLACING_GRID"
	StateClosing     State = "CLOSING"
)

// Direction definition
type Direction string

const (
	DirectionLong  Direction = "LONG"
	DirectionShort Direction = "SHORT"
)

// MinNotional is the minimum order value in USDT for Binance Futures
const MinNotional = 50.0

type MartingaleStrategy struct {
	cfg       *config.StrategyConfig
	exchange  *exchange.BinanceClient
	storage   *storage.Database
	bus       *core.EventBus
	symbol    string
	direction Direction

	mu               sync.RWMutex
	currentState     State
	position         *futures.AccountPosition
	activeOrders     map[int64]*futures.Order
	currentTPOrderID int64

	quantityPrecision int
	pricePrecision    int
	minQty            float64
	stepSize          float64
	tickSize          float64

	gridMu sync.Mutex
	tpMu   sync.Mutex

	gridSkipCount int64
	tpSkipCount   int64

	// Position value tracking for hedging
	positionValue float64
}

func NewMartingaleStrategy(cfg *config.StrategyConfig, ex *exchange.BinanceClient, st *storage.Database, bus *core.EventBus) *MartingaleStrategy {
	dir := Direction(cfg.Direction)
	if dir == "" {
		dir = DirectionLong
	}

	return &MartingaleStrategy{
		cfg:          cfg,
		exchange:     ex,
		storage:      st,
		bus:          bus,
		symbol:       cfg.Symbol,
		direction:    dir,
		currentState: StateIdle,
		activeOrders: make(map[int64]*futures.Order),
	}
}

func (s *MartingaleStrategy) Start() {
	si, err := s.exchange.GetSymbolInfo(s.symbol)
	if err != nil {
		utils.Logger.Fatal("Failed to get symbol info", zap.Error(err), zap.String("symbol", s.symbol))
	}

	s.quantityPrecision = si.QuantityPrecision
	s.pricePrecision = si.PricePrecision
	s.minQty = si.MinQty
	s.stepSize = si.StepSize
	s.tickSize = si.TickSize

	utils.Logger.Info("Symbol Info Initialized",
		zap.String("symbol", s.symbol),
		zap.String("direction", string(s.direction)),
		zap.Int("price_prec", s.pricePrecision),
		zap.Int("qty_prec", s.quantityPrecision),
		zap.Float64("step_size", s.stepSize),
		zap.Float64("tick_size", s.tickSize),
		zap.Float64("min_qty", s.minQty),
	)

	s.bus.SubscribeWithFilter(core.EventTick, s.symbol, s.handleTick)
	s.bus.Subscribe(core.EventOrderUpdate, s.handleOrderUpdate)

	s.syncState()
}

func (s *MartingaleStrategy) syncState() {
	s.mu.Lock()
	defer s.mu.Unlock()

	pos, err := s.exchange.GetPosition(s.symbol)
	if err != nil {
		utils.Logger.Error("Failed to sync position", zap.String("symbol", s.symbol), zap.Error(err))
		return
	}
	s.position = pos

	amt, _ := strconv.ParseFloat(pos.PositionAmt, 64)
	if math.Abs(amt) > 0 {
		s.currentState = StateInPosition

		orders, err := s.exchange.GetOpenOrders(s.symbol)
		if err != nil {
			utils.Logger.Error("Failed to get open orders", zap.Error(err))
		} else {
			hasTP := false
			for _, o := range orders {
				if s.direction == DirectionLong {
					if o.Side == futures.SideTypeSell && o.Type == futures.OrderTypeLimit {
						hasTP = true
						s.currentTPOrderID = o.OrderID
						utils.Logger.Info("Found existing TP order", zap.Int64("id", o.OrderID))
						break
					}
				} else {
					if o.Side == futures.SideTypeBuy && o.Type == futures.OrderTypeLimit {
						hasTP = true
						s.currentTPOrderID = o.OrderID
						utils.Logger.Info("Found existing TP order", zap.Int64("id", o.OrderID))
						break
					}
				}
			}

			if !hasTP {
				utils.Logger.Warn("Detected position without TP order. Restoring TP...", zap.String("symbol", s.symbol))
				go func() {
					time.Sleep(100 * time.Millisecond)
					s.updateTP()
				}()
			} else {
				utils.Logger.Info("State restored with existing TP.", zap.String("symbol", s.symbol), zap.Int("open_orders", len(orders)))
			}
		}

	} else {
		s.currentState = StateIdle
	}

	utils.Logger.Info("State Synced",
		zap.String("symbol", s.symbol),
		zap.String("direction", string(s.direction)),
		zap.String("state", string(s.currentState)),
		zap.Float64("amt", amt),
	)
}

func (s *MartingaleStrategy) handleTick(ctx context.Context, event core.Event) error {
	var price float64
	switch data := event.Data.(type) {
	case core.TickData:
		price = data.Price
	case *core.TickData:
		price = data.Price
	default:
		return fmt.Errorf("invalid tick data type: %T", event.Data)
	}

	s.mu.Lock()
	if s.currentState != StateIdle {
		s.mu.Unlock()
		return nil
	}
	s.currentState = StatePlacingGrid
	s.mu.Unlock()

	if err := s.enterPosition(price); err != nil {
		s.mu.Lock()
		s.currentState = StateIdle
		s.mu.Unlock()
		utils.Logger.Error("enterPosition failed, resetting to IDLE",
			zap.String("symbol", s.symbol),
			zap.Error(err),
		)
		return err
	}
	return nil
}

func (s *MartingaleStrategy) handleOrderUpdate(ctx context.Context, event core.Event) error {
	order, ok := event.Data.(*futures.WsOrderTradeUpdate)
	if !ok {
		return fmt.Errorf("invalid order update data: expected *futures.WsOrderTradeUpdate, got %T", event.Data)
	}

	if order.Symbol != s.symbol {
		return nil
	}

	utils.Logger.Info("Order Update Received",
		zap.String("symbol", order.Symbol),
		zap.Int64("id", order.ID),
		zap.String("status", string(order.Status)),
		zap.String("type", string(order.Type)),
		zap.String("side", string(order.Side)),
	)

	if order.Status == futures.OrderStatusTypeFilled {
		if s.direction == DirectionLong {
			if order.Side == futures.SideTypeBuy {
				utils.Logger.Info("Buy Order Filled", zap.String("type", string(order.Type)))

				s.mu.Lock()
				prevState := s.currentState
				s.currentState = StateInPosition
				s.mu.Unlock()

				if prevState == StateIdle || prevState == StatePlacingGrid {
					execPrice, _ := strconv.ParseFloat(order.AveragePrice, 64)
					go s.placeGridOrders(execPrice)
				} else {
					utils.Logger.Info("Safety Order Filled. Re-calculating TP.")
					go s.updateTP()
				}
			} else if order.Side == futures.SideTypeSell {
				utils.Logger.Info("Sell Order Filled (TP/Manual). Resetting to IDLE.",
					zap.String("type", string(order.Type)),
				)

				s.mu.Lock()
				s.currentState = StateIdle
				s.currentTPOrderID = 0
				s.positionValue = 0
				s.mu.Unlock()

				s.exchange.CancelAllOrders(s.symbol)
				time.Sleep(10 * time.Second)
			}
		} else {
			// Short direction
			if order.Side == futures.SideTypeSell {
				utils.Logger.Info("Sell Order Filled", zap.String("type", string(order.Type)))

				s.mu.Lock()
				prevState := s.currentState
				s.currentState = StateInPosition
				s.mu.Unlock()

				if prevState == StateIdle || prevState == StatePlacingGrid {
					execPrice, _ := strconv.ParseFloat(order.AveragePrice, 64)
					go s.placeGridOrders(execPrice)
				} else {
					utils.Logger.Info("Safety Order Filled. Re-calculating TP.")
					go s.updateTP()
				}
			} else if order.Side == futures.SideTypeBuy {
				utils.Logger.Info("Buy Order Filled (TP/Manual). Resetting to IDLE.",
					zap.String("type", string(order.Type)),
				)

				s.mu.Lock()
				s.currentState = StateIdle
				s.currentTPOrderID = 0
				s.positionValue = 0
				s.mu.Unlock()

				s.exchange.CancelAllOrders(s.symbol)
				time.Sleep(10 * time.Second)
			}
		}
	}
	return nil
}

func (s *MartingaleStrategy) enterPosition(currentPrice float64) error {
	if s.direction == DirectionLong {
		return s.enterLong(currentPrice)
	}
	return s.enterShort(currentPrice)
}

func (s *MartingaleStrategy) enterLong(currentPrice float64) error {
	utils.Logger.Info("Entering Long Position...", zap.String("symbol", s.symbol))

	unitQtyRaw := MinNotional / currentPrice
	unitQty := utils.RoundUpToTickSize(unitQtyRaw, s.stepSize)

	if unitQty < s.minQty {
		unitQty = s.minQty
	}

	baseQty := unitQty * 1.0
	baseQty = utils.ToFixed(baseQty, s.quantityPrecision)

	utils.Logger.Info("Calculated Base Qty",
		zap.String("symbol", s.symbol),
		zap.Float64("price", currentPrice),
		zap.Float64("unit_qty", unitQty),
		zap.Float64("base_qty", baseQty),
	)

	_, err := s.exchange.PlaceOrder(s.symbol, futures.SideTypeBuy, futures.OrderTypeMarket, baseQty, 0)
	if err != nil {
		utils.Logger.Error("Failed to place base order", zap.Error(err))
		return err
	}

	s.mu.Lock()
	s.positionValue = baseQty * currentPrice
	s.mu.Unlock()

	return nil
}

func (s *MartingaleStrategy) enterShort(currentPrice float64) error {
	utils.Logger.Info("Entering Short Position...", zap.String("symbol", s.symbol))

	unitQtyRaw := MinNotional / currentPrice
	unitQty := utils.RoundUpToTickSize(unitQtyRaw, s.stepSize)

	if unitQty < s.minQty {
		unitQty = s.minQty
	}

	baseQty := unitQty * 1.0
	baseQty = utils.ToFixed(baseQty, s.quantityPrecision)

	utils.Logger.Info("Calculated Base Qty",
		zap.String("symbol", s.symbol),
		zap.Float64("price", currentPrice),
		zap.Float64("unit_qty", unitQty),
		zap.Float64("base_qty", baseQty),
	)

	_, err := s.exchange.PlaceOrder(s.symbol, futures.SideTypeSell, futures.OrderTypeMarket, baseQty, 0)
	if err != nil {
		utils.Logger.Error("Failed to place base order", zap.Error(err))
		return err
	}

	s.mu.Lock()
	s.positionValue = baseQty * currentPrice
	s.mu.Unlock()

	return nil
}

func (s *MartingaleStrategy) placeGridOrders(execPrice float64) {
	if !s.gridMu.TryLock() {
		s.mu.Lock()
		s.gridSkipCount++
		skipCount := s.gridSkipCount
		s.mu.Unlock()
		utils.Logger.Warn("placeGridOrders skipped: already running",
			zap.String("symbol", s.symbol),
			zap.Int64("skip_count", skipCount))
		return
	}
	defer s.gridMu.Unlock()

	var entryPrice float64

	if execPrice > 0 {
		entryPrice = execPrice
		utils.Logger.Info("Using execution price from order event",
			zap.String("symbol", s.symbol),
			zap.Float64("entryPrice", entryPrice))
	} else {
		pos, err := s.exchange.GetPosition(s.symbol)
		if err != nil {
			utils.Logger.Error("Failed to get position for grid orders", zap.Error(err))
			return
		}
		entryPrice, _ = strconv.ParseFloat(pos.EntryPrice, 64)
		utils.Logger.Info("Using entry price from position API",
			zap.String("symbol", s.symbol),
			zap.Float64("entryPrice", entryPrice))
	}

	if entryPrice <= 0 {
		utils.Logger.Error("Invalid entry price, cannot place grid orders",
			zap.String("symbol", s.symbol),
			zap.Float64("entryPrice", entryPrice))
		return
	}

	atr30m := s.fetchATR("30m")
	atr1h := s.fetchATR("1h")
	atr2h := s.fetchATR("2h")
	atr4h := s.fetchATR("4h")
	atr6h := s.fetchATR("6h")
	atr8h := s.fetchATR("8h")
	atr12h := s.fetchATR("12h")
	atr1d := s.fetchATR("1d")

	defaultATR := entryPrice * 0.01
	if atr30m == 0 {
		atr30m = defaultATR
	}
	if atr1h == 0 {
		atr1h = defaultATR
	}
	if atr2h == 0 {
		atr2h = defaultATR
	}
	if atr4h == 0 {
		atr4h = defaultATR
	}
	if atr6h == 0 {
		atr6h = defaultATR
	}
	if atr8h == 0 {
		atr8h = defaultATR
	}
	if atr12h == 0 {
		atr12h = defaultATR
	}
	if atr1d == 0 {
		atr1d = defaultATR
	}

	unitQty := utils.RoundUpToTickSize(MinNotional/entryPrice, s.stepSize)

	utils.Logger.Info("Placing Grid Orders",
		zap.String("symbol", s.symbol),
		zap.String("direction", string(s.direction)),
		zap.Float64("Entry", entryPrice),
		zap.Float64("ATR30m", atr30m),
		zap.Float64("UnitQty", unitQty),
	)

	gridDistances := []float64{
		atr30m,
		atr1h,
		atr30m + atr1h,
		atr2h,
		atr4h,
		atr6h,
		atr8h,
		atr12h,
		atr1d,
	}

	currentPriceLevel := entryPrice

	for i := 1; i <= s.cfg.MaxSafetyOrders; i++ {
		stepDist := 0.0
		if i-1 < len(gridDistances) {
			stepDist = gridDistances[i-1]
		} else {
			stepDist = gridDistances[len(gridDistances)-1]
		}

		var price float64
		if s.direction == DirectionLong {
			price = currentPriceLevel - stepDist
		} else {
			price = currentPriceLevel + stepDist
		}
		currentPriceLevel = price

		price = utils.RoundToTickSize(price, s.tickSize)
		price = utils.ToFixed(price, s.pricePrecision)

		volMult := s.getFibonacci(i)
		qty := unitQty * float64(volMult)

		if qty*price < MinNotional {
			utils.Logger.Info("Adjusting Qty to meet MinNotional",
				zap.Int("index", i),
				zap.Float64("old_qty", qty),
				zap.Float64("price", price),
			)
			qty = MinNotional / price
		}

		qty = utils.RoundUpToTickSize(qty, s.stepSize)
		qty = utils.ToFixed(qty, s.quantityPrecision)

		var side futures.SideType
		if s.direction == DirectionLong {
			side = futures.SideTypeBuy
		} else {
			side = futures.SideTypeSell
		}

		utils.Logger.Info("Placing Safety Order",
			zap.String("symbol", s.symbol),
			zap.String("direction", string(s.direction)),
			zap.Int("index", i),
			zap.Float64("price", price),
			zap.Float64("qty", qty),
			zap.Float64("dist_atr", stepDist),
		)

		_, err := s.exchange.PlaceOrder(s.symbol, side, futures.OrderTypeLimit, qty, price)
		if err != nil {
			utils.Logger.Error("Failed to place safety order",
				zap.String("symbol", s.symbol),
				zap.Int("index", i),
				zap.Error(err),
			)
		}

		time.Sleep(200 * time.Millisecond)
	}

	s.updateTP()
}

func (s *MartingaleStrategy) updateTP() {
	if !s.tpMu.TryLock() {
		s.mu.Lock()
		s.tpSkipCount++
		skipCount := s.tpSkipCount
		s.mu.Unlock()
		utils.Logger.Warn("updateTP skipped: already running",
			zap.String("symbol", s.symbol),
			zap.Int64("skip_count", skipCount))
		return
	}
	defer s.tpMu.Unlock()

	pos, err := s.exchange.GetPosition(s.symbol)
	if err != nil {
		utils.Logger.Error("Failed to get position for TP update", zap.Error(err))
		return
	}

	avgPrice, _ := strconv.ParseFloat(pos.EntryPrice, 64)
	amt, _ := strconv.ParseFloat(pos.PositionAmt, 64)

	if math.Abs(amt) == 0 {
		s.mu.Lock()
		s.currentTPOrderID = 0
		s.mu.Unlock()
		return
	}

	s.mu.RLock()
	if s.currentState == StateIdle {
		s.mu.RUnlock()
		return
	}
	atr30m := s.fetchATR("30m")
	if atr30m == 0 {
		atr30m = avgPrice * 0.01
	}
	oldTPID := s.currentTPOrderID
	s.mu.RUnlock()

	var tpPrice float64
	if s.direction == DirectionLong {
		tpPrice = avgPrice + atr30m
	} else {
		tpPrice = avgPrice - atr30m
	}

	if oldTPID != 0 {
		utils.Logger.Info("Cancelling old TP",
			zap.String("symbol", s.symbol),
			zap.Int64("id", oldTPID))
		if err := s.exchange.CancelOrder(s.symbol, oldTPID); err != nil {
			utils.Logger.Warn("Failed to cancel old TP (might be filled or already canceled)", zap.Error(err))
		}
	}

	tpPrice = utils.RoundToTickSize(tpPrice, s.tickSize)
	tpPrice = utils.ToFixed(tpPrice, s.pricePrecision)

	tpQty := utils.ToFixed(math.Abs(amt), s.quantityPrecision)

	var tpSide futures.SideType
	if s.direction == DirectionLong {
		tpSide = futures.SideTypeSell
	} else {
		tpSide = futures.SideTypeBuy
	}

	utils.Logger.Info("Updating TP",
		zap.String("symbol", s.symbol),
		zap.String("direction", string(s.direction)),
		zap.Float64("Price", tpPrice),
		zap.Float64("Qty", tpQty),
	)

	resp, err := s.exchange.PlaceOrder(s.symbol, tpSide, futures.OrderTypeLimit, tpQty, tpPrice)
	if err != nil {
		utils.Logger.Error("Failed to place TP order", zap.Error(err))
		return
	}

	s.mu.Lock()
	if s.currentState == StateIdle {
		s.mu.Unlock()
		utils.Logger.Info("Cycle finished during TP update, cancelling new TP", zap.Int64("id", resp.OrderID))
		go s.exchange.CancelOrder(s.symbol, resp.OrderID)
		return
	}
	s.currentTPOrderID = resp.OrderID
	s.mu.Unlock()
}

func (s *MartingaleStrategy) fetchATR(interval string) float64 {
	klines, err := s.exchange.GetKlines(s.symbol, interval, 50)
	if err != nil {
		utils.Logger.Error("Failed to get klines",
			zap.String("symbol", s.symbol),
			zap.String("interval", interval),
			zap.Error(err))
		return 0
	}

	var highs, lows, closes []float64
	for _, k := range klines {
		h, _ := strconv.ParseFloat(k.High, 64)
		l, _ := strconv.ParseFloat(k.Low, 64)
		c, _ := strconv.ParseFloat(k.Close, 64)
		highs = append(highs, h)
		lows = append(lows, l)
		closes = append(closes, c)
	}

	return utils.CalculateATR(highs, lows, closes, s.cfg.AtrPeriod)
}

func (s *MartingaleStrategy) getFibonacci(n int) int {
	if n <= 0 {
		return 0
	}
	if n == 1 {
		return 1
	}
	if n == 2 {
		return 2
	}
	a, b := 1, 2
	for i := 3; i <= n; i++ {
		a, b = b, a+b
	}
	return b
}

// GetPositionValue returns the current position value in USDT
func (s *MartingaleStrategy) GetPositionValue() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.positionValue
}

// GetState returns the current state
func (s *MartingaleStrategy) GetState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentState
}

// GetSymbol returns the trading symbol
func (s *MartingaleStrategy) GetSymbol() string {
	return s.symbol
}

// GetDirection returns the strategy direction
func (s *MartingaleStrategy) GetDirection() Direction {
	return s.direction
}
