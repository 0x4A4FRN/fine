package executor

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

// RoleDiscordAPI is the narrow set of Discord operations RoleExecutor needs:
// MemberAPI for the permission gate, RoleAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type RoleDiscordAPI interface {
	MemberAPI
	RoleAPI
}

type RoleExecutor struct {
	discord RoleDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewRoleExecutor(
	discord RoleDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *RoleExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RoleExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *RoleExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: role: executing",
		zap.String("intent", action.Intent),
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	userID, err := extractUserTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: role: extract user target failed",
			zap.String("intent", action.Intent),
			zap.Error(err),
		)
		return replyTextFor(e.replies, "role", "no_target")
	}

	rolePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageRoles|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, rolePermFn, "role", action, userID, false, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	roleID, err := extractRoleTarget(action.Targets)
	if err != nil {
		e.logger.Error("executor: role: extract role target failed",
			zap.String("intent", action.Intent),
			zap.Error(err),
		)
		return replyTextFor(e.replies, "role", "no_target")
	}
	// Defense-in-depth: validate the role ID snowflake before it reaches the
	// Discord API. gate() only validates the user target; the role target
	// comes from the LLM and must be checked separately to match the
	// snowflake-validity invariant enforced for every other ID that leaves
	// this package.
	if !llm.IsValidSnowflake(roleID) {
		e.logger.Warn("executor: role: invalid role snowflake",
			zap.String("intent", action.Intent),
			zap.String("role_id", roleID),
		)
		return &TextResult{Text: renderReply(e.replies, "gateway", "invalid_user", nil)}
	}

	switch action.Intent {
	case "add_role":
		if err := e.discord.GuildMemberRoleAdd(
			action.GuildID, userID, roleID,
		); err != nil {
			e.logger.Error("executor: role: discord api call failed",
				zap.String("intent", action.Intent),
				zap.String("target_id", userID),
				zap.String("role_id", roleID),
				zap.Error(err),
			)
			return fmt.Errorf("executor: add_role: %w", err)
		}
	case "remove_role":
		if err := e.discord.GuildMemberRoleRemove(
			action.GuildID, userID, roleID,
		); err != nil {
			e.logger.Error("executor: role: discord api call failed",
				zap.String("intent", action.Intent),
				zap.String("target_id", userID),
				zap.String("role_id", roleID),
				zap.Error(err),
			)
			return fmt.Errorf("executor: remove_role: %w", err)
		}
	default:
		e.logger.Error("executor: role: unsupported intent",
			zap.String("intent", action.Intent),
		)
		return fmt.Errorf("executor: role: unsupported intent %q", action.Intent)
	}

	if err := audit.WriteAction(ctx, e.pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        roleID,
		TargetType:      "role",
		Intent:          action.Intent,
		Reason:          orEmpty(action.Parameters.Reason),
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		e.logger.Error("executor: role: audit write failed",
			zap.String("intent", action.Intent),
			zap.Error(err),
		)
		return fmt.Errorf("executor: %s: audit write: %w", action.Intent, err)
	}

	e.logger.Info("executor: role: executed",
		zap.String("intent", action.Intent),
		zap.String("target_id", userID),
		zap.String("role_id", roleID),
	)
	return nil
}

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *RoleExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	rolePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageRoles|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, rolePermFn, "role", action, userID, false, false, false)
}

var _ Executor = (*RoleExecutor)(nil)
