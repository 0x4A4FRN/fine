package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
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

			if logger != nil {
				logger.Debug("executor: voice state lookup: user not in voice state cache",
					zap.String("user_id", userID),
				)
			}
		} else {

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

func writeAudit(ctx context.Context, pool audit.DB, logger *zap.Logger, action Action, targetID, targetType string) error {
	if err := audit.WriteAction(ctx, pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        targetID,
		TargetType:      targetType,
		Intent:          action.Intent,
		Reason:          orEmpty(action.Parameters.Reason),
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		logger.Error("executor: "+action.Intent+": audit write failed", zap.Error(err))
		return fmt.Errorf("executor: %s: audit write: %w", action.Intent, err)
	}
	return nil
}

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
