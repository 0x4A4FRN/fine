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

// TimeoutDiscordAPI is the narrow set of Discord operations TimeoutExecutor needs:
// MemberAPI for the permission gate, MemberEditAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type TimeoutDiscordAPI interface {
	MemberAPI
	MemberEditAPI
}

type TimeoutExecutor struct {
	discord TimeoutDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewTimeoutExecutor(
	discord TimeoutDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
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

	timeoutPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionModerateMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, timeoutPermFn, "timeout", action, userID, false, false, false); msg != "" {
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

	if err := audit.WriteAction(ctx, e.pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        userID,
		TargetType:      "user",
		Intent:          action.Intent,
		Reason:          orEmpty(action.Parameters.Reason),
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		e.logger.Error("executor: timeout: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: timeout: audit write: %w", err)
	}

	e.logger.Info("executor: timeout: executed",
		zap.String("target_id", userID),
		zap.Int("duration_seconds", *action.Parameters.DurationSeconds),
	)
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *TimeoutExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionModerateMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "timeout", action, userID, false, false, false)
}

var _ Executor = (*TimeoutExecutor)(nil)
