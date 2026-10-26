// Package sms delivers one-time passcodes. Provider selection is a runtime
// switch so the same binary runs against a log sink in development and a real
// gateway in production without code changes.
package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

type Sender interface {
	Send(ctx context.Context, phone, message string) error
}

type Config struct {
	Provider string
	BaseURL  string
	APIKey   string
	SenderID string
}

func (c Config) Enabled() bool {
	return c.Provider != "" && c.Provider != "log"
}

func NewSender(cfg Config, httpClient *http.Client) Sender {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	switch strings.ToLower(cfg.Provider) {
	case "msg91":
		return &MSG91Sender{
			baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
			authKey:  cfg.APIKey,
			senderID: cfg.SenderID,
			client:   httpClient,
		}
	default:
		return &LogSender{}
	}
}

// LogSender is the development default: it records what would have been sent.
type LogSender struct{}

func (s *LogSender) Send(ctx context.Context, phone, message string) error {
	slog.Info("SMS (dev mode, not actually sent)", "phone", maskPhone(phone), "message", message)
	return nil
}

type MSG91Sender struct {
	baseURL  string
	authKey  string
	senderID string
	client   *http.Client
}

func (s *MSG91Sender) Send(ctx context.Context, phone, message string) error {
	payload := map[string]any{
		"sender":  s.senderID,
		"route":   "4",
		"country": "91",
		"mobiles": "91" + phone,
		"message": message,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("authkey", s.authKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("msg91 send error: status %d", resp.StatusCode)
	}
	return nil
}

func maskPhone(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	return "****" + phone[len(phone)-4:]
}
