package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const assistantTruncateLimit = 500

type Message struct {
	Role    string
	Content string
}

type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type Store struct {
	db           DB
	window       time.Duration
	historyLimit int
}

func NewStore(db DB, window time.Duration, historyLimit int) *Store {
	return &Store{db: db, window: window, historyLimit: historyLimit}
}

// Parameterized SQL constants. The window duration is passed as a parameter
// ($4 / $5) rather than interpolated via fmt.Sprintf to follow the
// project's parameterized-query convention and avoid SQL injection patterns.

const selectConversationSQL = `
SELECT id FROM conversations
WHERE guild_id = $1 AND channel_id = $2 AND user_id = $3
  AND last_active_at > NOW() - ($4 || ' minutes')::INTERVAL
ORDER BY last_active_at DESC
LIMIT 1`

const insertConversationSQL = `
INSERT INTO conversations (guild_id, channel_id, user_id)
VALUES ($1, $2, $3)
RETURNING id`

const insertMessageSQL = `
INSERT INTO conversation_messages (conversation_id, role, content, discord_msg_id)
VALUES ($1, $2, $3, $4)`

const updateLastActiveSQL = `UPDATE conversations SET last_active_at = NOW() WHERE id = $1`

const selectHistorySQL = `
SELECT cm.role, cm.content
FROM conversation_messages cm
JOIN conversations c ON c.id = cm.conversation_id
WHERE c.guild_id = $1 AND c.channel_id = $2 AND c.user_id = $3
  AND c.last_active_at > NOW() - ($4 || ' minutes')::INTERVAL
ORDER BY cm.id DESC
LIMIT $5`

func (s *Store) WriteMessage(
	ctx context.Context,
	guildID, channelID, userID, role, content, discordMsgID string,
) error {
	if role != "user" && role != "assistant" {
		return fmt.Errorf("conversation: invalid role %q", role)
	}

	if role == "assistant" && len(content) > assistantTruncateLimit {
		content = content[:assistantTruncateLimit] + "…"
	}

	minutes := int(s.window.Minutes())
	var convID int64
	err := s.db.QueryRow(
		ctx,
		selectConversationSQL,
		guildID,
		channelID,
		userID,
		minutes,
	).Scan(&convID)

	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("conversation: querying conversation: %w", err)
		}
		err = s.db.QueryRow(
			ctx,
			insertConversationSQL,
			guildID,
			channelID,
			userID,
		).Scan(&convID)
		if err != nil {
			return fmt.Errorf("conversation: inserting conversation: %w", err)
		}
	}

	if _, err := s.db.Exec(
		ctx,
		insertMessageSQL,
		convID,
		role,
		content,
		discordMsgID,
	); err != nil {
		return fmt.Errorf("conversation: inserting message: %w", err)
	}

	if _, err := s.db.Exec(ctx, updateLastActiveSQL, convID); err != nil {
		return fmt.Errorf("conversation: updating last_active_at: %w", err)
	}

	return nil
}

func (s *Store) GetHistory(
	ctx context.Context,
	guildID, channelID, userID string,
) ([]Message, error) {
	minutes := int(s.window.Minutes())
	rows, err := s.db.Query(
		ctx,
		selectHistorySQL,
		guildID,
		channelID,
		userID,
		minutes,
		s.historyLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("conversation: querying history: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.Role, &msg.Content); err != nil {
			return nil, fmt.Errorf("conversation: scanning history row: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conversation: iterating history rows: %w", err)
	}

	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if messages == nil {
		messages = []Message{}
	}

	return messages, nil
}

const countDistinctUsersSQL = `
SELECT COUNT(DISTINCT user_id) FROM conversations`

// CountDistinctUsers returns the number of unique users who have ever opened a
// conversation with the bot. Used by the info command; the count is
// approximate in the sense that bot-only-initiated or short-window dismissals
// are still counted, but it's a useful ballpark for active-user telemetry.
func (s *Store) CountDistinctUsers(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var n int
	if err := s.db.QueryRow(ctx, countDistinctUsersSQL).Scan(&n); err != nil {
		return 0, fmt.Errorf("conversation: counting distinct users: %w", err)
	}
	return n, nil
}
