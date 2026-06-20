package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type LookupResult struct {
	GuildID    string
	ChannelID  string
	ActorID    string
	TargetID   string
	TargetType string
	Intent     string
	Reason     string
	Parameters string
	ExecutedAt time.Time
}

type AuditQuery struct {
	Action   *string
	TargetID *string
	Info     string
}

var intentPastTense = map[string]string{
	"ban":            "banned",
	"unban":          "unbanned",
	"kick":           "kicked",
	"timeout":        "timed out",
	"untimeout":      "untimed out",
	"mute":           "muted",
	"unmute":         "unmuted",
	"deafen":         "deafened",
	"undeafen":       "undeafened",
	"set_nickname":   "renamed",
	"reset_nickname": "reset",
	"add_role":       "given a role",
	"remove_role":    "had a role removed",
	"pin_message":    "pinned a message",
	"unpin_message":  "unpinned a message",
	"delete_message": "had a message deleted",
	"purge_messages": "had messages purged",
}

var intentNoun = map[string]string{
	"ban":            "ban",
	"unban":          "unban",
	"kick":           "kick",
	"timeout":        "timeout",
	"untimeout":      "untimeout",
	"mute":           "mute",
	"unmute":         "unmute",
	"deafen":         "deafen",
	"undeafen":       "undeafen",
	"set_nickname":   "rename",
	"reset_nickname": "reset",
	"add_role":       "role add",
	"remove_role":    "role remove",
	"pin_message":    "pin",
	"unpin_message":  "unpin",
	"delete_message": "deletion",
	"purge_messages": "purge",
}

func PastTenseIntent(intent string) string {
	if past, ok := intentPastTense[intent]; ok {
		return past
	}
	return intent
}

func IntentNoun(intent string) string {
	if noun, ok := intentNoun[intent]; ok {
		return noun
	}
	return intent
}

func RelativeTime(t time.Time) string {
	return fmt.Sprintf("<t:%d:R>", t.Unix())
}

const lookupSQL = `
SELECT guild_id, channel_id, actor_id, target_id, target_type,
       intent, reason, parameters, executed_at
FROM mod_actions
WHERE guild_id = $1
  AND ($2::text IS NULL OR target_id = $2)
  AND ($3::text IS NULL OR intent = $3)
ORDER BY executed_at DESC
LIMIT 1`

func Lookup(
	ctx context.Context,
	db DB,
	guildID string,
	query AuditQuery,
) (*LookupResult, error) {
	var result LookupResult
	var reason *string
	var params *string

	err := db.QueryRow(ctx, lookupSQL, guildID, query.TargetID, query.Action).Scan(
		&result.GuildID,
		&result.ChannelID,
		&result.ActorID,
		&result.TargetID,
		&result.TargetType,
		&result.Intent,
		&reason,
		&params,
		&result.ExecutedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("audit: lookup query: %w", err)
	}

	if reason != nil {
		result.Reason = *reason
	}
	if params != nil {
		result.Parameters = *params
	}

	return &result, nil
}

type TemplateData struct {
	TargetID        string
	ModeratorID     string
	PastTenseIntent string
	IntentNoun      string
	Reason          string
	RelativeTime    string
	GuildID         string
	ChannelID       string
	HasReason       bool
}

func BuildTemplateData(result *LookupResult) TemplateData {
	return TemplateData{
		TargetID:        result.TargetID,
		ModeratorID:     result.ActorID,
		PastTenseIntent: PastTenseIntent(result.Intent),
		IntentNoun:      IntentNoun(result.Intent),
		Reason:          result.Reason,
		RelativeTime:    RelativeTime(result.ExecutedAt),
		GuildID:         result.GuildID,
		ChannelID:       result.ChannelID,
		HasReason:       result.Reason != "",
	}
}

func SelectTemplate(info string, result *LookupResult, hasTarget bool) string {
	if result == nil {
		return "audit.no_record"
	}

	if result.TargetType == "message" {
		return "audit.message_target"
	}

	switch info {
	case "actor":
		if hasTarget {
			return "audit.actor_with_target"
		}
		return "audit.actor_no_target"

	case "reason":
		if result.Reason != "" {
			return "audit.reason_with_reason"
		}
		return "audit.reason_without_reason"

	case "details":
		if result.Reason != "" {
			return "audit.details_with_reason"
		}
		return "audit.details_without_reason"

	default:
		return "audit.no_record"
	}
}
