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

// SettingDiscordAPI is the narrow set of Discord operations SettingExecutor needs:
// MemberAPI for the permission gate.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type SettingDiscordAPI interface {
	MemberAPI
}

type SettingExecutor struct {
	discord  SettingDiscordAPI
	pool     GuildSettingsDB
	snapshot *GuildSettingsSnapshot
	replies  *replies.Replies
	logger   *zap.Logger
}

func NewSettingExecutor(
	discord SettingDiscordAPI,
	pool GuildSettingsDB,
	snapshot *GuildSettingsSnapshot,
	replies *replies.Replies,
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

	// Custom permission check instead of gate() because the setting
	// no_permission template uses {{.setting}} which gate() can't fill
	// (gate renders with nil vars). We resolve the setting label from
	// action.Parameters.Setting so the denial message says "Sudo Mode"
	// instead of "<no value>".
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
	r *replies.Replies, category, key, name string,
) string {
	if r == nil {
		return "[" + category + "." + key + "]"
	}
	return r.Get(category, key, map[string]string{
		"setting": name,
	})
}

// checkPermission verifies the actor has Administrator. The denial reply
// includes the setting label (e.g. "Sudo Mode") so the user sees a
// meaningful name instead of the raw key or "<no value>".
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

// PreCheck runs the permission gate without executing the setting change.
// Returns "" if allowed, or the denial reply text. toggle_setting is not
// destructive so this is not currently called by the handler's confirmation
// pre-check, but the method exists for future use and testability.
func (e *SettingExecutor) PreCheck(_ context.Context, action Action) string {
	return e.checkPermission(action)
}

// settingDisplay maps the machine-readable internal setting key to a friendly
// label that we hand to YAML templates for user-facing rendering. Internal
// keys (`verbose_error`, `sudo_mode`) stay stable everywhere they're stored,
// parsed, or constrained by JSON Schema; only the rendered text changes.
var settingDisplay = map[string]string{
	"verbose_error": "Verbose Error Logging",
	"sudo_mode":     "Sudo Mode",
}

// settingLabel returns the friendly label for a known setting, falling back to
// the raw key for unknown inputs so error replies don't lose information.
func settingLabel(key string) string {
	if v, ok := settingDisplay[key]; ok {
		return v
	}
	return key
}

var _ Executor = (*SettingExecutor)(nil)
