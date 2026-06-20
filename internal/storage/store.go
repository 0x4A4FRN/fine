package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the narrow interface the Store needs from the connection pool.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// AttachmentMetadata is the per-attachment record stored as JSON in the
// message_snapshots.attachments column.
type AttachmentMetadata struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	S3Key       string `json:"s3_key,omitempty"`
}

// Snapshot represents a row in the message_snapshots table.
type Snapshot struct {
	ID          int64
	GuildID     string
	ChannelID   string
	MessageID   string
	AuthorID    string
	AuthorName  string
	AuthorBot   bool
	Content     string
	Attachments []AttachmentMetadata
	MessageTS   time.Time
	DeletedAt   *time.Time
	CreatedAt   time.Time
}

// Store provides DB operations for message snapshots: insert, mark-deleted,
// query deleted messages, and retention sweep.
type Store struct {
	db DB
}

func NewStore(db DB) *Store {
	return &Store{db: db}
}

const insertSnapshotSQL = `
INSERT INTO message_snapshots
    (guild_id, channel_id, message_id, author_id, author_name, author_bot, content, attachments, embeds, message_ts)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING`

// InsertSnapshot stores a message snapshot. Attachments are serialised as
// JSON. If the message was already snapshot (e.g. duplicate event), the
// insert is silently skipped via ON CONFLICT DO NOTHING (no unique
// constraint exists today, but the guard is harmless).
func (s *Store) InsertSnapshot(ctx context.Context, snap Snapshot) error {
	var attachJSON any
	if len(snap.Attachments) > 0 {
		b, err := json.Marshal(snap.Attachments)
		if err != nil {
			return fmt.Errorf("storage: marshalling attachments: %w", err)
		}
		attachJSON = b
	} else {
		attachJSON = nil
	}

	_, err := s.db.Exec(ctx, insertSnapshotSQL,
		snap.GuildID,
		snap.ChannelID,
		snap.MessageID,
		snap.AuthorID,
		snap.AuthorName,
		snap.AuthorBot,
		snap.Content,
		attachJSON,
		nil, // embeds — reserved for future use
		snap.MessageTS,
	)
	if err != nil {
		return fmt.Errorf("storage: inserting snapshot: %w", err)
	}
	return nil
}

const markDeletedSQL = `UPDATE message_snapshots SET deleted_at = NOW() WHERE message_id = $1 AND deleted_at IS NULL`

// MarkDeleted records that a single message has been deleted.
func (s *Store) MarkDeleted(ctx context.Context, messageID string) error {
	_, err := s.db.Exec(ctx, markDeletedSQL, messageID)
	if err != nil {
		return fmt.Errorf("storage: marking deleted: %w", err)
	}
	return nil
}

const markBulkDeletedSQL = `UPDATE message_snapshots SET deleted_at = NOW() WHERE message_id = ANY($1) AND deleted_at IS NULL`

// MarkBulkDeleted records that multiple messages have been deleted.
func (s *Store) MarkBulkDeleted(ctx context.Context, messageIDs []string) error {
	_, err := s.db.Exec(ctx, markBulkDeletedSQL, messageIDs)
	if err != nil {
		return fmt.Errorf("storage: bulk marking deleted: %w", err)
	}
	return nil
}

const queryDeletedSQL = `
SELECT id, guild_id, channel_id, message_id, author_id, author_name, author_bot,
       content, attachments, message_ts, deleted_at, created_at
FROM message_snapshots
WHERE channel_id = $1 AND deleted_at IS NOT NULL
ORDER BY message_ts DESC
LIMIT $2`

// QueryDeleted returns the N most recently deleted messages in a channel.
func (s *Store) QueryDeleted(ctx context.Context, channelID string, limit int) ([]Snapshot, error) {
	rows, err := s.db.Query(ctx, queryDeletedSQL, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: querying deleted: %w", err)
	}
	defer rows.Close()

	var snaps []Snapshot
	for rows.Next() {
		var snap Snapshot
		var attachJSON []byte
		if err := rows.Scan(
			&snap.ID,
			&snap.GuildID,
			&snap.ChannelID,
			&snap.MessageID,
			&snap.AuthorID,
			&snap.AuthorName,
			&snap.AuthorBot,
			&snap.Content,
			&attachJSON,
			&snap.MessageTS,
			&snap.DeletedAt,
			&snap.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("storage: scanning deleted snapshot: %w", err)
		}
		if len(attachJSON) > 0 {
			if err := json.Unmarshal(attachJSON, &snap.Attachments); err != nil {
				return nil, fmt.Errorf("storage: unmarshalling attachments: %w", err)
			}
		}
		snaps = append(snaps, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterating deleted snapshots: %w", err)
	}
	return snaps, nil
}

const sweepRetentionSQL = `DELETE FROM message_snapshots WHERE created_at < NOW() - ($1 || ' days')::INTERVAL`

// SweepRetention deletes snapshot rows older than the given number of days.
func (s *Store) SweepRetention(ctx context.Context, days int) (int64, error) {
	tag, err := s.db.Exec(ctx, sweepRetentionSQL, days)
	if err != nil {
		return 0, fmt.Errorf("storage: retention sweep: %w", err)
	}
	return tag.RowsAffected(), nil
}
