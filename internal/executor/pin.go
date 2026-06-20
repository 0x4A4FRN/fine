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

// PinDiscordAPI is the narrow set of Discord operations PinExecutor needs:
// MemberAPI for the permission gate, PinAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type PinDiscordAPI interface {
	MemberAPI
	PinAPI
}

type PinExecutor struct {
	discord PinDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewPinExecutor(
	discord PinDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
	logger *zap.Logger,
) *PinExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PinExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *PinExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: pin: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	messageID, err := extractMessageTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: pin: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "pin", "no_message")
	}

	pinPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, pinPermFn, "pin", action, messageID, true, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.ChannelMessagePin(
		action.ChannelID, messageID,
	); err != nil {
		e.logger.Error("executor: pin: discord api call failed",
			zap.String("target_id", messageID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: pin_message: %w", err)
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
		e.logger.Error("executor: pin: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: pin_message: audit write: %w", err)
	}

	e.logger.Info("executor: pin: executed",
		zap.String("target_id", messageID),
	)
	return nil
}

var _ Executor = (*PinExecutor)(nil)
