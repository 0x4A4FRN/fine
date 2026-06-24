package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type UntimeoutDiscordAPI interface {
	MemberAPI
	MemberEditAPI
}

type UntimeoutExecutor struct {
	discord UntimeoutDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewUntimeoutExecutor(
	discord UntimeoutDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *UntimeoutExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UntimeoutExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *UntimeoutExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: untimeout: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: untimeout: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "untimeout", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionModerateMembers, "timeout", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.GuildMemberEdit(
		action.GuildID, userID,
		&discordgo.GuildMemberParams{
			CommunicationDisabledUntil: nil,
		},
	); err != nil {
		e.logger.Error("executor: untimeout: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: untimeout: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: untimeout: executed", zap.String("target_id", userID))
	return nil
}

func (e *UntimeoutExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionModerateMembers, "untimeout", action, userID, false, false, false)
}

var _ Executor = (*UntimeoutExecutor)(nil)
