package executor

import (
	"context"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

func guildPermsForRoles(roles []*discordgo.Role, roleIDs []string) int64 {
	permsByID := make(map[string]int64, len(roles))
	for _, r := range roles {
		permsByID[r.ID] = r.Permissions
	}
	var combined int64
	for _, id := range roleIDs {
		combined |= permsByID[id]
	}
	return combined
}

func highestRolePosition(roles []*discordgo.Role, roleIDs []string) int {
	byID := make(map[string]*discordgo.Role, len(roles))
	for _, r := range roles {
		byID[r.ID] = r
	}
	best := -1
	for _, id := range roleIDs {
		if r, ok := byID[id]; ok && r.Position > best {
			best = r.Position
		}
	}
	return best
}

func renderReply(r replies.Renderer, category, key string, vars any) string {
	if r == nil {
		return "[" + category + "." + key + "]"
	}
	return r.Get(category, key, vars)
}

type PreChecker interface {
	PreCheck(ctx context.Context, action Action) string
}

func PermissionAdministrator(_ string, guildPerms int64) bool {
	return guildPerms&discordgo.PermissionAdministrator != 0
}

func gate(
	api MemberAPI,
	r replies.Renderer,
	permBits int64,
	replyCategory string,
	action Action,
	targetID string,
	skipUserChecks bool,
	skipSelfChecks bool,
	skipMemberExistence bool,
) string {
	authorMember, err := api.GuildMember(action.GuildID, action.ActorID)
	if err != nil || authorMember == nil {
		return renderReply(r, "gateway", "cannot_verify_permissions", nil)
	}

	roles, err := api.GuildRoles(action.GuildID)
	if err != nil {
		return renderReply(r, "gateway", "cannot_verify_permissions", nil)
	}
	authorPerms := guildPermsForRoles(roles, authorMember.Roles)

	if authorPerms&(permBits|discordgo.PermissionAdministrator) == 0 {
		return renderReply(r, replyCategory, "no_permission", nil)
	}

	if targetID != "" {
		if !llm.IsValidSnowflake(targetID) {
			return renderReply(r, "gateway", "invalid_user", nil)
		}
	}
	if targetID == "" || skipUserChecks {
		return ""
	}

	if !skipSelfChecks {
		if targetID == api.BotUserID() {
			return renderReply(r, "self_protection", "bot", nil)
		}
		if targetID == action.ActorID {
			return renderReply(r, replyCategory, "self_protection", nil)
		}
		g, err := api.Guild(action.GuildID)
		if err != nil || g == nil {
			return renderReply(r, "gateway", "cannot_verify_permissions", nil)
		}
		if targetID == g.OwnerID {
			return renderReply(r, "self_protection", "owner", nil)
		}
	}

	if targetID == action.ActorID {
		return ""
	}

	targetMember, targetErr := api.GuildMember(action.GuildID, targetID)
	if msg := gateCheckHierarchy(r, roles, authorMember.Roles, targetMember, api, action.GuildID); msg != "" {
		return msg
	}
	if !skipMemberExistence {
		if targetErr != nil || targetMember == nil {
			return renderReply(r, "gateway", "user_not_found", nil)
		}
	}
	return ""
}

func gateCheckHierarchy(
	r replies.Renderer,
	roles []*discordgo.Role,
	authorRoles []string,
	targetMember *discordgo.Member,
	api MemberAPI,
	guildID string,
) string {
	if guildID == "" || targetMember == nil {
		return ""
	}
	if roles == nil {
		return renderReply(r, "gateway", "cannot_verify_permissions", nil)
	}

	authorPos := highestRolePosition(roles, authorRoles)

	targetRoles := targetMember.Roles
	targetPos := highestRolePosition(roles, targetRoles)
	if targetPos >= 0 && authorPos >= 0 && authorPos <= targetPos {
		return renderReply(r, "hierarchy", "author", nil)
	}

	botID := api.BotUserID()
	if botID != "" {
		var botRoles []string
		if bm, err := api.GuildMember(guildID, botID); err == nil && bm != nil {
			botRoles = bm.Roles
		}
		botPos := highestRolePosition(roles, botRoles)
		if targetPos >= 0 && botPos >= 0 && botPos <= targetPos {
			return renderReply(r, "hierarchy", "bot", nil)
		}
	}
	return ""
}
