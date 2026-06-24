package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type BanDiscordAPI interface {
	MemberAPI
	BanAPI
}

type BanExecutor struct {
	discord BanDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewBanExecutor(
	discord BanDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *BanExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BanExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *BanExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: ban: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: ban: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "ban", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionBanMembers, "ban", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	reason := orEmpty(action.Parameters.Reason)

	if err := e.discord.GuildBanCreate(
		action.GuildID, userID, reason, 0,
	); err != nil {
		e.logger.Error("executor: ban: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: ban: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: ban: executed",
		zap.String("target_id", userID),
	)
	return nil
}

func (e *BanExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionBanMembers, "ban", action, userID, false, false, false)
}

var _ Executor = (*BanExecutor)(nil)
