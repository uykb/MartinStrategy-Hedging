package exchange

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/uykb/MartinStrategy-Hedging/internal/config"
	"github.com/uykb/MartinStrategy-Hedging/internal/core"
	"github.com/uykb/MartinStrategy-Hedging/internal/utils"
	"go.uber.org/zap"
)

// SymbolInfo holds trading parameters for a symbol
type SymbolInfo struct {
	QuantityPrecision int
	PricePrecision    int
	MinQty            float64
	StepSize          float64
	TickSize          float64
}

// BinanceClient handles Binance Futures operations
type BinanceClient struct {
	client  *futures.Client
	cfg     *config.ExchangeConfig
	bus     *core.EventBus
	symbols map[string]*SymbolInfo
	mu      sync.RWMutex
}

// NewBinanceClient creates a new Binance client
func NewBinanceClient(cfg *config.ExchangeConfig, bus *core.EventBus) *BinanceClient {
	futures.UseTestnet = cfg.UseTestnet
	client := binance.NewFuturesClient(cfg.ApiKey, cfg.ApiSecret)

	return &BinanceClient{
		client:  client,
		cfg:     cfg,
		bus:     bus,
		symbols: make(map[string]*SymbolInfo),
	}
}

// InitSymbolInfo initializes symbol info for a given symbol
func (bc *BinanceClient) InitSymbolInfo(symbol string) error {
	info, err := bc.client.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get exchange info: %w", err)
	}

	var symbolInfo futures.Symbol
	found := false
	for _, sym := range info.Symbols {
		if sym.Symbol == symbol {
			symbolInfo = sym
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("symbol %s not found in exchange info", symbol)
	}

	si := &SymbolInfo{
		QuantityPrecision: symbolInfo.QuantityPrecision,
		PricePrecision:    symbolInfo.PricePrecision,
	}

	for _, filter := range symbolInfo.Filters {
		filterType, ok := filter["filterType"].(string)
		if !ok {
			continue
		}

		switch filterType {
		case "LOT_SIZE":
			if stepSize, ok := filter["stepSize"].(string); ok {
				si.StepSize, _ = strconv.ParseFloat(stepSize, 64)
			}
			if minQty, ok := filter["minQty"].(string); ok {
				si.MinQty, _ = strconv.ParseFloat(minQty, 64)
			}
		case "PRICE_FILTER":
			if tickSize, ok := filter["tickSize"].(string); ok {
				si.TickSize, _ = strconv.ParseFloat(tickSize, 64)
			}
		}
	}

	bc.mu.Lock()
	bc.symbols[symbol] = si
	bc.mu.Unlock()

	utils.Logger.Info("Symbol Info Initialized",
		zap.String("symbol", symbol),
		zap.Int("price_prec", si.PricePrecision),
		zap.Int("qty_prec", si.QuantityPrecision),
		zap.Float64("step_size", si.StepSize),
		zap.Float64("tick_size", si.TickSize),
		zap.Float64("min_qty", si.MinQty),
	)
	return nil
}

// GetSymbolInfo returns symbol info for a given symbol
func (bc *BinanceClient) GetSymbolInfo(symbol string) (*SymbolInfo, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	si, ok := bc.symbols[symbol]
	if !ok {
		return nil, fmt.Errorf("symbol info not initialized for %s", symbol)
	}
	return si, nil
}

// StartWS connects to websocket streams for multiple symbols
func (bc *BinanceClient) StartWS(symbols []string) error {
	// Sync Server Time first
	if _, err := bc.client.NewSetServerTimeService().Do(context.Background()); err != nil {
		utils.Logger.Error("Failed to sync server time", zap.Error(err))
	}

	// Connect to User Data Stream (Order Updates)
	listenKey, err := bc.client.NewStartUserStreamService().Do(context.Background())
	if err != nil {
		return fmt.Errorf("failed to start user stream: %w", err)
	}

	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			bc.client.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(context.Background())
		}
	}()

	// User Data WS
	wsUserHandler := func(event *futures.WsUserDataEvent) {
		switch event.Event {
		case futures.UserDataEventTypeOrderTradeUpdate:
			o := event.OrderTradeUpdate
			utils.Logger.Info("Order Update", zap.String("symbol", o.Symbol), zap.String("status", string(o.Status)))
			bc.bus.Publish(core.EventOrderUpdate, &o)
		case futures.UserDataEventTypeAccountUpdate:
			for _, p := range event.AccountUpdate.Positions {
				for _, sym := range symbols {
					if p.Symbol == sym {
						bc.bus.Publish(core.EventPositionUpdate, p)
					}
				}
			}
		}
	}

	errHandler := func(err error) {
		utils.Logger.Error("WS Error", zap.Error(err))
	}

	doneC, stopC, err := futures.WsUserDataServe(listenKey, wsUserHandler, errHandler)
	if err != nil {
		return err
	}

	// Connect to Market Streams (one per symbol)
	var doneMList []chan struct{}
	var stopMList []chan struct{}

	for _, symbol := range symbols {
		sym := symbol // capture for closure
		wsMarketHandler := func(event *futures.WsAggTradeEvent) {
			price, _ := strconv.ParseFloat(event.Price, 64)
			bc.bus.Publish(core.EventTick, core.TickData{Symbol: sym, Price: price})
		}

		doneM, stopM, err := futures.WsAggTradeServe(sym, wsMarketHandler, errHandler)
		if err != nil {
			return err
		}
		doneMList = append(doneMList, doneM)
		stopMList = append(stopMList, stopM)
	}

	go func() {
		<-doneC
		for _, doneM := range doneMList {
			<-doneM
		}
		close(stopC)
		for _, stopM := range stopMList {
			close(stopM)
		}
	}()

	return nil
}

// GetPosition returns position for a given symbol
func (bc *BinanceClient) GetPosition(symbol string) (*futures.AccountPosition, error) {
	acc, err := bc.client.NewGetAccountService().Do(context.Background())
	if err != nil {
		return nil, err
	}
	for _, p := range acc.Positions {
		if p.Symbol == symbol {
			return p, nil
		}
	}
	return nil, fmt.Errorf("position not found for %s", symbol)
}

// PlaceOrder places an order for a specific symbol
func (bc *BinanceClient) PlaceOrder(symbol string, side futures.SideType, orderType futures.OrderType, quantity, price float64) (*futures.CreateOrderResponse, error) {
	qtyStr := strconv.FormatFloat(quantity, 'f', -1, 64)
	service := bc.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		Type(orderType).
		Quantity(qtyStr)

	if orderType == futures.OrderTypeLimit {
		priceStr := strconv.FormatFloat(price, 'f', -1, 64)
		service.Price(priceStr).TimeInForce(futures.TimeInForceTypeGTC)
	}

	return service.Do(context.Background())
}

// CancelAllOrders cancels all open orders for a symbol
func (bc *BinanceClient) CancelAllOrders(symbol string) error {
	return bc.client.NewCancelAllOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())
}

// CancelOrder cancels a specific order
func (bc *BinanceClient) CancelOrder(symbol string, orderID int64) error {
	_, err := bc.client.NewCancelOrderService().
		Symbol(symbol).
		OrderID(orderID).
		Do(context.Background())
	return err
}

// GetOpenOrders returns open orders for a symbol
func (bc *BinanceClient) GetOpenOrders(symbol string) ([]*futures.Order, error) {
	return bc.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())
}

// GetKlines returns klines for a symbol
func (bc *BinanceClient) GetKlines(symbol, interval string, limit int) ([]*futures.Kline, error) {
	return bc.client.NewKlinesService().
		Symbol(symbol).
		Interval(interval).
		Limit(limit).
		Do(context.Background())
}

// CancelAllOrdersAllSymbols cancels all orders for all given symbols
func (bc *BinanceClient) CancelAllOrdersAllSymbols(symbols []string) error {
	for _, sym := range symbols {
		if err := bc.CancelAllOrders(sym); err != nil {
			utils.Logger.Error("Failed to cancel orders", zap.String("symbol", sym), zap.Error(err))
		}
	}
	return nil
}

// IsTestnet returns whether testnet is enabled
func (bc *BinanceClient) IsTestnet() bool {
	return bc.cfg.UseTestnet
}

// GetPrices returns all symbol prices
func (bc *BinanceClient) GetPrices() ([]*futures.SymbolPrice, error) {
	return bc.client.NewListPricesService().Do(context.Background())
}
