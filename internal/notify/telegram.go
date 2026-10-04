package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/basicfu/fp/internal/domain"
)

const telegramBase = "https://api.telegram.org"

var telegramSpec = TypeSpec{
	Type:    "telegram",
	Channel: domain.NotifyChannelTelegram,
	ConfigSchema: []domain.Field{
		{Key: "botToken", Label: "Bot Token", Type: domain.FieldTypeSecret, Required: true},
		{Key: "chatId", Label: "Chat ID", Type: domain.FieldTypeString, Required: true, Help: "群组是负数，频道可填 @频道名"},
	},
	New: func(cfg Config) (Provider, error) {
		return &telegramProvider{baseURL: telegramBase, token: cfg.String("botToken"), chatID: cfg.String("chatId")}, nil
	},
}

type telegramProvider struct{ baseURL, token, chatID string }

func (p *telegramProvider) Send(ctx context.Context, d Delivery) error {
	status, body, err := postJSON(ctx, p.baseURL+"/bot"+p.token+"/sendMessage",
		map[string]string{"chat_id": p.chatID, "text": d.Rendered.Body})
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &out)
	if status != http.StatusOK || !out.OK {
		return fmt.Errorf("telegram: HTTP %d %s", status, out.Description)
	}
	return nil
}
