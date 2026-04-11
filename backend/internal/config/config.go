package config

import (
	"os"
	"time"
)

type Config struct {
	ServerPort    string
	DatabasePath  string
	JWTSecret     string
	JWTExpiration time.Duration
	SyncInterval  time.Duration
	OfflineMaxHours int
}

func Load() *Config {
	return &Config{
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		DatabasePath:  getEnv("DATABASE_PATH", "./data/gbos.db"),
		JWTSecret:     getEnv("JWT_SECRET", "gbos-secret-key-change-in-production"),
		JWTExpiration: 24 * time.Hour,
		SyncInterval:  5 * time.Minute,
		OfflineMaxHours: 8,
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}