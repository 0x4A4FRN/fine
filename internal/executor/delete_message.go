package executor

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

// DeleteMessageDiscordAPI is the narrow set of Discord operations DeleteMessageExecutor needs:
// MemberAPI for the permission gate, ChannelMessageAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type DeleteMessageDiscordAPI interface {
	MemberAPI
	ChannelMessageAPI
}

type DeleteMessageExecutor struct {
	discord DeleteMessageDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewDeleteMessageExecutor(
	discord DeleteMessageDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
	logger *zap.Logger,
) *DeleteMessageExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DeleteMessageExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *DeleteMessageExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: delete_message: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	messageID, err := extractMessageTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: delete_message: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "delete", "no_message")
	}

	deletePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, deletePermFn, "delete", action, messageID, true, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.DeleteMessage(action.ChannelID, messageID); err != nil {
		e.logger.Error("executor: delete_message: discord api call failed",
			zap.String("target_id", messageID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: delete_message: %w", err)
	}

	var reason string
	if action.Parameters.Reason != nil {
		reason = *action.Parameters.Reason
	}

	if err := audit.WriteAction(ctx, e.pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        messageID,
		TargetType:      "message",
		Intent:          action.Intent,
		Reason:          reason,
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		e.logger.Error("executor: delete_message: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: delete_message: audit write: %w", err)
	}

	e.logger.Info("executor: delete_message: executed",
		zap.String("target_id", messageID),
	)
	return nil
}

func extractMessageTarget(targets []llm.Target) (string, error) {
	for _, t := range targets {
		if t.Type == "message" {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("executor: no message target found")
}

var _ Executor = (*DeleteMessageExecutor)(nil)
