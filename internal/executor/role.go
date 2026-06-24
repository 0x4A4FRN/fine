package executor

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

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

	if msg := gate(e.discord, e.replies, discordgo.PermissionManageRoles, "role", action, userID, false, false, false); msg != "" {
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

	if err := writeAudit(ctx, e.pool, e.logger, action, roleID, "role"); err != nil {
		return err
	}

	e.logger.Info("executor: role: executed",
		zap.String("intent", action.Intent),
		zap.String("target_id", userID),
		zap.String("role_id", roleID),
	)
	return nil
}

func (e *RoleExecutor) PreCheck(_ context.Context, action Action) string {
	userID, _ := extractUserTarget(action.Targets)
	return gate(e.discord, e.replies, discordgo.PermissionManageRoles, "role", action, userID, false, false, false)
}

var _ Executor = (*RoleExecutor)(nil)
