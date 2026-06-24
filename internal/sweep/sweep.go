package sweep

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0x4A4FRN/fine/internal/replies"
)

const sweepInterval = 60 * time.Second

type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type MessageEditor interface {
	ChannelMessageEdit(channelID, messageID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

type expiredWindow struct {
	ID           int64
	ChannelID    string
	BotMessageID string
	Payload      string
}

const selectExpiredSQL = `
SELECT id, channel_id, bot_message_id, payload
FROM suggestion_windows
WHERE status = 'open' AND expires_at < NOW()`

const updateExpiredSQL = `UPDATE suggestion_windows SET status = 'expired' WHERE id = $1 AND status = 'open'`

const fallbackExpiredText = "~~Confirm...~~ Expired."

func Start(
	ctx context.Context,
	db DB,
	editor MessageEditor,
	r replies.Renderer,
	logger *zap.Logger,
) {
	if logger == nil {
		logger = zap.NewNop()
	}
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSweep(ctx, db, editor, r, logger)
		}
	}
}

func runSweep(
	ctx context.Context,
	db DB,
	editor MessageEditor,
	r replies.Renderer,
	logger *zap.Logger,
) {
	if logger == nil {
		logger = zap.NewNop()
	}
	rows, err := db.Query(ctx, selectExpiredSQL)
	if err != nil {
		logger.Error("sweep: querying expired windows", zap.Error(err))
		return
	}
	defer rows.Close()

	var windows []expiredWindow
	for rows.Next() {
		var w expiredWindow
		if err := rows.Scan(&w.ID, &w.ChannelID, &w.BotMessageID, &w.Payload); err != nil {
			logger.Error("sweep: scanning expired window", zap.Error(err))
			continue
		}
		windows = append(windows, w)
	}

	if err := rows.Err(); err != nil {
		logger.Error("sweep: iterating expired windows", zap.Error(err))
		return
	}

	var failedCount int
	var editedCount int
	for _, w := range windows {
		commandTag, err := db.Exec(ctx, updateExpiredSQL, w.ID)
		if err != nil {
			failedCount++
			logger.Error("sweep: updating window status",
				zap.Int64("window_id", w.ID),
				zap.Error(err),
			)
			continue
		}
		if commandTag.RowsAffected() == 0 {
			logger.Debug("sweep: window already resolved, skipping",
				zap.Int64("window_id", w.ID),
			)
			continue
		}

		expiredText := buildExpiredText(r, w.Payload)
		if editor != nil {
			_, editErr := editor.ChannelMessageEdit(w.ChannelID, w.BotMessageID, expiredText)
			if editErr != nil {
				logger.Warn("sweep: editing message failed (continuing)",
					zap.String("message_id", w.BotMessageID),
					zap.Error(editErr),
				)
				continue
			}
			editedCount++
		}
	}

	if len(windows) > 0 {
		if failedCount > 0 {
			// Warn-level summary so operators can alert on sweep failure
			// rates. A 100% failure rate over multiple cycles indicates a
			// sustained DB connection issue that would otherwise be visible
			// only by aggregating per-window ERROR lines.
			logger.Warn("sweep: expired windows processed with failures",
				zap.Int("total", len(windows)),
				zap.Int("succeeded", len(windows)-failedCount),
				zap.Int("failed", failedCount),
				zap.Int("edited", editedCount),
			)
		} else {
			logger.Info("sweep: expired windows processed",
				zap.Int("total", len(windows)),
				zap.Int("edited", editedCount),
			)
		}
	} else {
		logger.Debug("sweep: pass complete, nothing to expire")
	}
}

func buildExpiredText(r replies.Renderer, payload string) string {
	original := extractOriginalConfirmText(payload)
	if r == nil || original == "" {
		return fallbackExpiredText
	}
	return r.Get("confirmation", "expired", map[string]string{
		"original": original,
	})
}

func extractOriginalConfirmText(payload string) string {
	if payload == "" {
		return ""
	}
	var wp struct {
		OriginalConfirmText string `json:"original_confirm_text"`
	}
	if err := json.Unmarshal([]byte(payload), &wp); err != nil {
		return ""
	}
	return wp.OriginalConfirmText
}

func SweepOnce(
	ctx context.Context,
	db DB,
	editor MessageEditor,
	r replies.Renderer,
	logger *zap.Logger,
) {
	if logger == nil {
		logger = zap.NewNop()
	}
	runSweep(ctx, db, editor, r, logger)
}
