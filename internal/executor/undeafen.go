package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type UndeafenDiscordAPI interface {
	MemberAPI
	MemberEditAPI
	VoiceStateAPI
}

type UndeafenExecutor struct {
	discord UndeafenDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewUndeafenExecutor(
	discord UndeafenDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *UndeafenExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UndeafenExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *UndeafenExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: undeafen: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: undeafen: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "undeafen", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionVoiceDeafenMembers, "undeafen", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := ensureTargetInVoice(
		e.discord, e.replies, e.logger, action.Intent, action.GuildID, userID,
	); err != nil {
		return err
	}

	deafened := false
	if err := e.discord.GuildMemberEdit(
		action.GuildID, userID,
		&discordgo.GuildMemberParams{Deaf: &deafened},
	); err != nil {
		e.logger.Error("executor: undeafen: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: undeafen: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: undeafen: executed", zap.String("target_id", userID))
	return nil
}

func (e *UndeafenExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionVoiceDeafenMembers, "undeafen", action, userID, false, false, false)
}

var _ Executor = (*UndeafenExecutor)(nil)
