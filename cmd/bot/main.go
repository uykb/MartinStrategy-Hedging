package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/uykb/MartinStrategy-Hedging/internal/config"
	"github.com/uykb/MartinStrategy-Hedging/internal/core"
	"github.com/uykb/MartinStrategy-Hedging/internal/exchange"
	"github.com/uykb/MartinStrategy-Hedging/internal/notifier"
	"github.com/uykb/MartinStrategy-Hedging/internal/storage"
	"github.com/uykb/MartinStrategy-Hedging/internal/strategy"
	"github.com/uykb/MartinStrategy-Hedging/internal/utils"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		panic(err)
	}

	if err := utils.InitLogger(cfg.Log.Level); err != nil {
		panic(err)
	}
	defer utils.Logger.Sync()

	notifier.InitNotifier(&cfg.Notification)

	utils.Logger.Info("Starting MartinStrategy Hedging Bot",
		zap.Int("strategies", len(cfg.Strategies)),
		zap.Bool("discord_enabled", cfg.Notification.Enabled),
	)

	db, err := storage.InitStorage(cfg.Storage.SqlitePath, cfg.Storage.RedisAddr, cfg.Storage.RedisPass, cfg.Storage.RedisDB)
	if err != nil {
		utils.Logger.Fatal("Failed to init storage", zap.Error(err))
	}

	bus := core.NewEventBus()
	bus.Start()
	defer bus.Stop()

	ex := exchange.NewBinanceClient(&cfg.Exchange, bus)

	var symbols []string
	for _, stratCfg := range cfg.Strategies {
		if stratCfg.Enabled {
			symbols = append(symbols, stratCfg.Symbol)
		}
	}

	if len(symbols) == 0 {
		utils.Logger.Fatal("No enabled strategies found")
	}

	for _, sym := range symbols {
		if err := ex.InitSymbolInfo(sym); err != nil {
			utils.Logger.Fatal("Failed to init symbol info", zap.String("symbol", sym), zap.Error(err))
		}
	}

	if err := ex.StartWS(symbols); err != nil {
		utils.Logger.Fatal("Failed to start exchange WS", zap.Error(err))
	}

	var strategies []*strategy.MartingaleStrategy
	for _, stratCfg := range cfg.Strategies {
		if !stratCfg.Enabled {
			utils.Logger.Info("Skipping disabled strategy", zap.String("name", stratCfg.Name))
			continue
		}

		utils.Logger.Info("Starting strategy",
			zap.String("name", stratCfg.Name),
			zap.String("symbol", stratCfg.Symbol),
			zap.String("direction", stratCfg.Direction),
			zap.Float64("capital_weight", stratCfg.CapitalWeight),
		)

		strat := strategy.NewMartingaleStrategy(&stratCfg, ex, db, bus)
		strategies = append(strategies, strat)
		go strat.Start()
	}

	var coordinator *strategy.HedgeCoordinator
	if cfg.Hedge.Enabled && len(strategies) > 1 {
		coordinator = strategy.NewHedgeCoordinator(strategies, ex, &cfg.Hedge, bus)
		coordinator.Start()
		defer coordinator.Stop()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	utils.Logger.Info("Shutting down...")

	if err := ex.CancelAllOrdersAllSymbols(symbols); err != nil {
		utils.Logger.Error("Failed to cancel orders on shutdown", zap.Error(err))
	} else {
		utils.Logger.Info("All orders cancelled")
	}

	utils.Logger.Info("Shutdown complete")
}
