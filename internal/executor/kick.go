package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type KickDiscordAPI interface {
	MemberAPI
	KickAPI
}

type KickExecutor struct {
	discord KickDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewKickExecutor(
	discord KickDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *KickExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &KickExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *KickExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: kick: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: kick: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "kick", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionKickMembers, "kick", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.GuildMemberDelete(action.GuildID, userID); err != nil {
		e.logger.Error("executor: kick: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: kick: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: kick: executed",
		zap.String("target_id", userID),
	)
	return nil
}

func (e *KickExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionKickMembers, "kick", action, userID, false, false, false)
}

var _ Executor = (*KickExecutor)(nil)
