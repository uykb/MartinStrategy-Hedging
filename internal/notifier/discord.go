package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/uykb/MartinStrategy-Hedging/internal/config"
)

// DiscordNotifier sends notifications to Discord via webhook
type DiscordNotifier struct {
	webhookURL string
	enabled    bool
	cfg        *config.NotificationConfig
	client     *http.Client
}

// NewDiscordNotifier creates a new Discord notifier
func NewDiscordNotifier(cfg *config.NotificationConfig) *DiscordNotifier {
	return &DiscordNotifier{
		webhookURL: cfg.DiscordWebhookURL,
		enabled:    cfg.Enabled && cfg.DiscordWebhookURL != "",
		cfg:        cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type discordPayload struct {
	Content   string         `json:"content,omitempty"`
	Embeds    []discordEmbed `json:"embeds,omitempty"`
	Username  string         `json:"username,omitempty"`
	AvatarURL string         `json:"avatar_url,omitempty"`
}

type discordEmbed struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
	Footer      *discordFooter `json:"footer,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordFooter struct {
	Text string `json:"text"`
}

// NotifyOpen sends a position opened notification
func (n *DiscordNotifier) NotifyOpen(symbol, direction string, price, qty, value float64) {
	if !n.enabled || !n.cfg.NotifyOpen {
		return
	}

	color := 0x00ff00
	if direction == "SHORT" {
		color = 0xff0000
	}

	payload := discordPayload{
		Username:  "Martin Hedging Bot",
		AvatarURL: "https://cdn-icons-png.flaticon.com/512/2622/2622127.png",
		Embeds: []discordEmbed{
			{
				Title:       fmt.Sprintf("🟢 开仓通知 - %s %s", symbol, direction),
				Description: fmt.Sprintf("**%s** 已开仓", symbol),
				Color:       color,
				Fields: []discordField{
					{Name: "方向", Value: direction, Inline: true},
					{Name: "价格", Value: fmt.Sprintf("%.4f", price), Inline: true},
					{Name: "数量", Value: fmt.Sprintf("%.4f", qty), Inline: true},
					{Name: "仓位价值", Value: fmt.Sprintf("%.2f USDT", value), Inline: false},
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    &discordFooter{Text: "Martin Hedging Bot"},
			},
		},
	}

	n.send(payload)
}

// NotifyClose sends a position closed notification
func (n *DiscordNotifier) NotifyClose(symbol, direction string, price, qty, pnl float64) {
	if !n.enabled || !n.cfg.NotifyClose {
		return
	}

	color := 0x888888
	if pnl > 0 {
		color = 0x00ff00
	} else if pnl < 0 {
		color = 0xff0000
	}

	pnlStr := fmt.Sprintf("%.2f USDT", pnl)
	if pnl > 0 {
		pnlStr = fmt.Sprintf("+%.2f USDT", pnl)
	}

	payload := discordPayload{
		Username:  "Martin Hedging Bot",
		AvatarURL: "https://cdn-icons-png.flaticon.com/512/2622/2622127.png",
		Embeds: []discordEmbed{
			{
				Title:       fmt.Sprintf("⚪ 平仓通知 - %s %s", symbol, direction),
				Description: fmt.Sprintf("**%s** 已平仓", symbol),
				Color:       color,
				Fields: []discordField{
					{Name: "方向", Value: direction, Inline: true},
					{Name: "价格", Value: fmt.Sprintf("%.4f", price), Inline: true},
					{Name: "数量", Value: fmt.Sprintf("%.4f", qty), Inline: true},
					{Name: "盈亏", Value: pnlStr, Inline: false},
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    &discordFooter{Text: "Martin Hedging Bot"},
			},
		},
	}

	n.send(payload)
}

// NotifySafetyOrder sends a safety order filled notification
func (n *DiscordNotifier) NotifySafetyOrder(symbol, direction string, level, price, qty, avgPrice float64) {
	if !n.enabled || !n.cfg.NotifySafety {
		return
	}

	color := 0xffa500

	payload := discordPayload{
		Username:  "Martin Hedging Bot",
		AvatarURL: "https://cdn-icons-png.flaticon.com/512/2622/2622127.png",
		Embeds: []discordEmbed{
			{
				Title:       fmt.Sprintf("🟠 加仓通知 - %s %s 第%d层", symbol, direction, level),
				Description: fmt.Sprintf("**%s** 马丁加仓 #%d", symbol, level),
				Color:       color,
				Fields: []discordField{
					{Name: "方向", Value: direction, Inline: true},
					{Name: "加仓价格", Value: fmt.Sprintf("%.4f", price), Inline: true},
					{Name: "加仓数量", Value: fmt.Sprintf("%.4f", qty), Inline: true},
					{Name: "均价", Value: fmt.Sprintf("%.4f", avgPrice), Inline: false},
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    &discordFooter{Text: "Martin Hedging Bot"},
			},
		},
	}

	n.send(payload)
}

// NotifyTP sends a take-profit notification
func (n *DiscordNotifier) NotifyTP(symbol, direction string, tpPrice, qty float64) {
	if !n.enabled || !n.cfg.NotifyTP {
		return
	}

	color := 0x00bfff

	payload := discordPayload{
		Username:  "Martin Hedging Bot",
		AvatarURL: "https://cdn-icons-png.flaticon.com/512/2622/2622127.png",
		Embeds: []discordEmbed{
			{
				Title:       fmt.Sprintf("🔵 止盈挂单 - %s %s", symbol, direction),
				Description: fmt.Sprintf("**%s** 止盈单已更新", symbol),
				Color:       color,
				Fields: []discordField{
					{Name: "方向", Value: direction, Inline: true},
					{Name: "止盈价格", Value: fmt.Sprintf("%.4f", tpPrice), Inline: true},
					{Name: "数量", Value: fmt.Sprintf("%.4f", qty), Inline: true},
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    &discordFooter{Text: "Martin Hedging Bot"},
			},
		},
	}

	n.send(payload)
}

// NotifyHedgeAlert sends a hedge ratio deviation alert
func (n *DiscordNotifier) NotifyHedgeAlert(longValue, shortValue, ratio, targetRatio, deviation float64) {
	if !n.enabled || !n.cfg.NotifyHedgeAlert {
		return
	}

	color := 0xff4500

	payload := discordPayload{
		Username:  "Martin Hedging Bot",
		AvatarURL: "https://cdn-icons-png.flaticon.com/512/2622/2622127.png",
		Embeds: []discordEmbed{
			{
				Title:       "🚨 对冲比例告警",
				Description: "多空对冲比例偏离目标值",
				Color:       color,
				Fields: []discordField{
					{Name: "多头仓位", Value: fmt.Sprintf("%.2f USDT", longValue), Inline: true},
					{Name: "空头仓位", Value: fmt.Sprintf("%.2f USDT", shortValue), Inline: true},
					{Name: "当前比例", Value: fmt.Sprintf("%.2f", ratio), Inline: true},
					{Name: "目标比例", Value: fmt.Sprintf("%.2f", targetRatio), Inline: true},
					{Name: "偏差", Value: fmt.Sprintf("%.2f%%", deviation*100), Inline: true},
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    &discordFooter{Text: "Martin Hedging Bot"},
			},
		},
	}

	n.send(payload)
}

// NotifyHedgeStatus sends a periodic hedge status summary
func (n *DiscordNotifier) NotifyHedgeStatus(longValue, shortValue, ratio, targetRatio float64, longStrats, shortStrats []string) {
	if !n.enabled {
		return
	}

	color := 0x00bfff

	payload := discordPayload{
		Username:  "Martin Hedging Bot",
		AvatarURL: "https://cdn-icons-png.flaticon.com/512/2622/2622127.png",
		Embeds: []discordEmbed{
			{
				Title:       "📊 对冲状态报告",
				Description: "多空策略对冲状态",
				Color:       color,
				Fields: []discordField{
					{Name: "多头策略", Value: joinStr(longStrats, ", "), Inline: true},
					{Name: "空头策略", Value: joinStr(shortStrats, ", "), Inline: true},
					{Name: "多头仓位", Value: fmt.Sprintf("%.2f USDT", longValue), Inline: true},
					{Name: "空头仓位", Value: fmt.Sprintf("%.2f USDT", shortValue), Inline: true},
					{Name: "当前比例", Value: fmt.Sprintf("%.2f", ratio), Inline: true},
					{Name: "目标比例", Value: fmt.Sprintf("%.2f", targetRatio), Inline: true},
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    &discordFooter{Text: "Martin Hedging Bot"},
			},
		},
	}

	n.send(payload)
}

func joinStr(strs []string, sep string) string {
	if len(strs) == 0 {
		return "无"
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func (n *DiscordNotifier) send(payload discordPayload) {
	if n.webhookURL == "" {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Failed to marshal discord payload: %v\n", err)
		return
	}

	resp, err := n.client.Post(n.webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		fmt.Printf("Failed to send discord notification: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Printf("Discord webhook returned status: %d\n", resp.StatusCode)
	}
}

// GetNotifier returns the global notifier instance
var globalNotifier *DiscordNotifier

// InitNotifier initializes the global notifier
func InitNotifier(cfg *config.NotificationConfig) {
	globalNotifier = NewDiscordNotifier(cfg)
}

// GetNotifier returns the global notifier
func GetNotifier() *DiscordNotifier {
	return globalNotifier
}
