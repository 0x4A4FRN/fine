package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type UnpinDiscordAPI interface {
	MemberAPI
	PinAPI
}

type UnpinExecutor struct {
	discord UnpinDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewUnpinExecutor(
	discord UnpinDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
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

	if msg := gate(e.discord, e.replies, discordgo.PermissionManageMessages, "unpin", action, messageID, true, false, false); msg != "" {
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

	if err := writeAudit(ctx, e.pool, e.logger, action, messageID, "message"); err != nil {
		return err
	}

	e.logger.Info("executor: unpin: executed",
		zap.String("target_id", messageID),
	)
	return nil
}

var _ Executor = (*UnpinExecutor)(nil)
