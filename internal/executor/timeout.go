package executor

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

type TimeoutDiscordAPI interface {
	MemberAPI
	MemberEditAPI
}

type TimeoutExecutor struct {
	discord TimeoutDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewTimeoutExecutor(
	discord TimeoutDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *TimeoutExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TimeoutExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *TimeoutExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: timeout: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: timeout: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "timeout", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionModerateMembers, "timeout", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if action.Parameters.DurationSeconds == nil {
		e.logger.Error("executor: timeout: duration required but absent")
		return replyTextFor(e.replies, "timeout", "requires_duration")
	}

	duration := time.Duration(*action.Parameters.DurationSeconds) * time.Second
	until := time.Now().UTC().Add(duration)

	if err := e.discord.GuildMemberEdit(
		action.GuildID, userID,
		&discordgo.GuildMemberParams{
			CommunicationDisabledUntil: &until,
		},
	); err != nil {
		e.logger.Error("executor: timeout: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: timeout: %w", err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: timeout: executed",
		zap.String("target_id", userID),
		zap.Int("duration_seconds", *action.Parameters.DurationSeconds),
	)
	return nil
}

func (e *TimeoutExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionModerateMembers, "timeout", action, userID, false, false, false)
}

var _ Executor = (*TimeoutExecutor)(nil)
