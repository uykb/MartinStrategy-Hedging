# MartinStrategy-Hedging

基于 Go 语言的高性能多空马丁格尔策略交易机器人，采用 **事件驱动 + 有限状态机 (ED-FSM)** 架构，专为 BinanceFutures 设计。

支持 **多空对冲** 模式，可同时运行多个策略实例并自动维持对冲比例。

## 特性

- **多空对冲**: 同时运行做多和做空策略，自动维持对冲比例
- **资金比例**: 支持自定义多空资金比例（如 2:1）
- **Discord 推送**: 实时推送开仓、平仓、加仓、止盈、对冲告警等消息
- **事件驱动架构**: 基于 EventBus 的异步消息处理，高并发低延迟
- **有限状态机**: 清晰的状态流转，避免逻辑混乱
- **多交易对支持**: 可同时交易多个币种
- **对冲协调器**: 实时监控多空仓位，确保对冲比例
- **并发安全**: 互斥锁保护关键操作，防止重复下单
- **生产就绪**: 完善的日志、错误处理和监控计数器
- **Docker 支持**: 一键部署，跨平台兼容

## 目录结构

```
.
├── cmd/
│   └── bot/
│       └── main.go        # 入口文件
├── internal/
│   ├── config/            # 配置管理 (Viper)
│   ├── core/               # 核心组件 (EventBus)
│   ├── exchange/           # 交易所适配 (Binance WebSocket)
│   ├── strategy/           # 策略逻辑 (Martingale FSM + HedgeCoordinator)
│   ├── storage/            # 数据存储 (SQLite, Redis)
│   ├── notifier/           # 通知推送 (Discord Webhook)
│   └── utils/              # 工具库 (Logger, ATR)
├── config.yaml             # 配置文件
├── docker-compose.yml      # Docker Compose
├── Dockerfile              # 构建文件
└── go.mod                  # 依赖管理
```

## 快速开始

### 方式一: Docker Compose (推荐)

```bash
# 1. 创建配置文件
cp config.yaml.example config.yaml

# 2. 编辑配置 (填入API密钥)
vim config.yaml

# 3. 启动服务
docker-compose up -d

# 4. 查看日志
docker-compose logs -f
```

### 方式二: 本地运行

```bash
# 1. 安装依赖
go mod tidy

# 2. 编辑配置
vim config.yaml

# 3. 运行
go run cmd/bot/main.go
```

## 配置说明

### config.yaml

```yaml
exchange:
  api_key: ""              # Binance API Key
  api_secret: ""           # Binance API Secret
  use_testnet: false       # 是否使用测试网

strategies:
  - name: "HYPE_Long"      # 策略名称
    symbol: "HYPEUSDT"     # 交易对
    direction: "long"      # 方向: long 或 short
    enabled: true          # 是否启用
    capital_weight: 2.0    # 资金权重 (做多2份)
    max_safety_orders: 9   # 最大加仓层数
    atr_period: 14         # ATR 周期

  - name: "SOL_Short"
    symbol: "SOLUSDT"
    direction: "short"
    enabled: true
    capital_weight: 1.0    # 做空1份 (2:1 比例)
    max_safety_orders: 9
    atr_period: 14

hedge:
  enabled: true            # 是否启用对冲协调
  ratio: 2.0               # 目标对冲比例 (long/short = 2:1)
  rebalance_threshold: 0.15 # 偏差超过15%时告警

notification:
  discord_webhook_url: ""  # Discord Webhook URL
  enabled: true            # 是否启用通知
  notify_open: true        # 开仓通知
  notify_close: true       # 平仓通知
  notify_safety: true      # 加仓通知
  notify_tp: true          # 止盈通知
  notify_hedge_alert: true # 对冲告警

storage:
  sqlite_path: "bot.db"    # SQLite 数据库路径
  redis_addr: "localhost:6379"
  redis_pass: ""
  redis_db: 0

log:
  level: "info"            # 日志级别: debug, info, warn, error
```

### 环境变量

支持通过环境变量覆盖配置，前缀为 `MARTIN_`：

```bash
export MARTIN_EXCHANGE_API_KEY="your_api_key"
export MARTIN_EXCHANGE_API_SECRET="your_api_secret"
```

## 策略逻辑

### 状态机

```
┌─────────┐     Tick(IDLE)      ┌──────────────┐
│  IDLE   │────────────────────▶│ PLACING_GRID │
│ (空仓)   │                     │  (挂网格单)   │
└─────────┘                     └──────────────┘│
     ▲                                 │
     │                                 │ OrderFilled
     │                                 ▼
     │                         ┌──────────────┐
     │      TPFilled           │ IN_POSITION  │
     └────────────────────────│   (持仓中)    │──────────────┐
                               └──────────────┘              │
                                     │                      │
                                     │ SafetyFilled         │
                                     ▼                      │
                               更新TP止盈单                  │
```

### 多空对冲

```
┌─────────────────────────────────────────────────────────┐
│              HedgeCoordinator                            │
│  - 监控多空仓位价值                                       │
│  - 计算对冲比例                                           │
│  - 偏差超阈值时告警                                       │
└─────────────────────────────────────────────────────────┘
           │                              │
           ▼                              ▼
  ┌─────────────────┐          ┌─────────────────┐
  │ LongStrategy    │          │ ShortStrategy   │
  │ (HYPEUSDT)      │          │ (SOLUSDT)       │
  │ - 做多马丁       │          │ - 做空马丁       │
  │ - 向下挂单       │          │ - 向上挂单       │
  └─────────────────┘          └─────────────────┘
```

### 马丁策略

1. **开仓**: IDLE 状态收到 Tick 事件，市价开底仓
   - 做多: 买入开仓
   - 做空: 卖出开仓
2. **网格挂单**: 根据 Fibonacci 序列计算各层加仓数量，按ATR 距离递进挂单
   - 做多: 价格下跌时加仓（向下挂单）
   - 做空: 价格上涨时加仓（向上挂单）
3. **止盈**: 基于 15m ATR 设置止盈价位
   - 做多: 均价 + ATR
   - 做空: 均价 - ATR

### Fibonacci 加仓倍数

| 层数 | 倍数 | 数量 (假设unit=1) |
|------|------|-------------------|
| 1    | 1    | 1                 |
| 2    | 1    | 1                 |
| 3    | 2    | 2                 |
| 4    | 3    | 3                 |
| 5    | 5    | 5                 |
| 6    | 8    | 8                 |
| 7    | 13   | 13                |
| 8    | 21   | 21                |
| 9    | 34   | 34                |

## Discord 通知推送

### 配置 Webhook

1. 在 Discord 频道设置中创建 Webhook
2. 复制 Webhook URL 到 `config.yaml` 的 `notification.discord_webhook_url`

### 推送事件类型

| 事件 | 配置项 | 说明 | 颜色 |
|------|--------|------|------|
| 🟢 开仓 | `notify_open` | 底仓成交 | 绿色/红色 |
| 🟠 加仓 | `notify_safety` | 马丁加仓成交 | 橙色 |
| 🔵 止盈 | `notify_tp` | 止盈单挂单更新 | 蓝色 |
| ⚪ 平仓 | `notify_close` | 止盈/手动平仓 | 绿/灰/红 |
| 🚨 对冲告警 | `notify_hedge_alert` | 对冲比例偏离阈值 | 红橙 |

### 通知示例

```
🟢 开仓通知 - HYPEUSDT LONG
HYPEUSDT 已开仓
方向: LONG
价格: 39.6380
数量: 2.5200
仓位价值: 100.00 USDT

🟠 加仓通知 - SOLUSDT SHORT 第3层
SOLUSDT 马丁加仓 #3
方向: SHORT
加仓价格: 155.0000
加仓数量: 0.6500
均价: 152.5000

🚨 对冲比例告警
多空对冲比例偏离目标值
多头仓位: 2000.00 USDT
空头仓位: 800.00 USDT
当前比例: 2.50
目标比例: 2.00
偏差: 25.00%
```

## 对冲状态监控

### HedgeStatus 字段

| 字段 | 说明 |
|------|------|
| `long_value` | 多头仓位总价值 (USDT) |
| `short_value` | 空头仓位总价值 (USDT) |
| `ratio` | 当前对冲比例 (long/short) |
| `target_ratio` | 目标对冲比例 |
| `deviation` | 偏差百分比 |
| `need_rebalance` | 是否需要重新平衡 |

### 示例日志

```json
{"level":"info","msg":"Hedge Status","long_value":1000.0,"short_value":1000.0,"ratio":1.0,"target_ratio":1.0,"deviation_pct":0.0,"need_rebalance":false}
{"level":"warn","msg":"Hedge ratio deviation exceeds threshold","deviation":0.15,"threshold":0.1}
```

## 并发安全机制

### 1. 防重入锁

```go
if !s.gridMu.TryLock() {
    s.gridSkipCount++
    return
}
defer s.gridMu.Unlock()
```

### 2. 状态原子操作

```go
s.mu.Lock()
if s.currentState != StateIdle {
    s.mu.Unlock()
    return
}
s.currentState = StatePlacingGrid
s.mu.Unlock()

// 网络请求在锁外执行
```

## 风险提示

- 马丁格尔策略在单边行情中风险极高
- 多空对冲可降低风险但不能完全消除
- 同时运行多空策略会占用双倍保证金
- 建议设置止损或限制最大持仓
- 请确保API Key 有合约交易权限
- 建议先测试网验证策略

## 技术栈

| 组件 | 技术 |
|------|------|
| 语言 | Go 1.21+ |
| 交易所 | Binance Futures WebSocket |
| 存储 | SQLite / Redis |
| 配置 | Viper |
| 日志 | Zap (结构化) |
| 指标 | go-talib (ATR) |

## 开发

```bash
# 运行测试
go test ./...

# 构建
go build -o bot cmd/bot/main.go

# 代码检查
go vet ./...
```

## License

MIT License
