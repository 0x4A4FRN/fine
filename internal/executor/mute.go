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

// MuteDiscordAPI is the narrow set of Discord operations MuteExecutor needs:
// MemberAPI for the permission gate, MemberEditAPI for the actual mute,
// VoiceStateAPI for the ensureTargetInVoice pre-check.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type MuteDiscordAPI interface {
	MemberAPI
	MemberEditAPI
	VoiceStateAPI
}

type MuteExecutor struct {
	discord MuteDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewMuteExecutor(
	discord MuteDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *MuteExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MuteExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *MuteExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: mute: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: mute: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "mute", "no_target")
	}

	mutePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionVoiceMuteMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, mutePermFn, "mute", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := ensureTargetInVoice(
		e.discord, e.replies, e.logger, action.Intent, action.GuildID, userID,
	); err != nil {
		return err
	}

	muted := true
	if err := e.discord.GuildMemberEdit(
		action.GuildID, userID,
		&discordgo.GuildMemberParams{Mute: &muted},
	); err != nil {
		e.logger.Error("executor: mute: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: mute: %w", err)
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
		e.logger.Error("executor: mute: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: mute: audit write: %w", err)
	}

	e.logger.Info("executor: mute: executed", zap.String("target_id", userID))
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *MuteExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionVoiceMuteMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "mute", action, userID, false, false, false)
}

var _ Executor = (*MuteExecutor)(nil)
