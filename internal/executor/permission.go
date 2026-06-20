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

func renderReply(r *replies.Replies, category, key string, vars any) string {
	if r == nil {
		return "[" + category + "." + key + "]"
	}
	return r.Get(category, key, vars)
}

type permFn func(channelID string, guildPerms int64) bool

// PreChecker is implemented by executors that support pre-execution
// permission checks. The handler calls PreCheck BEFORE showing the
// destructive confirmation prompt, so users who lack permission get
// denied immediately instead of clicking "Yes" only to be told they
// can't. Returns "" if the actor has permission, or the denial reply
// text if not.
type PreChecker interface {
	PreCheck(ctx context.Context, action Action) string
}

func PermissionAdministrator(_ string, guildPerms int64) bool {
	return guildPerms&discordgo.PermissionAdministrator != 0
}

// gate enforces permission, snowflake validity, self-protection, hierarchy,
// and member-existence checks for a moderation intent. Pass skipUserChecks
// when the intent does not operate on a guild member (e.g. message pinning or
// message deletion). Pass skipSelfChecks only for non-destructive intents
// where the actor acting on themselves is a legitimate operation (currently
// just set_nickname / reset_nickname); for all other intents the self-block
// remains in force.
func gate(
	api MemberAPI,
	r *replies.Replies,
	permFn permFn,
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

	if !permFn(action.ChannelID, authorPerms) {
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

	// Skip hierarchy check when the actor is targeting themselves
	// (e.g. set_nickname on own nickname). You always have hierarchy
	// over yourself — without this, the check fails because
	// authorPos == targetPos (same person, same roles).
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
	r *replies.Replies,
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
