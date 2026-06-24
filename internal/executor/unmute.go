package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type UnmuteDiscordAPI interface {
	MemberAPI
	MemberEditAPI
	VoiceStateAPI
}

type UnmuteExecutor struct {
	discord UnmuteDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewUnmuteExecutor(
	discord UnmuteDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *UnmuteExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UnmuteExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *UnmuteExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: unmute: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: unmute: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "unmute", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionVoiceMuteMembers, "unmute", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := ensureTargetInVoice(
		e.discord, e.replies, e.logger, action.Intent, action.GuildID, userID,
	); err != nil {
		return err
	}

	muted := false
	if err := e.discord.GuildMemberEdit(
		action.GuildID, userID,
		&discordgo.GuildMemberParams{Mute: &muted},
	); err != nil {
		e.logger.Error("executor: unmute: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: unmute: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: unmute: executed", zap.String("target_id", userID))
	return nil
}

func (e *UnmuteExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionVoiceMuteMembers, "unmute", action, userID, false, false, false)
}

var _ Executor = (*UnmuteExecutor)(nil)
