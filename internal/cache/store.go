package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type CacheEntry struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Parameters string  `json:"parameters"`
	ActionType string  `json:"action_type"`
}

type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db DB
}

func NewStore(db DB) *Store {
	return &Store{db: db}
}

const selectCacheSQL = `
SELECT intent, confidence, parameters, action_type
FROM intent_cache
WHERE guild_id = $1 AND template = $2`

func (s *Store) Get(
	ctx context.Context,
	guildID, template string,
) (*CacheEntry, error) {
	var entry CacheEntry
	err := s.db.QueryRow(ctx, selectCacheSQL, guildID, template).Scan(
		&entry.Intent,
		&entry.Confidence,
		&entry.Parameters,
		&entry.ActionType,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("cache: querying entry: %w", err)
	}
	return &entry, nil
}

const upsertCacheSQL = `
INSERT INTO intent_cache (guild_id, template, intent, confidence, parameters, action_type, hits, last_hit_at)
VALUES ($1, $2, $3, $4, $5, $6, 0, NOW())
ON CONFLICT (guild_id, template)
DO UPDATE SET
    intent = EXCLUDED.intent,
    confidence = EXCLUDED.confidence,
    parameters = EXCLUDED.parameters,
    action_type = EXCLUDED.action_type,
    hits = intent_cache.hits + 1,
    last_hit_at = NOW()`

func (s *Store) Set(
	ctx context.Context,
	guildID, template string,
	entry CacheEntry,
) error {
	if _, err := s.db.Exec(
		ctx,
		upsertCacheSQL,
		guildID,
		template,
		entry.Intent,
		entry.Confidence,
		entry.Parameters,
		entry.ActionType,
	); err != nil {
		return fmt.Errorf("cache: upserting entry: %w", err)
	}
	return nil
}

func SerializeParameters(params any) (string, error) {
	if params == nil {
		return "", nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("cache: serializing parameters: %w", err)
	}
	return string(b), nil
}
