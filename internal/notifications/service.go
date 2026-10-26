package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"drivethrough/internal/config"
	"drivethrough/internal/fcm"
	"drivethrough/internal/models"
)

type Service struct {
	pool *pgxpool.Pool
	fcm  *fcm.Sender
	// pushTimeout bounds a single device delivery so one unreachable token
	// cannot hold the caller's request open.
	pushTimeout time.Duration
}

func NewService(pool *pgxpool.Pool, cfg config.FCMConfig, httpClient *http.Client) *Service {
	var sender *fcm.Sender

	// The token source is what makes FCM work at all: the v1 API requires an
	// OAuth2 bearer token, and it is only obtainable from the service account
	// key. A bad key disables push rather than failing server construction.
	if cfg.ProjectID != "" && cfg.ServiceAccountKey != "" {
		tokens, err := fcm.NewTokenSource(cfg.ServiceAccountKey, httpClient)
		if err != nil {
			slog.Error("fcm disabled: could not load service account key", "error", err)
		} else {
			sender = fcm.NewSender(cfg.BaseURL, cfg.ProjectID, tokens, httpClient)
		}
	}

	return &Service{
		pool:        pool,
		fcm:         sender,
		pushTimeout: 10 * time.Second,
	}
}

func (s *Service) Enabled() bool { return s.fcm != nil && s.fcm.Enabled() }

func (s *Service) Notify(ctx context.Context, userID uuid.UUID, notifType models.NotificationType, title, body string, data map[string]string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, body, data)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, string(notifType), title, body, jsonMap(data)); err != nil {
		return fmt.Errorf("store notification: %w", err)
	}

	tokens, err := s.DeviceTokens(ctx, userID)
	if err != nil {
		slog.Warn("failed to fetch device tokens", "user_id", userID, "error", err)
		return nil
	}

	// Delivery is asynchronous with respect to the stored notification: the row
	// above is already the durable record, so a push failure must not fail the
	// caller, which is usually handling a booking transition.
	for _, t := range tokens {
		s.deliver(ctx, userID, t, title, body, data)
	}

	return nil
}

// deliver pushes to one device and prunes the token when FCM reports it dead.
func (s *Service) deliver(ctx context.Context, userID uuid.UUID, token models.DeviceToken, title, body string, data map[string]string) {
	pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.pushTimeout)
	defer cancel()

	discard, err := s.fcm.Send(pushCtx, fcm.Message{
		Token: token.Token,
		Title: title,
		Body:  body,
		Data:  data,
	})
	if err != nil {
		// The token is not logged: device tokens are bearer credentials for the
		// user's push inbox.
		slog.Warn("failed to send push", "user_id", userID, "error", err)
	}
	if discard {
		if err := s.removeDeviceToken(pushCtx, userID, token.Token); err != nil {
			slog.Warn("failed to prune dead device token", "user_id", userID, "error", err)
		}
	}
}

func (s *Service) removeDeviceToken(ctx context.Context, userID uuid.UUID, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device_tokens WHERE user_id = $1 AND token = $2`, userID, token)
	return err
}

func jsonMap(m map[string]string) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, _ := json.Marshal(m)
	return b
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]models.Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, type, title, body, data, is_read, created_at
		FROM notifications WHERE user_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.Notification
	for rows.Next() {
		var n models.Notification
		var data []byte
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body, &data, &n.IsRead, &n.CreatedAt); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			_ = json.Unmarshal(data, &n.Data)
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

func (s *Service) MarkRead(ctx context.Context, userID, notifID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE notifications SET is_read = TRUE WHERE id = $1 AND user_id = $2
	`, notifID, userID)
	return err
}

func (s *Service) RegisterDeviceToken(ctx context.Context, userID uuid.UUID, token, platform string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO device_tokens (user_id, token, platform)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, token) DO NOTHING
	`, userID, token, platform)
	return err
}

func (s *Service) DeviceTokens(ctx context.Context, userID uuid.UUID) ([]models.DeviceToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, token, platform, created_at FROM device_tokens WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.DeviceToken
	for rows.Next() {
		var t models.DeviceToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Token, &t.Platform, &t.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}
