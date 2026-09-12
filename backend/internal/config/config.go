package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// devJWTSecret is used only when GIN_MODE is debug or test and no secret is
// supplied, so that a plain `go run` works without any environment setup.
const devJWTSecret = "gbos-local-development-only-not-for-deployment"

const minJWTSecretLength = 32

// placeholderSecrets are values that have appeared in this repo's deployment
// files or are common filler. Rejecting the whole set means a typo in one
// config file cannot quietly re-enable a publicly known signing key.
var placeholderSecrets = map[string]bool{
	"gbos-secret-key-change-in-production":           true,
	"default-change-in-production":                   true,
	"change-me-in-production":                        true,
	"gbos-local-development-only-not-for-deployment": true,
	"changeme":    true,
	"change-me":   true,
	"secret":      true,
	"test-secret": true,
	"password":    true,
}

// devDatabaseURL points at the Postgres that `make db-up` starts, so a plain
// `go run ./cmd/server` works with no environment setup. It is only ever used
// in debug mode.
const devDatabaseURL = "postgres://gbos:gbos_dev_password@localhost:5434/gbos_dev?sslmode=disable"

type Config struct {
	ServerPort string
	// DatabaseURL is the PostgreSQL connection string.
	DatabaseURL string
	// DatabasePath is the SQLite file the pre-migration server still uses.
	// It goes away with that server at the cutover.
	DatabasePath    string
	JWTSecret       string
	JWTExpiration   time.Duration
	SyncInterval    time.Duration
	OfflineMaxHours int
	GinMode         string
	CORSOrigin      string
	LogLevel        string
	TrustedProxies  []string
}

// Load reads configuration from the environment and refuses to return a
// configuration that would be unsafe to serve with.
func Load() (*Config, error) {
	cfg := &Config{
		ServerPort:      getEnv("SERVER_PORT", "8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		DatabasePath:    getEnv("DATABASE_PATH", "./data/gbos.db"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		JWTExpiration:   24 * time.Hour,
		SyncInterval:    5 * time.Minute,
		OfflineMaxHours: 8,
		GinMode:         getEnv("GIN_MODE", "debug"),
		CORSOrigin:      getEnv("CORS_ORIGIN", "http://localhost:3000"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		TrustedProxies:  splitList(os.Getenv("TRUSTED_PROXIES")),
	}
	if err := cfg.resolve(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) isDevMode() bool {
	return c.GinMode == "debug" || c.GinMode == "test"
}

func (c *Config) resolve() error {
	if c.isDevMode() {
		if c.JWTSecret == "" {
			c.JWTSecret = devJWTSecret
		}
		if c.DatabaseURL == "" {
			c.DatabaseURL = devDatabaseURL
		}
		return nil
	}

	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required when GIN_MODE=%s", c.GinMode)
	}
	if c.JWTSecret == "" {
		return fmt.Errorf("JWT_SECRET is required when GIN_MODE=%s; generate one with: openssl rand -base64 48", c.GinMode)
	}
	if placeholderSecrets[strings.ToLower(strings.TrimSpace(c.JWTSecret))] {
		return fmt.Errorf("JWT_SECRET is a known placeholder value; generate a real one with: openssl rand -base64 48")
	}
	if len(c.JWTSecret) < minJWTSecretLength {
		return fmt.Errorf("JWT_SECRET must be at least %d characters, got %d", minJWTSecretLength, len(c.JWTSecret))
	}
	if c.CORSOrigin == "*" {
		return fmt.Errorf("CORS_ORIGIN must name an explicit origin when GIN_MODE=%s, not \"*\"", c.GinMode)
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
