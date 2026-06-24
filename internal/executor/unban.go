package executor

import (
	"context"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type UnbanDiscordAPI interface {
	MemberAPI
	BanAPI
}

type UnbanExecutor struct {
	discord UnbanDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewUnbanExecutor(
	discord UnbanDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *UnbanExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UnbanExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *UnbanExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: unban: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: unban: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "unban", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionBanMembers, "unban", action, userID, false, false, true); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.GuildBanDelete(action.GuildID, userID); err != nil {
		e.logger.Error("executor: unban: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: unban: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: unban: executed",
		zap.String("target_id", userID),
	)
	return nil
}

func (e *UnbanExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionBanMembers, "unban", action, userID, false, false, true)
}

var _ Executor = (*UnbanExecutor)(nil)
