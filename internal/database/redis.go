package database

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"drivethrough/internal/config"
)

func NewRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	opts := redis.Options{
		Addr:        cfg.Address(),
		Password:    cfg.Password,
		DB:          cfg.DB,
		DialTimeout: cfg.ConnectTimeout,
	}

	if u, err := url.Parse(cfg.URL); err == nil && u.Scheme != "" {
		applyRedisURL(&opts, u)
	}

	if cfg.UseTLS || strings.HasPrefix(opts.Addr, "rediss://") {
		opts.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // opt-in for self-signed staging Redis
		}
	}

	client := redis.NewClient(&opts)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("unable to connect to Redis: %w", err)
	}

	slog.Info("connected to Redis", "address", opts.Addr, "db", opts.DB, "tls", opts.TLSConfig != nil)

	return client, nil
}

func applyRedisURL(opts *redis.Options, u *url.URL) {
	opts.Addr = u.Host
	if u.User != nil {
		if pass, ok := u.User.Password(); ok {
			opts.Password = pass
		}
	}
	if db := strings.Trim(u.Path, "/"); db != "" {
		var idx int
		if _, err := fmt.Sscanf(db, "%d", &idx); err == nil {
			opts.DB = idx
		}
	}
	for q, values := range u.Query() {
		if len(values) == 0 {
			continue
		}
		switch strings.ToLower(q) {
		case "tls", "rediss":
			if values[0] != "false" && values[0] != "0" {
				opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			}
		case "dial_timeout":
			opts.DialTimeout = parseDurationOrSeconds(values[0], opts.DialTimeout)
		case "read_timeout":
			opts.ReadTimeout = parseDurationOrSeconds(values[0], opts.ReadTimeout)
		case "write_timeout":
			opts.WriteTimeout = parseDurationOrSeconds(values[0], opts.WriteTimeout)
		}
	}
}

func parseDurationOrSeconds(raw string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
