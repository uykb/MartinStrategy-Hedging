package config

import (
	"strings"

	"github.com/spf13/viper"
)

// Config is the root configuration
type Config struct {
	Exchange   ExchangeConfig    `mapstructure:"exchange"`
	Strategies []StrategyConfig  `mapstructure:"strategies"`
	Hedge      HedgeConfig       `mapstructure:"hedge"`
	Storage    StorageConfig     `mapstructure:"storage"`
	Log        LogConfig         `mapstructure:"log"`
}

// ExchangeConfig holds exchange connection details
type ExchangeConfig struct {
	ApiKey     string `mapstructure:"api_key"`
	ApiSecret  string `mapstructure:"api_secret"`
	UseTestnet bool   `mapstructure:"use_testnet"`
}

// StrategyConfig holds configuration for a single strategy instance
type StrategyConfig struct {
	Name            string  `mapstructure:"name"`
	Symbol          string  `mapstructure:"symbol"`
	Direction       string  `mapstructure:"direction"`
	Enabled         bool    `mapstructure:"enabled"`
	CapitalWeight   float64 `mapstructure:"capital_weight"`
	MaxSafetyOrders int     `mapstructure:"max_safety_orders"`
	AtrPeriod       int     `mapstructure:"atr_period"`
}

// HedgeConfig holds hedging coordination settings
type HedgeConfig struct {
	Enabled             bool    `mapstructure:"enabled"`
	Ratio               float64 `mapstructure:"ratio"`
	RebalanceThreshold  float64 `mapstructure:"rebalance_threshold"`
}

// StorageConfig holds storage connection details
type StorageConfig struct {
	SqlitePath string `mapstructure:"sqlite_path"`
	RedisAddr  string `mapstructure:"redis_addr"`
	RedisPass  string `mapstructure:"redis_pass"`
	RedisDB    int    `mapstructure:"redis_db"`
}

// LogConfig holds logging settings
type LogConfig struct {
	Level string `mapstructure:"level"`
}

func LoadConfig(path string) (*Config, error) {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	// Environment variables
	viper.SetEnvPrefix("MARTIN")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Set defaults for hedge config
	if cfg.Hedge.Ratio == 0 {
		cfg.Hedge.Ratio = 1.0
	}
	if cfg.Hedge.RebalanceThreshold == 0 {
		cfg.Hedge.RebalanceThreshold = 0.1
	}

	// Set defaults for each strategy
	for i := range cfg.Strategies {
		if cfg.Strategies[i].CapitalWeight == 0 {
			cfg.Strategies[i].CapitalWeight = 1.0
		}
		if cfg.Strategies[i].MaxSafetyOrders == 0 {
			cfg.Strategies[i].MaxSafetyOrders = 9
		}
		if cfg.Strategies[i].AtrPeriod == 0 {
			cfg.Strategies[i].AtrPeriod = 14
		}
	}

	return &cfg, nil
}
