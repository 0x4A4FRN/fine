package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/0x4A4FRN/fine/internal/replies"
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
	Source     string // "bot" | "native" | "external" | "unknown"
	ActorIsBot bool
	ActorName  string
}

type ActorState int

const (
	ActorStateExternalBotMention ActorState = iota
	ActorStateExternalNamed
	ActorStateExternalUnknownBot
	ActorStateBotMention
)

func (r *LookupResult) Classify() ActorState {
	if r == nil {
		return ActorStateExternalUnknownBot
	}
	switch r.Source {
	case SourceExternal:
		switch {
		case !r.ActorIsBot && r.ActorID != "" && r.ActorID != "unknown":
			return ActorStateExternalBotMention
		case r.ActorName != "":
			return ActorStateExternalNamed
		default:
			return ActorStateExternalUnknownBot
		}
	case SourceUnknown:
		if r.ActorID != "" && r.ActorID != "unknown" {
			return ActorStateExternalBotMention
		}
		if r.ActorName != "" {
			return ActorStateExternalNamed
		}
		return ActorStateExternalUnknownBot
	default:
		return ActorStateBotMention
	}
}

type AuditQuery struct {
	Action   *string
	TargetID *string
	Info     string
}

var intentVerbForms = map[string]struct {
	Past string
	Noun string
}{
	"ban":              {"banned", "ban"},
	"unban":            {"unbanned", "unban"},
	"kick":             {"kicked", "kick"},
	"timeout":          {"timed out", "timeout"},
	"untimeout":        {"untimed out", "untimeout"},
	"mute":             {"muted", "mute"},
	"unmute":           {"unmuted", "unmute"},
	"deafen":           {"deafened", "deafen"},
	"undeafen":         {"undeafened", "undeafen"},
	"set_nickname":     {"renamed", "rename"},
	"reset_nickname":   {"reset", "reset"},
	"add_role":         {"given a role", "role add"},
	"remove_role":      {"had a role removed", "role remove"},
	"pin_message":      {"pinned a message", "pin"},
	"unpin_message":    {"unpinned a message", "unpin"},
	"delete_message":   {"had a message deleted", "deletion"},
	"purge_messages":   {"had messages purged", "purge"},
	"voice_disconnect": {"disconnected from voice", "voice disconnect"},
	"channel_create":   {"created", "channel creation"},
	"channel_update":   {"updated", "channel update"},
	"channel_delete":   {"deleted", "channel deletion"},
	"role_create":      {"created", "role creation"},
	"role_update":      {"updated", "role update"},
	"role_delete":      {"deleted", "role deletion"},
	"guild_update":     {"changed", "server settings update"},
}

func PastTenseIntent(intent string) string {
	if v, ok := intentVerbForms[intent]; ok {
		return v.Past
	}
	return intent
}

func IntentNoun(intent string) string {
	if v, ok := intentVerbForms[intent]; ok {
		return v.Noun
	}
	return intent
}

func RelativeTime(t time.Time) string {
	return fmt.Sprintf("<t:%d:R>", t.Unix())
}

const lookupSQL = `
SELECT guild_id, channel_id, actor_id, target_id, target_type,
       intent, reason, parameters, executed_at,
       source, actor_is_bot, actor_name
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
		&result.Source,
		&result.ActorIsBot,
		&result.ActorName,
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
	ModeratorID     string // legacy field — resolved human snowflake or "unknown".
	Actor           string // format-appropriate label: <@id>, "a user named X", "a bot".
	PastTenseIntent string
	IntentNoun      string
	Reason          string
	RelativeTime    string
	GuildID         string
	ChannelID       string
	HasReason       bool
	Source          string
}

func BuildTemplateData(result *LookupResult, renderer replies.Renderer) TemplateData {
	if result == nil {
		return TemplateData{
			Actor: renderer.Get("handler", "actor_label_unknown_moderator", nil),
		}
	}

	var actor string
	switch result.Classify() {
	case ActorStateBotMention, ActorStateExternalBotMention, ActorStateExternalNamed:
		if result.ActorID != "" && result.ActorID != "unknown" {
			actor = result.ActorID
		} else if result.ActorName != "" {
			actor = renderer.Get(
				"handler", "actor_label_user_named",
				map[string]string{"name": result.ActorName},
			)
		} else {
			actor = renderer.Get("handler", "actor_label_unknown_moderator", nil)
		}
	case ActorStateExternalUnknownBot:
		if result.ActorIsBot && result.ActorID != "" && result.ActorID != "unknown" {
			actor = renderer.Get(
				"handler", "actor_label_bot_mention",
				map[string]string{"id": result.ActorID},
			)
		} else {
			actor = renderer.Get("handler", "actor_label_automated_bot", nil)
		}
	default:
		actor = renderer.Get("handler", "actor_label_unknown_moderator", nil)
	}

	return TemplateData{
		TargetID:        result.TargetID,
		ModeratorID:     actor,
		Actor:           actor,
		PastTenseIntent: PastTenseIntent(result.Intent),
		IntentNoun:      IntentNoun(result.Intent),
		Reason:          result.Reason,
		RelativeTime:    RelativeTime(result.ExecutedAt),
		GuildID:         result.GuildID,
		ChannelID:       result.ChannelID,
		HasReason:       result.Reason != "",
		Source:          result.Source,
	}
}

func SelectTemplate(info string, result *LookupResult, hasTarget bool) string {
	if result == nil {
		return "audit.no_record"
	}

	switch result.TargetType {
	case "message":
		return "audit.message_target"
	case "channel":
		return "audit.details_channel"
	case "role":
		return "audit.details_role"
	case "guild":
		return "audit.details_guild"
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
