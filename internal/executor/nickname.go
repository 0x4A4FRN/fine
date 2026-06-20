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

// NicknameDiscordAPI is the narrow set of Discord operations NicknameExecutor needs:
// MemberAPI for the permission gate, MemberEditAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type NicknameDiscordAPI interface {
	MemberAPI
	MemberEditAPI
}

type NicknameExecutor struct {
	discord NicknameDiscordAPI
	pool    audit.DB
	replies *replies.Replies
	logger  *zap.Logger
}

func NewNicknameExecutor(
	discord NicknameDiscordAPI,
	pool audit.DB,
	replies *replies.Replies,
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

	// Default to self-target for set_nickname / reset_nickname. Users are
	// legitimately allowed to change their own nickname via the bot, so when
	// the LLM has not supplied an explicit target (or no @mention was found)
	// we treat the actor as the target. An explicit LLM-supplied target
	// wins (the actor can still name themselves or others).
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
		// Reflect into action.Targets so downstream logging and audit rows
		// are consistent with the resolved target.
		action.Targets = []llm.Target{{Type: "user", ID: action.ActorID}}
	default:
		e.logger.Error("executor: nickname: extract target failed",
			zap.String("intent", action.Intent),
			zap.Error(extractErr),
		)
		return replyTextFor(e.replies, "nickname", "no_target")
	}

	nicknamePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageNicknames|discordgo.PermissionAdministrator) != 0
	}
	// Final arg true: opt out of the bot/author/owner self-protection check
	// for set_nickname / reset_nickname. Self-target is the legitimate
	// default for these non-destructive intents. Permission, hierarchy, and
	// member-existence checks still run.
	if msg := gate(e.discord, e.replies, nicknamePermFn, "nickname", action, userID, false, true, false); msg != "" {
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
		e.logger.Error("executor: nickname: audit write failed",
			zap.String("intent", action.Intent),
			zap.Error(err),
		)
		return fmt.Errorf("executor: %s: audit write: %w", action.Intent, err)
	}

	e.logger.Info("executor: nickname: executed",
		zap.String("intent", action.Intent),
		zap.String("target_id", userID),
	)
	return nil
}

var _ Executor = (*NicknameExecutor)(nil)
