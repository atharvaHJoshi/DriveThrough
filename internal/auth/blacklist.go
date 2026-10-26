package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type TokenBlacklist struct {
	redis *redis.Client
}

func NewTokenBlacklist(redis *redis.Client) *TokenBlacklist {
	return &TokenBlacklist{redis: redis}
}

func (b *TokenBlacklist) Add(ctx context.Context, token string, expiry time.Duration) error {
	key := blacklistKey(token)
	return b.redis.Set(ctx, key, "1", expiry).Err()
}

func (b *TokenBlacklist) IsBlacklisted(ctx context.Context, token string) (bool, error) {
	key := blacklistKey(token)
	exists, err := b.redis.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

func blacklistKey(token string) string {
	hash := sha256.Sum256([]byte(token))
	return fmt.Sprintf("blacklist:%x", hash)
}
