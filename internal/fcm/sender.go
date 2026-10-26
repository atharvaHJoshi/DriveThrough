package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sender delivers messages through the FCM HTTP v1 API.
type Sender struct {
	baseURL   string
	projectID string
	tokens    *TokenSource
	http      *http.Client
}

// NewSender builds a sender. A nil token source is tolerated so the caller can
// construct a disabled sender for local development.
func NewSender(baseURL, projectID string, tokens *TokenSource, httpClient *http.Client) *Sender {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Sender{
		baseURL:   strings.TrimRight(baseURL, "/"),
		projectID: projectID,
		tokens:    tokens,
		http:      httpClient,
	}
}

func (s *Sender) Enabled() bool {
	return s != nil && s.projectID != "" && s.tokens != nil
}

// Message is one push notification.
type Message struct {
	Token string
	Title string
	Body  string
	Data  map[string]string
}

// Send delivers a single message and returns whether the device token should be
// discarded.
//
// A token reported as unregistered is permanently invalid, so the caller must
// remove it; retrying it would fail forever and would eventually get the sender
// throttled.
func (s *Sender) Send(ctx context.Context, msg Message) (discardToken bool, err error) {
	if !s.Enabled() {
		return false, nil
	}

	accessToken, err := s.tokens.Token(ctx)
	if err != nil {
		return false, err
	}

	payload := map[string]any{
		"message": map[string]any{
			"token": msg.Token,
			"notification": map[string]string{
				"title": msg.Title,
				"body":  msg.Body,
			},
			// FCM requires data values to be strings, so this has to be a
			// map[string]string rather than arbitrary JSON.
			"data": msg.Data,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	endpoint := s.baseURL + "/v1/projects/" + url.PathEscape(s.projectID) + "/messages:send"
	resp, err := s.do(ctx, endpoint, accessToken, body)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		// The cached bearer token is stale or was revoked. Drop it so the next
		// send re-mints, and let the caller retry.
		s.tokens.Invalidate()
		return false, fmt.Errorf("fcm: unauthorized (token invalidated)")
	}

	if resp.StatusCode >= 400 {
		discard := isUnregistered(responseBody)
		return discard, fmt.Errorf("fcm: send failed: status %d: %s", resp.StatusCode, string(responseBody))
	}

	return false, nil
}

func (s *Sender) do(ctx context.Context, endpoint, accessToken string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	return s.http.Do(req)
}

// isUnregistered detects FCM's terminal invalid-token conditions. The details
// block carries the reason as a snake_case string.
func isUnregistered(body []byte) bool {
	var errResp struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type      string `json:"type"`
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return false
	}

	switch errResp.Error.Status {
	case "NOT_FOUND", "INVALID_ARGUMENT":
	default:
		return false
	}
	for _, d := range errResp.Error.Details {
		if strings.Contains(strings.ToLower(d.ErrorCode), "registration-token-not-registered") ||
			strings.Contains(strings.ToLower(d.ErrorCode), "invalid-argument") {
			return true
		}
	}
	return false
}
