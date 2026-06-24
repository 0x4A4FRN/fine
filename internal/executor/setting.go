package executor

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/replies"
)

type GuildSettingsDB interface {
	UpsertGuildSettings(ctx context.Context, gs GuildSettings) error
}

type SettingDiscordAPI interface {
	MemberAPI
}

type SettingExecutor struct {
	discord  SettingDiscordAPI
	pool     GuildSettingsDB
	snapshot *GuildSettingsSnapshot
	replies  replies.Renderer
	logger   *zap.Logger
}

func NewSettingExecutor(
	discord SettingDiscordAPI,
	pool GuildSettingsDB,
	snapshot *GuildSettingsSnapshot,
	replies replies.Renderer,
	logger *zap.Logger,
) *SettingExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SettingExecutor{
		discord:  discord,
		pool:     pool,
		snapshot: snapshot,
		replies:  replies,
		logger:   logger,
	}
}

func (e *SettingExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: toggle_setting: executing",
		zap.String("guild_id", action.GuildID),
	)

	if msg := e.checkPermission(action); msg != "" {
		return &TextResult{Text: msg}
	}

	if action.Parameters.Setting == nil || action.Parameters.Value == nil {
		return &TextResult{
			Text: e.replies.Get("setting", "invalid_value", nil),
		}
	}

	setting := strings.ToLower(strings.TrimSpace(*action.Parameters.Setting))
	value := strings.ToLower(strings.TrimSpace(*action.Parameters.Value))
	if setting != "sudo_mode" && setting != "verbose_error" {
		return &TextResult{
			Text: renderTemplate(e.replies, "setting", "invalid_name",
				settingLabel(setting)),
		}
	}
	if value != "on" && value != "off" {
		return &TextResult{
			Text: renderTemplate(e.replies, "setting", "invalid_value",
				value),
		}
	}

	on := value == "on"

	gs := e.snapshot.UpdateSetting(action.GuildID, setting, on, action.ActorID)
	if err := e.pool.UpsertGuildSettings(ctx, gs); err != nil {
		e.logger.Error("executor: toggle_setting: db write failed",
			zap.String("setting", setting),
			zap.Error(err),
		)
		return fmt.Errorf("executor: toggle_setting: %w", err)
	}

	vars := map[string]string{
		"setting":   settingLabel(setting),
		"value":     value,
		"user_name": userTag(action.ActorID),
	}
	return &TextResult{Text: e.replies.Get("setting", "toggled", vars)}
}

func renderTemplate(
	r replies.Renderer, category, key, name string,
) string {
	if r == nil {
		return "[" + category + "." + key + "]"
	}
	return r.Get(category, key, map[string]string{
		"setting": name,
	})
}

func (e *SettingExecutor) checkPermission(action Action) string {
	authorMember, err := e.discord.GuildMember(action.GuildID, action.ActorID)
	if err != nil || authorMember == nil {
		return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
	}
	roles, err := e.discord.GuildRoles(action.GuildID)
	if err != nil || roles == nil {
		return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
	}
	authorPerms := guildPermsForRoles(roles, authorMember.Roles)
	if authorPerms&discordgo.PermissionAdministrator == 0 {
		settingName := ""
		if action.Parameters.Setting != nil {
			settingName = settingLabel(*action.Parameters.Setting)
		}
		return renderReply(e.replies, "setting", "no_permission", map[string]string{
			"setting": settingName,
		})
	}
	return ""
}

var settingDisplay = map[string]string{
	"verbose_error": "Verbose Error Logging",
	"sudo_mode":     "Sudo Mode",
}

func settingLabel(key string) string {
	if v, ok := settingDisplay[key]; ok {
		return v
	}
	return key
}

var _ Executor = (*SettingExecutor)(nil)
