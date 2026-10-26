package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"
)

// OTPManager issues and checks the one-time passcodes used for phone login.
// Every method takes a context so a cancelled request abandons the Redis work
// instead of running to completion behind the client's back.
type OTPManager struct {
	redis  *redis.Client
	expiry time.Duration
	length int
}

func NewOTPManager(redis *redis.Client, expiry time.Duration, length int) *OTPManager {
	return &OTPManager{redis: redis, expiry: expiry, length: length}
}

// Generate creates a code and stores it. The caller is responsible for sending
// it; the code is never logged.
func (m *OTPManager) Generate(ctx context.Context, phone string) (string, error) {
	code, err := generateCode(m.length)
	if err != nil {
		return "", err
	}

	if err := m.redis.Set(ctx, otpKey(phone), code, m.expiry).Err(); err != nil {
		return "", fmt.Errorf("store otp: %w", err)
	}
	return code, nil
}

// Verify consumes the code. A successful check deletes it, so a code is
// single-use even if it is replayed within its validity window.
func (m *OTPManager) Verify(ctx context.Context, phone, code string) error {
	key := otpKey(phone)
	stored, err := m.redis.Get(ctx, key).Result()
	switch {
	case err == redis.Nil:
		return fmt.Errorf("otp expired or not requested")
	case err != nil:
		return fmt.Errorf("get otp: %w", err)
	}

	// Constant-time compare: a timing side channel here would let an attacker
	// recover the code digit by digit.
	if subtle.ConstantTimeCompare([]byte(stored), []byte(code)) != 1 {
		return fmt.Errorf("invalid otp")
	}

	if err := m.redis.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("clear otp: %w", err)
	}
	return nil
}

func (m *OTPManager) Set(ctx context.Context, phone, code string) error {
	return m.redis.Set(ctx, otpKey(phone), code, m.expiry).Err()
}

func otpKey(phone string) string {
	return "otp:" + phone
}

func generateCode(length int) (string, error) {
	const digits = "0123456789"
	code := make([]byte, length)
	max := big.NewInt(int64(len(digits)))
	for i := range code {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		code[i] = digits[n.Int64()]
	}
	return string(code), nil
}
