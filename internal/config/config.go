package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken    string
	DatabaseURL string

	LogDir   string
	LogLevel string

	LogStreamSecret string

	LLMBaseURL        string
	LLMAPIKeys        []string
	LLMKeyRotateEvery int
	LLMModel          string
	LLMTimeout        time.Duration
	LLMMaxRetries     int

	ConversationWindowMinutes      int
	ConversationRetentionDays      int
	ConversationHistoryMaxMessages int

	ConfirmWindowSeconds int

	CacheHitConfidenceMin float64
	RepliesPath           string

	AuditRetentionDays int

	S3Endpoint  string
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string

	SnipeRetentionDays int
}

func Load() (*Config, error) {
	cfg := &Config{
		BotToken:    os.Getenv("DISCORD_BOT_TOKEN"),
		DatabaseURL: os.Getenv("DATABASE_URL"),

		LogDir:   os.Getenv("LOG_DIR"),
		LogLevel: envOr("LOG_LEVEL", "info"),

		LogStreamSecret: os.Getenv("LOG_STREAM_SECRET"),

		LLMBaseURL: os.Getenv("LLM_BASE_URL"),
		LLMAPIKeys: parseAPIKeys(os.Getenv("LLM_API_KEY")),
		LLMModel:   envOr("LLM_MODEL", "gpt-4o-mini"),

		ConversationWindowMinutes:      envIntOr("CONVERSATION_WINDOW_MINUTES", 30),
		ConversationRetentionDays:      envIntOr("CONVERSATION_RETENTION_DAYS", 30),
		ConversationHistoryMaxMessages: envIntOr("CONVERSATION_HISTORY_MAX_MESSAGES", 10),

		ConfirmWindowSeconds: envIntOr("CONFIRM_WINDOW_SECONDS", 60),

		CacheHitConfidenceMin: envFloatOr("CACHE_HIT_CONFIDENCE_MIN", 0.7),
		RepliesPath:           envOr("REPLIES_PATH", "internal/replies/replies.yaml"),

		AuditRetentionDays: envIntOr("AUDIT_RETENTION_DAYS", 90),

		S3Endpoint:  os.Getenv("S3_ENDPOINT"),
		S3Bucket:    os.Getenv("S3_BUCKET"),
		S3Region:    envOr("S3_REGION", "us-east-1"),
		S3AccessKey: os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("S3_SECRET_KEY"),

		SnipeRetentionDays: envIntOr("SNIPE_RETENTION_DAYS", 7),
	}

	timeoutMs := envIntOr("LLM_TIMEOUT_MS", 15000)
	cfg.LLMTimeout = time.Duration(timeoutMs) * time.Millisecond

	cfg.LLMMaxRetries = envIntOr("LLM_MAX_RETRIES", 2)

	cfg.LLMKeyRotateEvery = envIntOr("LLM_KEY_ROTATE_EVERY", 25)

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("config: DISCORD_BOT_TOKEN is required")
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}

	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloatOr(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func parseAPIKeys(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		k := strings.TrimSpace(p)
		if k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}
