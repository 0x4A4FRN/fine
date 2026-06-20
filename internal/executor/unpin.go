package executor

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

// UnpinDiscordAPI is the narrow set of Discord operations UnpinExecutor needs:
// MemberAPI for the permission gate, PinAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type UnpinDiscordAPI interface {
	MemberAPI
	PinAPI
}

type UnpinExecutor struct {
	discord UnpinDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewUnpinExecutor(
	discord UnpinDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
	logger *zap.Logger,
) *UnpinExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UnpinExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *UnpinExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: unpin: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	messageID, err := extractMessageTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: unpin: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "unpin", "no_message")
	}

	unpinPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, unpinPermFn, "unpin", action, messageID, true, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.ChannelMessageUnpin(
		action.ChannelID, messageID,
	); err != nil {
		e.logger.Error("executor: unpin: discord api call failed",
			zap.String("target_id", messageID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: unpin_message: %w", err)
	}

	if err := audit.WriteAction(ctx, e.pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        messageID,
		TargetType:      "message",
		Intent:          action.Intent,
		Reason:          orEmpty(action.Parameters.Reason),
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		e.logger.Error("executor: unpin: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: unpin_message: audit write: %w", err)
	}

	e.logger.Info("executor: unpin: executed",
		zap.String("target_id", messageID),
	)
	return nil
}

var _ Executor = (*UnpinExecutor)(nil)
