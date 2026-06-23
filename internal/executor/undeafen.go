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

// UndeafenDiscordAPI is the narrow set of Discord operations UndeafenExecutor needs:
// MemberAPI for the permission gate, MemberEditAPI for the actual undeafen,
// VoiceStateAPI for the ensureTargetInVoice pre-check.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
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

	undeafenPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionVoiceDeafenMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, undeafenPermFn, "undeafen", action, userID, false, false, false); msg != "" {
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
		e.logger.Error("executor: undeafen: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: undeafen: audit write: %w", err)
	}

	e.logger.Info("executor: undeafen: executed", zap.String("target_id", userID))
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *UndeafenExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionVoiceDeafenMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "undeafen", action, userID, false, false, false)
}

var _ Executor = (*UndeafenExecutor)(nil)
