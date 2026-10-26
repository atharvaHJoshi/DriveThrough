// Package fcm talks to the Firebase Cloud Messaging HTTP v1 API.
//
// FCM v1 authenticates with a short-lived OAuth2 access token, not with the
// service account key itself. The key is only used to mint a signed assertion
// that is exchanged for a bearer token, which is then reused until it is close
// to expiry. Sending a request without that header is rejected with 401, which
// is why Push previously never delivered a notification.
package fcm

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Scope is the OAuth2 scope required to send messages.
const Scope = "https://www.googleapis.com/auth/firebase.messaging"

const defaultTokenURI = "https://oauth2.googleapis.com/token"

// serviceAccount is the subset of the Google service account JSON key that is
// needed to authenticate.
type serviceAccount struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	ProjectID   string `json:"project_id"`
}

// tokenResponse is the OAuth2 token endpoint response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// TokenSource mints and caches FCM bearer tokens.
//
// A mutex guards the cache because the notification fan-out is concurrent and
// every sender hitting the token endpoint on each push would be both slow and a
// reliable way to get rate limited.
type TokenSource struct {
	clientEmail string
	privateKey  *rsa.PrivateKey
	tokenURI    string

	http *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	// now is injectable so the caching logic can be tested without sleeping.
	now func() time.Time
}

// NewTokenSource builds a token source from a service account JSON key. The key
// may be supplied either as a path or as the JSON document itself, since both
// are common ways of wiring the value into an environment variable.
func NewTokenSource(serviceAccountJSON string, httpClient *http.Client) (*TokenSource, error) {
	raw, err := readKey(serviceAccountJSON)
	if err != nil {
		return nil, err
	}

	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("fcm: parse service account key: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("fcm: service account key is missing client_email or private_key")
	}

	key, err := parsePrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}

	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = defaultTokenURI
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	return &TokenSource{
		clientEmail: sa.ClientEmail,
		privateKey:  key,
		tokenURI:    tokenURI,
		http:        httpClient,
		now:         time.Now,
	}, nil
}

// readKey resolves the configured value to raw JSON, accepting either a
// filesystem path or an inline document.
func readKey(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, errors.New("fcm: service account key is empty")
	}
	if strings.HasPrefix(trimmed, "{") {
		return []byte(trimmed), nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("fcm: read service account key: %w", err)
	}
	return data, nil
}

func parsePrivateKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("fcm: private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("fcm: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("fcm: private key is not RSA")
	}
	return key, nil
}

// Token returns a cached bearer token, minting a new one when the current token
// is missing or close to expiry.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Refresh a minute early so a token cannot expire mid-flight.
	if t.token != "" && t.now().Add(time.Minute).Before(t.expires) {
		return t.token, nil
	}

	token, expires, err := t.mint(ctx)
	if err != nil {
		return "", err
	}
	t.token = token
	t.expires = expires
	return token, nil
}

// mint performs the JWT bearer grant. It must be called with t.mu held.
func (t *TokenSource) mint(ctx context.Context) (string, time.Time, error) {
	now := t.now()
	// Google rejects assertions with a lifetime beyond one hour.
	expiry := now.Add(55 * time.Minute)

	assertion, err := t.assertion(now, expiry)
	if err != nil {
		return "", time.Time{}, err
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("fcm: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("fcm: read token response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", time.Time{}, fmt.Errorf("fcm: token request failed: status %d: %s", resp.StatusCode, string(body))
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("fcm: parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, errors.New("fcm: token response contained no access_token")
	}

	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return out.AccessToken, now.Add(ttl), nil
}

func (t *TokenSource) assertion(now, expiry time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iss":   t.clientEmail,
		"scope": Scope,
		"aud":   t.tokenURI,
		"iat":   now.Unix(),
		"exp":   expiry.Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(t.privateKey)
}

// Invalidate drops the cached token so the next call re-mints. Used after FCM
// rejects a request with 401, which means the cached token went stale.
func (t *TokenSource) Invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = ""
	t.expires = time.Time{}
}
