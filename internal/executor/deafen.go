package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type DeafenDiscordAPI interface {
	MemberAPI
	MemberEditAPI
	VoiceStateAPI
}

type DeafenExecutor struct {
	discord DeafenDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewDeafenExecutor(
	discord DeafenDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *DeafenExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DeafenExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *DeafenExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: deafen: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: deafen: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "deafen", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionVoiceDeafenMembers, "deafen", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := ensureTargetInVoice(
		e.discord, e.replies, e.logger, action.Intent, action.GuildID, userID,
	); err != nil {
		return err
	}

	deafened := true
	if err := e.discord.GuildMemberEdit(
		action.GuildID, userID,
		&discordgo.GuildMemberParams{Deaf: &deafened},
	); err != nil {
		e.logger.Error("executor: deafen: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: deafen: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: deafen: executed", zap.String("target_id", userID))
	return nil
}

func (e *DeafenExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionVoiceDeafenMembers, "deafen", action, userID, false, false, false)
}

var _ Executor = (*DeafenExecutor)(nil)
