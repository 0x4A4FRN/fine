package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type DeleteMessageDiscordAPI interface {
	MemberAPI
	ChannelMessageAPI
}

type DeleteMessageExecutor struct {
	discord DeleteMessageDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewDeleteMessageExecutor(
	discord DeleteMessageDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
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

	if msg := gate(e.discord, e.replies, discordgo.PermissionManageMessages, "delete", action, messageID, true, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.DeleteMessage(action.ChannelID, messageID); err != nil {
		e.logger.Error("executor: delete_message: discord api call failed",
			zap.String("target_id", messageID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: delete_message: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, messageID, "message"); err != nil {
		return err
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
