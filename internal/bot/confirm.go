package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0x4A4FRN/fine/internal/llm"
)

var confirmAffirmatives = []string{
	"y", "yes", "ok", "go", "confirm", "sure", "yeah",
	"do it", "proceed", "yep", "yup", "affirmative",
}

var confirmNegatives = []string{
	"n", "no", "cancel", "stop", "nope", "abort",
	"don't", "dont", "never", "no way",
}

func MatchConfirmation(text string) (action string, matched bool) {
	text = strings.ToLower(strings.TrimSpace(text))
	text = strings.TrimRight(text, "!.?,;")

	for _, w := range confirmAffirmatives {
		if text == w {
			return "yes", true
		}
	}
	for _, w := range confirmNegatives {
		if text == w {
			return "no", true
		}
	}
	return "", false
}

var destructiveIntents = map[string]bool{
	"ban":            true,
	"unban":          true,
	"kick":           true,
	"timeout":        true,
	"untimeout":      true,
	"mute":           true,
	"unmute":         true,
	"deafen":         true,
	"undeafen":       true,
	"add_role":       true,
	"remove_role":    true,
	"purge_messages": true,
}

func IsDestructive(intent string) bool {
	return destructiveIntents[intent]
}

type SuggestionWindow struct {
	ID           int64
	GuildID      string
	ChannelID    string
	UserID       string
	Status       string
	BotMessageID string
	Payload      string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type DBConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type WindowDB interface {
	DBConn
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

const insertWindowSQL = `
INSERT INTO suggestion_windows
    (guild_id, channel_id, user_id, status, bot_message_id, payload, expires_at)
VALUES ($1, $2, $3, 'open', $4, $5, $6)
RETURNING id`

func CreateWindow(
	ctx context.Context,
	db DBConn,
	guildID, channelID, userID, botMessageID, payload string,
	expiresAt time.Time,
) (int64, error) {
	var id int64
	err := db.QueryRow(
		ctx,
		insertWindowSQL,
		guildID,
		channelID,
		userID,
		botMessageID,
		payload,
		expiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("confirm: creating window: %w", err)
	}
	return id, nil
}

const selectOpenWindowSQL = `
SELECT id, guild_id, channel_id, user_id, status, bot_message_id,
       payload, expires_at, created_at
FROM suggestion_windows
WHERE channel_id = $1 AND user_id = $2
  AND status = 'open' AND expires_at > NOW()
ORDER BY created_at DESC
LIMIT 1
FOR UPDATE`

func GetOpenWindow(
	ctx context.Context,
	db WindowDB,
	channelID, userID string,
) (*SuggestionWindow, pgx.Tx, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: beginning tx: %w", err)
	}

	var w SuggestionWindow
	err = tx.QueryRow(ctx, selectOpenWindowSQL, channelID, userID).Scan(
		&w.ID,
		&w.GuildID,
		&w.ChannelID,
		&w.UserID,
		&w.Status,
		&w.BotMessageID,
		&w.Payload,
		&w.ExpiresAt,
		&w.CreatedAt,
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("confirm: querying open window: %w", err)
	}

	return &w, tx, nil
}

const selectWindowByIDSQL = `
SELECT id, guild_id, channel_id, user_id, status, bot_message_id,
       payload, expires_at, created_at
FROM suggestion_windows
WHERE id = $1
FOR UPDATE`

func GetWindowByID(
	ctx context.Context,
	db WindowDB,
	windowID int64,
) (*SuggestionWindow, pgx.Tx, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: beginning tx: %w", err)
	}

	var w SuggestionWindow
	err = tx.QueryRow(ctx, selectWindowByIDSQL, windowID).Scan(
		&w.ID,
		&w.GuildID,
		&w.ChannelID,
		&w.UserID,
		&w.Status,
		&w.BotMessageID,
		&w.Payload,
		&w.ExpiresAt,
		&w.CreatedAt,
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("confirm: querying window by id: %w", err)
	}

	return &w, tx, nil
}

const updateWindowStatusSQL = `UPDATE suggestion_windows SET status = $1 WHERE id = $2`

func UpdateStatus(ctx context.Context, db DBConn, id int64, status string) error {
	_, err := db.Exec(ctx, updateWindowStatusSQL, status, id)
	if err != nil {
		return fmt.Errorf("confirm: updating window status: %w", err)
	}
	return nil
}

type WindowPayload struct {
	Response            *llm.LLMResponse `json:"response"`
	SourceMessageID     string           `json:"source_message_id"`
	OriginalConfirmText string           `json:"original_confirm_text,omitempty"`
}
