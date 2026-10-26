package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server      ServerConfig
	Database    DatabaseConfig
	Redis       RedisConfig
	JWT         JWTConfig
	OTP         OTPConfig
	Razorpay    RazorpayConfig
	FCM         FCMConfig
	Google      GoogleConfig
	WS          WSConfig
	Pricing     PricingConfig
	Matching    MatchingConfig
	Log         LogConfig
	SMS         SMSConfig
	HTTP        HTTPConfig
	RateLimit   RateLimitConfig
	Workers     WorkerConfig
	Environment string
}

type ServerConfig struct {
	Port            string
	Env             string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
	AllowedOrigins  []string
	// TrustedProxies lists the peers permitted to set X-Forwarded-For. Empty
	// means no peer is trusted and the raw remote address is used.
	TrustedProxies []string
}

func (s ServerConfig) IsProduction() bool {
	return s.Env == "production" || s.Env == "prod"
}

type DatabaseConfig struct {
	URL             string
	Host            string
	Port            int
	User            string
	Password        string
	DBName          string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
	RunMigrations   bool
}

type RedisConfig struct {
	URL            string
	Host           string
	Port           string
	Password       string
	DB             int
	UseTLS         bool
	TLSSkipVerify  bool
	ConnectTimeout time.Duration
}

type JWTConfig struct {
	Secret        string
	AccessExpiry  time.Duration
	RefreshExpiry time.Duration
	Issuer        string
}

type OTPConfig struct {
	Expiry time.Duration
	Length int
}

type RazorpayConfig struct {
	BaseURL    string
	KeyID      string
	KeySecret  string
	WebhookTTL time.Duration
}

type FCMConfig struct {
	BaseURL           string
	ProjectID         string
	ServiceAccountKey string
}

type GoogleConfig struct {
	APIKey string
}

type WSConfig struct {
	ReadBuffer     int
	WriteBuffer    int
	PingInterval   time.Duration
	PongTimeout    time.Duration
	WriteTimeout   time.Duration
	SendBuffer     int
	AllowedOrigins []string
}

type HTTPConfig struct {
	Timeout time.Duration
}

type RateLimitConfig struct {
	GlobalLimit  int
	GlobalWindow time.Duration
	OTPLimit     int
	OTPWindow    time.Duration
}

type PricingConfig struct {
	BaseDriverFeePerHour int
	PlatformFee          int
	ProtectionFee        int
}

type MatchingConfig struct {
	MaxSearchRadiusKM   int
	DriverAcceptTimeout int
	MaxDriversToNotify  int
}

type LogConfig struct {
	Level  string
	Format string
}

type SMSConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	SenderID string
}

type WorkerConfig struct {
	Enabled               bool
	ScheduledPollInterval time.Duration
	OfferExpiryInterval   time.Duration
	StaleSearchInterval   time.Duration
	StaleSearchThreshold  time.Duration
	// SearchRetryMinAge is how long a booking must have been searching before the
	// short-cadence retry will re-drive it, so the inline attempt at creation
	// time is not duplicated.
	SearchRetryMinAge time.Duration
	// NoShowSweepInterval is how often arrivals that never became a trip are
	// checked.
	NoShowSweepInterval time.Duration
	// NoShowThreshold is how long a driver may sit in DRIVER_ARRIVED without
	// starting the trip before the booking is treated as a no-show.
	NoShowThreshold time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		Environment: getEnv("APP_ENV", getEnv("SERVER_ENV", "development")),
		Server: ServerConfig{
			Port:            getEnv("SERVER_PORT", "8080"),
			Env:             getEnv("SERVER_ENV", "development"),
			ReadTimeout:     seconds("SERVER_READ_TIMEOUT", 30),
			WriteTimeout:    seconds("SERVER_WRITE_TIMEOUT", 30),
			IdleTimeout:     seconds("SERVER_IDLE_TIMEOUT", 120),
			ShutdownTimeout: seconds("SERVER_SHUTDOWN_TIMEOUT", 15),
			RequestTimeout:  seconds("SERVER_REQUEST_TIMEOUT", 60),
			AllowedOrigins:  csv("SERVER_ALLOWED_ORIGINS", []string{"*"}),
			TrustedProxies:  csv("SERVER_TRUSTED_PROXIES", nil),
		},
		Database: DatabaseConfig{
			URL:             os.Getenv("DATABASE_URL"),
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnvAsInt("DB_PORT", 5432),
			User:            getEnv("DB_USER", "drivethrough"),
			Password:        getEnv("DB_PASSWORD", "drivethrough_secret"),
			DBName:          getEnv("DB_NAME", "drivethrough"),
			SSLMode:         getEnv("DB_SSLMODE", "disable"),
			MaxOpenConns:    getEnvAsInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getEnvAsInt("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: duration("DB_CONN_MAX_LIFETIME", 5*time.Minute),
			ConnectTimeout:  seconds("DB_CONNECT_TIMEOUT", 10),
			RunMigrations:   getEnvAsBool("DB_RUN_MIGRATIONS", true),
		},
		Redis: RedisConfig{
			URL:            os.Getenv("REDIS_URL"),
			Host:           getEnv("REDIS_HOST", "localhost"),
			Port:           getEnv("REDIS_PORT", "6379"),
			Password:       getEnv("REDIS_PASSWORD", ""),
			DB:             getEnvAsInt("REDIS_DB", 0),
			UseTLS:         getEnvAsBool("REDIS_TLS", false),
			TLSSkipVerify:  getEnvAsBool("REDIS_TLS_SKIP_VERIFY", false),
			ConnectTimeout: seconds("REDIS_CONNECT_TIMEOUT", 5),
		},
		JWT: JWTConfig{
			Secret:        getEnv("JWT_SECRET", ""),
			AccessExpiry:  minutesEnv("JWT_ACCESS_EXPIRY", 15*time.Minute),
			RefreshExpiry: minutesEnv("JWT_REFRESH_EXPIRY", 7*24*60*time.Minute),
			Issuer:        getEnv("JWT_ISSUER", "drivethrough"),
		},
		OTP: OTPConfig{
			Expiry: minutesEnv("OTP_EXPIRY", 5*time.Minute),
			Length: getEnvAsInt("OTP_LENGTH", 6),
		},
		Razorpay: RazorpayConfig{
			BaseURL:    getEnv("RAZORPAY_BASE_URL", "https://api.razorpay.com/v1"),
			KeyID:      getEnv("RAZORPAY_KEY_ID", ""),
			KeySecret:  getEnv("RAZORPAY_KEY_SECRET", ""),
			WebhookTTL: seconds("RAZORPAY_WEBHOOK_TTL", 300),
		},
		FCM: FCMConfig{
			BaseURL:           getEnv("FCM_BASE_URL", "https://fcm.googleapis.com"),
			ProjectID:         getEnv("FCM_PROJECT_ID", ""),
			ServiceAccountKey: getEnv("FCM_SERVICE_ACCOUNT_KEY", ""),
		},
		Google: GoogleConfig{
			APIKey: getEnv("GOOGLE_MAPS_API_KEY", ""),
		},
		WS: WSConfig{
			ReadBuffer:     getEnvAsInt("WS_READ_BUFFER", 4096),
			WriteBuffer:    getEnvAsInt("WS_WRITE_BUFFER", 4096),
			SendBuffer:     getEnvAsInt("WS_SEND_BUFFER", 256),
			PingInterval:   seconds("WS_PING_INTERVAL", 30),
			PongTimeout:    seconds("WS_PONG_TIMEOUT", 90),
			WriteTimeout:   seconds("WS_WRITE_TIMEOUT", 10),
			AllowedOrigins: csv("WS_ALLOWED_ORIGINS", nil),
		},
		HTTP: HTTPConfig{
			Timeout: seconds("HTTP_CLIENT_TIMEOUT", 15),
		},
		RateLimit: RateLimitConfig{
			GlobalLimit:  getEnvAsInt("RATE_LIMIT_GLOBAL", 120),
			GlobalWindow: seconds("RATE_LIMIT_GLOBAL_WINDOW", 60),
			OTPLimit:     getEnvAsInt("RATE_LIMIT_OTP", 5),
			OTPWindow:    seconds("RATE_LIMIT_OTP_WINDOW", 300),
		},
		Pricing: PricingConfig{
			BaseDriverFeePerHour: getEnvAsInt("BASE_DRIVER_FEE_PER_HOUR", 150),
			PlatformFee:          getEnvAsInt("PLATFORM_FEE", 50),
			ProtectionFee:        getEnvAsInt("PROTECTION_FEE", 20),
		},
		Matching: MatchingConfig{
			MaxSearchRadiusKM:   getEnvAsInt("MAX_SEARCH_RADIUS_KM", 5),
			DriverAcceptTimeout: getEnvAsInt("DRIVER_ACCEPT_TIMEOUT_SEC", 10),
			MaxDriversToNotify:  getEnvAsInt("MAX_DRIVERS_TO_NOTIFY", 5),
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "text"),
		},
		SMS: SMSConfig{
			Provider: getEnv("SMS_PROVIDER", "log"),
			BaseURL:  getEnv("SMS_BASE_URL", "https://control.msg91.com/api/v5"),
			APIKey:   getEnv("SMS_API_KEY", ""),
			SenderID: getEnv("SMS_SENDER_ID", "DTTHRU"),
		},
		Workers: WorkerConfig{
			Enabled:               getEnvAsBool("WORKERS_ENABLED", true),
			ScheduledPollInterval: seconds("WORKER_SCHEDULED_POLL_INTERVAL", 30),
			OfferExpiryInterval:   seconds("WORKER_OFFER_EXPIRY_INTERVAL", 15),
			StaleSearchInterval:   duration("WORKER_STALE_SEARCH_INTERVAL", 2*time.Minute),
			StaleSearchThreshold:  duration("WORKER_STALE_SEARCH_THRESHOLD", 10*time.Minute),
			SearchRetryMinAge:     duration("WORKER_SEARCH_RETRY_MIN_AGE", 20*time.Second),
			NoShowSweepInterval:   duration("WORKER_NO_SHOW_SWEEP_INTERVAL", 60*time.Second),
			NoShowThreshold:       duration("WORKER_NO_SHOW_THRESHOLD", 10*time.Minute),
		},
	}

	return cfg, cfg.Validate()
}

// DSN returns the connection string, preferring DATABASE_URL so the same binary
// can target a managed Postgres, a local socket, or a container.
func (d DatabaseConfig) DSN() string {
	if d.URL != "" {
		return d.URL
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(d.User), url.QueryEscape(d.Password), d.Host, d.Port, d.DBName, d.SSLMode)
}

func (r RedisConfig) Address() string {
	if r.URL != "" {
		if u, err := url.Parse(r.URL); err == nil && u.Host != "" {
			return u.Host
		}
		return r.URL
	}
	return fmt.Sprintf("%s:%s", r.Host, r.Port)
}

func (c Config) Validate() error {
	if c.JWT.Secret == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	if c.Server.IsProduction() {
		switch c.JWT.Secret {
		case "change-this-secret", "dev-secret-change-me", "secret", "your-super-secret-jwt-key-change-this":
			return fmt.Errorf("JWT_SECRET must be changed from its default value in production")
		}
		if len(c.JWT.Secret) < 32 {
			return fmt.Errorf("JWT_SECRET must be at least 32 characters in production")
		}
		if c.Database.SSLMode == "disable" {
			return fmt.Errorf("DB_SSLMODE=disable is not permitted in production")
		}
		if c.Server.AllowedOrigins[0] == "*" {
			return fmt.Errorf("SERVER_ALLOWED_ORIGINS must not be '*' in production")
		}
		if !c.Database.RunMigrations {
			slog.Warn("DB_RUN_MIGRATIONS is disabled; schema changes must be applied out of band")
		}
	}
	if c.OTP.Length < 4 || c.OTP.Length > 10 {
		return fmt.Errorf("OTP_LENGTH must be between 4 and 10, got %d", c.OTP.Length)
	}
	if c.Pricing.BaseDriverFeePerHour <= 0 {
		return fmt.Errorf("BASE_DRIVER_FEE_PER_HOUR must be positive")
	}
	if c.Matching.MaxSearchRadiusKM <= 0 {
		return fmt.Errorf("MAX_SEARCH_RADIUS_KM must be positive")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intVal, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getEnvAsBool(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if boolVal, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return boolVal
		}
	}
	return defaultValue
}

func seconds(key string, defaultValue time.Duration) time.Duration {
	return duration(key, defaultValue)
}

// minutesEnv accepts either a Go duration ("15m") or a bare integer count of
// minutes ("15"), so existing deployments keep their numeric values working.
func minutesEnv(key string, defaultValue time.Duration) time.Duration {
	raw, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	raw = strings.TrimSpace(raw)
	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed
	}
	if count, err := strconv.Atoi(raw); err == nil {
		return time.Duration(count) * time.Minute
	}
	return defaultValue
}

func duration(key string, defaultValue time.Duration) time.Duration {
	raw, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	raw = strings.TrimSpace(raw)
	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed
	}
	if count, err := strconv.Atoi(raw); err == nil {
		return time.Duration(count) * time.Second
	}
	return defaultValue
}

func csv(key string, defaultValue []string) []string {
	raw, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return defaultValue
	}
	return out
}
