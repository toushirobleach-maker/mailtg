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
	defaultQuery        = "is:unread in:inbox"
)

type Config struct {
	BotToken             string
	GmailCredentialsPath string
	PollInterval         time.Duration
	DBPath               string
	GmailQuery           string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		BotToken:             os.Getenv("BOT_TOKEN"),
		GmailCredentialsPath: os.Getenv("GMAIL_CREDENTIALS_PATH"),
		PollInterval:         defaultPollInterval,
		DBPath:               getEnv("DB_PATH", defaultDBPath),
		GmailQuery:           getEnv("GMAIL_QUERY", defaultQuery),
	}

	if raw := os.Getenv("POLL_INTERVAL_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("invalid POLL_INTERVAL_SECONDS: %q", raw)
		}
		cfg.PollInterval = time.Duration(seconds) * time.Second
	}

	switch {
	case cfg.BotToken == "":
		return nil, errors.New("BOT_TOKEN is required")
	case cfg.GmailCredentialsPath == "":
		return nil, errors.New("GMAIL_CREDENTIALS_PATH is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
