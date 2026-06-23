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

// KickDiscordAPI is the narrow set of Discord operations KickExecutor needs:
// MemberAPI for the permission gate, KickAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
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

	kickPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionKickMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, kickPermFn, "kick", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.GuildMemberDelete(action.GuildID, userID); err != nil {
		e.logger.Error("executor: kick: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: kick: %w", err)
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
		e.logger.Error("executor: kick: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: kick: audit write: %w", err)
	}

	e.logger.Info("executor: kick: executed",
		zap.String("target_id", userID),
	)
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *KickExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionKickMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "kick", action, userID, false, false, false)
}

var _ Executor = (*KickExecutor)(nil)
