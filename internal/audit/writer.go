package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type ModAction struct {
	GuildID         string
	ChannelID       string
	ActorID         string
	TargetID        string
	TargetType      string
	Intent          string
	Reason          string
	Parameters      any
	SourceMessageID string
	ConfirmedAt     *time.Time
	ExecutedAt      time.Time
}

func marshalParameters(p any) (*string, error) {
	if p == nil {
		return nil, nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("audit: marshaling parameters: %w", err)
	}
	s := string(raw)
	return &s, nil
}

const insertSQL = `
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
    executed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

func WriteAction(ctx context.Context, pool DB, action ModAction) error {
	params, err := marshalParameters(action.Parameters)
	if err != nil {
		return fmt.Errorf("audit: writing mod_action: %w", err)
	}

	var reason *string
	if action.Reason != "" {
		reason = &action.Reason
	}

	var sourceMsgID *string
	if action.SourceMessageID != "" {
		sourceMsgID = &action.SourceMessageID
	}

	_, err = pool.Exec(
		ctx,
		insertSQL,
		action.GuildID,
		action.ChannelID,
		action.ActorID,
		action.TargetID,
		action.TargetType,
		action.Intent,
		reason,
		params,
		sourceMsgID,
		action.ConfirmedAt,
		action.ExecutedAt,
	)
	if err != nil {
		return fmt.Errorf("audit: writing mod_action: %w", err)
	}

	return nil
}
