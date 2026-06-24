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

type NicknameDiscordAPI interface {
	MemberAPI
	MemberEditAPI
}

type NicknameExecutor struct {
	discord NicknameDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewNicknameExecutor(
	discord NicknameDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *NicknameExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &NicknameExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *NicknameExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: nickname: executing",
		zap.String("intent", action.Intent),
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	var userID string
	extractedID, extractErr := extractUserTarget(action.Targets)
	switch {
	case extractErr == nil:
		userID = extractedID
	case action.Intent == "set_nickname" || action.Intent == "reset_nickname":
		if action.ActorID == "" {
			e.logger.Error("executor: nickname: no actor id for self-target",
				zap.String("intent", action.Intent),
			)
			return replyTextFor(e.replies, "nickname", "no_target")
		}
		e.logger.Info("executor: nickname: inferring self-target from actor",
			zap.String("intent", action.Intent),
			zap.String("actor_id", action.ActorID),
		)
		userID = action.ActorID

		action.Targets = []llm.Target{{Type: "user", ID: action.ActorID}}
	default:
		e.logger.Error("executor: nickname: extract target failed",
			zap.String("intent", action.Intent),
			zap.Error(extractErr),
		)
		return replyTextFor(e.replies, "nickname", "no_target")
	}

	if msg := gate(e.discord, e.replies, discordgo.PermissionManageNicknames, "nickname", action, userID, false, true, false); msg != "" {
		return &TextResult{Text: msg}
	}

	var nick string
	if action.Intent == "set_nickname" {
		if action.Parameters.Nickname == nil {
			e.logger.Error("executor: set_nickname: nickname required but absent")
			return replyTextFor(e.replies, "nickname", "missing_nickname")
		}
		nick = *action.Parameters.Nickname
	}

	if err := e.discord.GuildMemberNickname(
		action.GuildID, userID, nick,
	); err != nil {
		e.logger.Error("executor: nickname: discord api call failed",
			zap.String("intent", action.Intent),
			zap.String("target_id", userID),
			zap.Error(err),
		)
		return fmt.Errorf("executor: %s: %w", action.Intent, err)
	}

	if err := writeAudit(ctx, e.pool, e.logger, action, userID, "user"); err != nil {
		return err
	}

	e.logger.Info("executor: nickname: executed",
		zap.String("intent", action.Intent),
		zap.String("target_id", userID),
	)
	return nil
}

var _ Executor = (*NicknameExecutor)(nil)
