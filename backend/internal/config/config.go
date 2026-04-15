package config

import (
	"os"
	"time"
)

const defaultJWTSecret = "gbos-secret-key-change-in-production"

type Config struct {
	ServerPort      string
	DatabasePath    string
	JWTSecret       string
	JWTExpiration   time.Duration
	SyncInterval    time.Duration
	OfflineMaxHours int
	GinMode         string
	CORSOrigin      string
	LogLevel        string
}

func Load() *Config {
	return &Config{
		ServerPort:      getEnv("SERVER_PORT", "8080"),
		DatabasePath:    getEnv("DATABASE_PATH", "./data/gbos.db"),
		JWTSecret:       getEnv("JWT_SECRET", defaultJWTSecret),
		JWTExpiration:   24 * time.Hour,
		SyncInterval:    5 * time.Minute,
		OfflineMaxHours: 8,
		GinMode:         getEnv("GIN_MODE", "debug"),
		CORSOrigin:      getEnv("CORS_ORIGIN", "*"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
	}
}

func (c *Config) IsDefaultSecret() bool {
	return c.JWTSecret == defaultJWTSecret
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
