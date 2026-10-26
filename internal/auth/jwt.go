package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"drivethrough/internal/config"
	"drivethrough/internal/models"
)

type Claims struct {
	UserID uuid.UUID       `json:"uid"`
	Phone  string          `json:"phone"`
	Role   models.UserRole `json:"role"`
	jwt.RegisteredClaims
}

type TokenIssuer interface {
	Issue(user *models.User) (*models.TokenPair, error)
	Parse(accessToken string) (*Claims, error)
	Logout(ctx context.Context, userID uuid.UUID, accessToken string) error
	Blacklist() *TokenBlacklist
}

type JWTManager struct {
	secret        []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
	issuer        string
	blacklist     *TokenBlacklist
}

func NewJWTManager(cfg config.JWTConfig, blacklist *TokenBlacklist) *JWTManager {
	return &JWTManager{
		secret:        []byte(cfg.Secret),
		accessExpiry:  cfg.AccessExpiry,
		refreshExpiry: cfg.RefreshExpiry,
		issuer:        cfg.Issuer,
		blacklist:     blacklist,
	}
}

func (m *JWTManager) Issue(user *models.User) (*models.TokenPair, error) {
	now := time.Now()

	accessClaims := Claims{
		UserID: user.ID,
		Phone:  user.Phone,
		Role:   user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessExpiry)),
			Issuer:    m.issuer,
		},
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := jwt.RegisteredClaims{
		Subject:   user.ID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.refreshExpiry)),
		Issuer:    m.issuer,
	}
	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("sign refresh token: %w", err)
	}

	return &models.TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    now.Add(m.accessExpiry).Unix(),
	}, nil
}

func (m *JWTManager) Parse(accessToken string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		accessToken,
		&Claims{},
		func(t *jwt.Token) (any, error) {
			// Pin the algorithm. Without this check a token signed with "none"
			// or an asymmetric algorithm could be accepted.
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return m.secret, nil
		},
		jwt.WithIssuer(m.issuer),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// Logout revokes an access token until it would have expired anyway. The
// caller's context is honoured so revocation is not abandoned on disconnect.
func (m *JWTManager) Logout(ctx context.Context, userID uuid.UUID, accessToken string) error {
	if m.blacklist == nil {
		return nil
	}
	return m.blacklist.Add(ctx, accessToken, m.accessExpiry)
}

func (m *JWTManager) Blacklist() *TokenBlacklist {
	return m.blacklist
}

type contextKey string

const ClaimsKey contextKey = "claims"

func WithUser(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, ClaimsKey, claims)
}

func UserFrom(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(ClaimsKey).(*Claims)
	return claims, ok
}
