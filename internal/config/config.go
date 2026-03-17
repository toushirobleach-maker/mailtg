package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

const (
	defaultPollInterval = 15 * time.Second
	defaultDBPath       = "mailtg.db"
	defaultIMAPHost     = "imap.yandex.ru"
	defaultIMAPPort     = 993
)

type Config struct {
	BotToken     string
	PollInterval time.Duration
	DBPath       string
	IMAPHost     string
	IMAPPort     int
	IMAPUsername string
	IMAPPassword string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		BotToken:     os.Getenv("BOT_TOKEN"),
		PollInterval: defaultPollInterval,
		DBPath:       getEnv("DB_PATH", defaultDBPath),
		IMAPHost:     getEnv("IMAP_HOST", defaultIMAPHost),
		IMAPPort:     defaultIMAPPort,
		IMAPUsername: os.Getenv("IMAP_USERNAME"),
		IMAPPassword: os.Getenv("IMAP_PASSWORD"),
	}

	if raw := os.Getenv("POLL_INTERVAL_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("invalid POLL_INTERVAL_SECONDS: %q", raw)
		}
		cfg.PollInterval = time.Duration(seconds) * time.Second
	}
	if raw := os.Getenv("IMAP_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port <= 0 {
			return nil, fmt.Errorf("invalid IMAP_PORT: %q", raw)
		}
		cfg.IMAPPort = port
	}

	switch {
	case cfg.BotToken == "":
		return nil, errors.New("BOT_TOKEN is required")
	case cfg.IMAPUsername == "":
		return nil, errors.New("IMAP_USERNAME is required")
	case cfg.IMAPPassword == "":
		return nil, errors.New("IMAP_PASSWORD is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
