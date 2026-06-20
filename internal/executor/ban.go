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

// BanDiscordAPI is the narrow set of Discord operations BanExecutor needs:
// MemberAPI for the permission gate, BanAPI for the actual ban. Defining it
// consumer-side lets tests mock only these two sub-interfaces instead of
// the full DiscordAPI composite.
type BanDiscordAPI interface {
	MemberAPI
	BanAPI
}

type BanExecutor struct {
	discord BanDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewBanExecutor(
	discord BanDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
	logger *zap.Logger,
) *BanExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BanExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *BanExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: ban: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: ban: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "ban", "no_target")
	}

	banPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionBanMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, banPermFn, "ban", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	reason := orEmpty(action.Parameters.Reason)

	if err := e.discord.GuildBanCreate(
		action.GuildID, userID, reason, 0,
	); err != nil {
		e.logger.Error("executor: ban: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: ban: %w", err)
	}

	if err := audit.WriteAction(ctx, e.pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        userID,
		TargetType:      "user",
		Intent:          action.Intent,
		Reason:          reason,
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		e.logger.Error("executor: ban: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: ban: audit write: %w", err)
	}

	e.logger.Info("executor: ban: executed",
		zap.String("target_id", userID),
	)
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *BanExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionBanMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "ban", action, userID, false, false, false)
}

var _ Executor = (*BanExecutor)(nil)
