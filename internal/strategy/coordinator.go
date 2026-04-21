package strategy

import (
	"strconv"
	"sync"
	"time"

	"github.com/uykb/MartinStrategy-Hedging/internal/config"
	"github.com/uykb/MartinStrategy-Hedging/internal/core"
	"github.com/uykb/MartinStrategy-Hedging/internal/exchange"
	"github.com/uykb/MartinStrategy-Hedging/internal/notifier"
	"github.com/uykb/MartinStrategy-Hedging/internal/utils"
	"go.uber.org/zap"
)

// HedgeStatus represents the current hedging state
type HedgeStatus struct {
	LongValue       float64
	ShortValue      float64
	TotalValue      float64
	Ratio           float64
	TargetRatio     float64
	Deviation       float64
	NeedRebalance   bool
	LongStrategies  []string
	ShortStrategies []string
}

// HedgeCoordinator manages multiple strategy instances and maintains hedge ratio
type HedgeCoordinator struct {
	mu          sync.RWMutex
	strategies  []*MartingaleStrategy
	exchange    *exchange.BinanceClient
	cfg         *config.HedgeConfig
	bus         *core.EventBus
	monitorStop chan struct{}
}

// NewHedgeCoordinator creates a new hedge coordinator
func NewHedgeCoordinator(strategies []*MartingaleStrategy, ex *exchange.BinanceClient, cfg *config.HedgeConfig, bus *core.EventBus) *HedgeCoordinator {
	return &HedgeCoordinator{
		strategies:  strategies,
		exchange:    ex,
		cfg:         cfg,
		bus:         bus,
		monitorStop: make(chan struct{}),
	}
}

// Start begins monitoring hedge positions
func (hc *HedgeCoordinator) Start() {
	if !hc.cfg.Enabled {
		utils.Logger.Info("Hedge coordinator disabled")
		return
	}

	utils.Logger.Info("Starting hedge coordinator",
		zap.Float64("target_ratio", hc.cfg.Ratio),
		zap.Float64("rebalance_threshold", hc.cfg.RebalanceThreshold),
	)

	go hc.monitorLoop()
}

// Stop stops the monitoring loop
func (hc *HedgeCoordinator) Stop() {
	close(hc.monitorStop)
}

func (hc *HedgeCoordinator) monitorLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-hc.monitorStop:
			utils.Logger.Info("Hedge coordinator stopped")
			return
		case <-ticker.C:
			hc.checkHedgeStatus()
		}
	}
}

func (hc *HedgeCoordinator) checkHedgeStatus() {
	status := hc.GetHedgeStatus()

	utils.Logger.Info("Hedge Status",
		zap.Float64("long_value", status.LongValue),
		zap.Float64("short_value", status.ShortValue),
		zap.Float64("total_value", status.TotalValue),
		zap.Float64("ratio", status.Ratio),
		zap.Float64("target_ratio", status.TargetRatio),
		zap.Float64("deviation_pct", status.Deviation),
		zap.Bool("need_rebalance", status.NeedRebalance),
	)

	if status.NeedRebalance {
		utils.Logger.Warn("Hedge ratio deviation exceeds threshold, consider rebalancing",
			zap.Float64("deviation", status.Deviation),
			zap.Float64("threshold", hc.cfg.RebalanceThreshold),
		)
		notifier.GetNotifier().NotifyHedgeAlert(
			status.LongValue,
			status.ShortValue,
			status.Ratio,
			status.TargetRatio,
			status.Deviation,
		)
	}
}

// GetHedgeStatus calculates and returns the current hedge status using real exchange data
func (hc *HedgeCoordinator) GetHedgeStatus() HedgeStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	var longValue, shortValue float64
	var longStrats, shortStrats []string

	for _, s := range hc.strategies {
		symbol := s.GetSymbol()
		direction := s.GetDirection()

		if direction == DirectionLong {
			longStrats = append(longStrats, symbol)
		} else {
			shortStrats = append(shortStrats, symbol)
		}

		pos, err := hc.exchange.GetPosition(symbol)
		if err != nil {
			utils.Logger.Warn("Failed to get position for hedge status",
				zap.String("symbol", symbol),
				zap.Error(err))
			continue
		}

		amt, _ := strconv.ParseFloat(pos.PositionAmt, 64)
		entryPrice, _ := strconv.ParseFloat(pos.EntryPrice, 64)
		notional := amt * entryPrice
		if notional < 0 {
			notional = -notional
		}

		if direction == DirectionLong {
			longValue += notional
		} else {
			shortValue += notional
		}

		utils.Logger.Debug("Position value for hedge",
			zap.String("symbol", symbol),
			zap.String("direction", string(direction)),
			zap.Float64("amount", amt),
			zap.Float64("entry_price", entryPrice),
			zap.Float64("notional", notional),
		)
	}

	totalValue := longValue + shortValue
	ratio := 0.0
	if shortValue > 0 {
		ratio = longValue / shortValue
	} else if longValue > 0 {
		ratio = 999.0
	}

	deviation := 0.0
	if hc.cfg.Ratio > 0 {
		deviation = (ratio - hc.cfg.Ratio) / hc.cfg.Ratio
		if deviation < 0 {
			deviation = -deviation
		}
	}

	needRebalance := deviation > hc.cfg.RebalanceThreshold

	return HedgeStatus{
		LongValue:       longValue,
		ShortValue:      shortValue,
		TotalValue:      totalValue,
		Ratio:           ratio,
		TargetRatio:     hc.cfg.Ratio,
		Deviation:       deviation,
		NeedRebalance:   needRebalance,
		LongStrategies:  longStrats,
		ShortStrategies: shortStrats,
	}
}

// GetStrategy returns a strategy by symbol
func (hc *HedgeCoordinator) GetStrategy(symbol string) *MartingaleStrategy {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	for _, s := range hc.strategies {
		if s.GetSymbol() == symbol {
			return s
		}
	}
	return nil
}

// GetAllStrategies returns all managed strategies
func (hc *HedgeCoordinator) GetAllStrategies() []*MartingaleStrategy {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.strategies
}