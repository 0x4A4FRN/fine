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

// UntimeoutDiscordAPI is the narrow set of Discord operations UntimeoutExecutor needs:
// MemberAPI for the permission gate, MemberEditAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type UntimeoutDiscordAPI interface {
	MemberAPI
	MemberEditAPI
}

type UntimeoutExecutor struct {
	discord UntimeoutDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewUntimeoutExecutor(
	discord UntimeoutDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
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

	untimeoutPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionModerateMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, untimeoutPermFn, "timeout", action, userID, false, false, false); msg != "" {
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
		e.logger.Error("executor: untimeout: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: untimeout: audit write: %w", err)
	}

	e.logger.Info("executor: untimeout: executed", zap.String("target_id", userID))
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *UntimeoutExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionModerateMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "untimeout", action, userID, false, false, false)
}

var _ Executor = (*UntimeoutExecutor)(nil)
