package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

// UnbanDiscordAPI is the narrow set of Discord operations UnbanExecutor needs:
// MemberAPI for the permission gate, BanAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type UnbanDiscordAPI interface {
	MemberAPI
	BanAPI
}

type UnbanExecutor struct {
	discord UnbanDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewUnbanExecutor(
	discord UnbanDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *UnbanExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UnbanExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *UnbanExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: unban: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: unban: extract target failed", zap.Error(err))
		return replyTextFor(e.replies, "unban", "no_target")
	}

	unbanPermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionBanMembers|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, unbanPermFn, "unban", action, userID, false, false, true); msg != "" {
		return &TextResult{Text: msg}
	}

	if err := e.discord.GuildBanDelete(action.GuildID, userID); err != nil {
		e.logger.Error("executor: unban: discord api call failed",
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: unban: %w", err)
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
		e.logger.Error("executor: unban: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: unban: audit write: %w", err)
	}

	e.logger.Info("executor: unban: executed",
		zap.String("target_id", userID),
	)
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *UnbanExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	permFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionBanMembers|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, permFn, "unban", action, userID, false, false, true)
}

var _ Executor = (*UnbanExecutor)(nil)
