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

// DeafenDiscordAPI is the narrow set of Discord operations DeafenExecutor needs:
// MemberAPI for the permission gate, MemberEditAPI for the actual deafen,
// VoiceStateAPI for the ensureTargetInVoice pre-check.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
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

	deafenPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionVoiceDeafenMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, deafenPermFn, "deafen", action, userID, false, false, false); msg != "" {
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
		e.logger.Error("executor: deafen: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: deafen: audit write: %w", err)
	}

	e.logger.Info("executor: deafen: executed", zap.String("target_id", userID))
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *DeafenExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionVoiceDeafenMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "deafen", action, userID, false, false, false)
}

var _ Executor = (*DeafenExecutor)(nil)
