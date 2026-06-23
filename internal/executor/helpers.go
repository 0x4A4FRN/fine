package executor

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

func userTag(userID string) string {
	return "<@" + userID + ">"
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func extractUserTarget(targets []llm.Target) (string, error) {
	for _, t := range targets {
		if t.Type == "user" {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("executor: no user target found")
}

func extractRoleTarget(targets []llm.Target) (string, error) {
	for _, t := range targets {
		if t.Type == "role" {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("executor: no role target found")
}

func voiceNotInVoiceText(r replies.Renderer, intent, userID string) string {
	if r == nil {
		return fmt.Sprintf("<@%s> isn't connected to voice.", userID)
	}
	return r.Get(intent, "not_in_voice", map[string]string{
		"user_name": "<@" + userID + ">",
	})
}

func ensureTargetInVoice(
	api VoiceStateAPI,
	r replies.Renderer,
	logger *zap.Logger,
	intent, guildID, userID string,
) error {
	if api == nil {
		return nil
	}
	vs, err := api.GuildMemberVoiceState(guildID, userID)
	if err != nil {
		if errors.Is(err, discordgo.ErrStateNotFound) {
			// Expected: the user simply has no voice state (never connected,
			// or Discord's state cache has no entry). Falls through to the
			// vs/ChannelID check below which surfaces the "not in voice"
			// reply.
			if logger != nil {
				logger.Debug("executor: voice state lookup: user not in voice state cache",
					zap.String("user_id", userID),
				)
			}
		} else {
			// Unexpected error (network, permissions, etc.). Log it but still
			// fall through to the "not in voice" reply so the caller gets a
			// deterministic, safe result rather than an opaque failure.
			if logger != nil {
				logger.Warn("executor: voice state lookup error; treating as not-in-voice",
					zap.String("user_id", userID),
					zap.Error(err),
				)
			}
		}
	}
	if vs == nil || vs.ChannelID == "" {
		return &TextResult{Text: voiceNotInVoiceText(r, intent, userID)}
	}
	return nil
}

func replyTextFor(r replies.Renderer, category, key string) *TextResult {
	if r == nil {
		return &TextResult{Text: "[" + category + "." + key + "]"}
	}
	return &TextResult{Text: r.Get(category, key, nil)}
}

// auditParameters merges a sudo marker into the executor's parameter blob when
// the action was sudo-bypassed. Returns the original value unchanged when
// sudo is false so the JSON shape is identical for confirm-flow actions.
//
// Note: errors are silently swallowed because this is a free function
// without access to a logger. The sudo marker is best-effort; if the
// JSON round-trip fails, the audit row is written without it.
func auditParameters(action Action, base any) any {
	if !action.Sudo {
		return base
	}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return base
	}
	m := make(map[string]any)
	if err := json.Unmarshal(baseJSON, &m); err != nil {
		return base
	}
	m["sudo"] = true
	return m
}
