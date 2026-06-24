package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	*pgxpool.Pool
}

func NewPool(ctx context.Context, connString string) (*Pool, error) {
	if connString == "" {
		return nil, fmt.Errorf("db: connection string must not be empty")
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("db: creating pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return &Pool{Pool: pool}, nil
}

// ── Guild settings ──────────────────────────────────────────────────────────

type GuildSettings struct {
	GuildID      string
	SudoMode     bool
	VerboseError bool
	UpdatedBy    string
	UpdatedAt    time.Time
}

const selectGuildSettingsSQL = `
SELECT guild_id, sudo_mode, verbose_error, updated_by, updated_at
FROM guild_settings
WHERE guild_id = $1`

func (p *Pool) GetGuildSettings(
	ctx context.Context, guildID string,
) (*GuildSettings, error) {
	if guildID == "" {
		return nil, nil
	}
	var s GuildSettings
	err := p.QueryRow(ctx, selectGuildSettingsSQL, guildID).Scan(
		&s.GuildID,
		&s.SudoMode,
		&s.VerboseError,
		&s.UpdatedBy,
		&s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: querying guild_settings: %w", err)
	}
	return &s, nil
}

const upsertGuildSettingsSQL = `
INSERT INTO guild_settings (guild_id, sudo_mode, verbose_error, updated_by, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (guild_id) DO UPDATE
SET sudo_mode     = EXCLUDED.sudo_mode,
    verbose_error = EXCLUDED.verbose_error,
    updated_by    = EXCLUDED.updated_by,
    updated_at    = NOW()`

func (p *Pool) UpsertGuildSettings(
	ctx context.Context, s GuildSettings,
) error {
	if s.GuildID == "" {
		return fmt.Errorf("db: UpsertGuildSettings: guild_id required")
	}
	_, err := p.Exec(
		ctx,
		upsertGuildSettingsSQL,
		s.GuildID,
		s.SudoMode,
		s.VerboseError,
		s.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("db: upsert guild_settings: %w", err)
	}
	return nil
}

const selectAllGuildSettingsSQL = `
SELECT guild_id, sudo_mode, verbose_error, updated_by, updated_at
FROM guild_settings`

func (p *Pool) LoadAllGuildSettings(
	ctx context.Context,
) (map[string]GuildSettings, error) {
	rows, err := p.Query(ctx, selectAllGuildSettingsSQL)
	if err != nil {
		return nil, fmt.Errorf("db: querying guild_settings: %w", err)
	}
	defer rows.Close()

	out := make(map[string]GuildSettings)
	for rows.Next() {
		var s GuildSettings
		if err := rows.Scan(
			&s.GuildID,
			&s.SudoMode,
			&s.VerboseError,
			&s.UpdatedBy,
			&s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scanning guild_settings: %w", err)
		}
		out[s.GuildID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterating guild_settings: %w", err)
	}
	return out, nil
}
