package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	SourceBot      = "bot"
	SourceNative   = "native"
	SourceExternal = "external"
	SourceUnknown  = "unknown"
)

type ExternalAction struct {
	ModAction
	Source     string
	ActorIsBot bool
	ActorName  string
}

const insertExternalSQL = `
INSERT INTO mod_actions (
    guild_id,
    channel_id,
    actor_id,
    target_id,
    target_type,
    intent,
    reason,
    parameters,
    source_message_id,
    confirmed_at,
    executed_at,
    source,
    actor_is_bot,
    actor_name
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
)
RETURNING id`

func InsertExternal(ctx context.Context, db DB, action ExternalAction) (int64, error) {
	params, err := marshalParameters(action.Parameters)
	if err != nil {
		return 0, fmt.Errorf("audit: insert external: %w", err)
	}

	var (
		reason          *string
		sourceMessageID *string
	)
	if action.Reason != "" {
		reason = &action.Reason
	}
	if action.SourceMessageID != "" {
		sourceMessageID = &action.SourceMessageID
	}

	source := action.Source
	if source == "" {
		source = SourceUnknown
	}

	var rowID int64
	err = db.QueryRow(
		ctx,
		insertExternalSQL,
		action.GuildID,
		action.ChannelID,
		action.ActorID,
		action.TargetID,
		action.TargetType,
		action.Intent,
		reason,
		params,
		sourceMessageID,
		action.ConfirmedAt,
		action.ExecutedAt,
		source,
		action.ActorIsBot,
		action.ActorName,
	).Scan(&rowID)
	if err != nil {
		return 0, fmt.Errorf("audit: insert external: %w", err)
	}

	return rowID, nil
}

const updateActorSQL = `
UPDATE mod_actions
SET actor_id    = COALESCE(NULLIF($2, ''), actor_id),
    actor_is_bot = $3,
    actor_name   = $4,
    reason       = COALESCE(NULLIF($5, ''), reason)
WHERE id = $1`

type ActorUpdate struct {
	RowID      int64
	ActorID    string
	ActorIsBot bool
	ActorName  string
	Reason     string
}

func UpdateActor(ctx context.Context, db DB, upd ActorUpdate) error {
	if upd.RowID == 0 {
		return fmt.Errorf("audit: update actor: rowID must not be zero")
	}

	_, err := db.Exec(
		ctx,
		updateActorSQL,
		upd.RowID,
		upd.ActorID,
		upd.ActorIsBot,
		upd.ActorName,
		upd.Reason,
	)
	if err != nil {
		return fmt.Errorf("audit: update actor: %w", err)
	}

	return nil
}

const recentBotActionSQL = `
SELECT EXISTS (
    SELECT 1 FROM mod_actions
    WHERE guild_id     = $1
      AND target_id    = $2
      AND intent       = $3
      AND source       = 'bot'
      AND executed_at >= NOW() - $4 * INTERVAL '1 second'
)`

func RecentBotAction(
	ctx context.Context,
	db DB,
	guildID, targetID, intent string,
	window time.Duration,
) (bool, error) {
	if window <= 0 {
		window = 10 * time.Second
	}

	var recent bool
	if err := db.QueryRow(
		ctx, recentBotActionSQL, guildID, targetID, intent, int64(window.Seconds()),
	).Scan(&recent); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("audit: dedup query: %w", err)
	}

	return recent, nil
}
