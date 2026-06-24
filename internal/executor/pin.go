package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type PinDiscordAPI interface {
	MemberAPI
	PinAPI
}

type PinExecutor struct {
	discord PinDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewPinExecutor(
	discord PinDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
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

	if msg := gate(e.discord, e.replies, discordgo.PermissionManageMessages, "pin", action, messageID, true, false, false); msg != "" {
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

	if err := writeAudit(ctx, e.pool, e.logger, action, messageID, "message"); err != nil {
		return err
	}

	e.logger.Info("executor: pin: executed",
		zap.String("target_id", messageID),
	)
	return nil
}

var _ Executor = (*PinExecutor)(nil)
