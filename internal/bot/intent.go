package bot

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/llm"
)

var utilityIntents = map[string]bool{
	"ping":   true,
	"help":   true,
	"info":   true,
	"status": true,
	"snipe":  true,
}

var snipeCountRe = regexp.MustCompile(`(?i)^snipe\s+(\d+)$`)

func matchBareUtilityCommand(cleaned string) string {
	s := strings.ToLower(strings.TrimSpace(cleaned))
	switch s {
	case "ping":
		return "ping"
	case "help":
		return "help"
	case "info", "information":
		return "info"
	case "status", "health", "stats":
		return "status"
	case "snipe":
		return "snipe"
	}

	if snipeCountRe.MatchString(s) {
		return "snipe"
	}
	return ""
}

func parseSnipeCount(cleaned string) int {
	m := snipeCountRe.FindStringSubmatch(strings.TrimSpace(cleaned))
	if len(m) < 2 {
		return 1
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 {
		n = 1
	}
	if n > 25 {
		n = 25
	}
	return n
}
func isModerationTierIntent(intent string) bool {
	if destructiveIntents[intent] {
		return true
	}
	switch intent {
	case "pin_message", "unpin_message", "delete_message",
		"set_nickname", "reset_nickname":
		return true
	}
	return false
}
func applyModerationOverride(resp *llm.LLMResponse) bool {
	if resp == nil {
		return false
	}
	if isModerationTierIntent(resp.Intent) && !resp.IsModeration {
		resp.IsModeration = true
		return true
	}
	return false
}
func needsMessageReplyFixup(intent string) bool {
	switch intent {
	case "pin_message", "unpin_message", "delete_message":
		return true
	}
	return false
}

var implicitDeleteTriggers = []string{
	"delete",
	"remove",
	"trash",
	"wipe",
	"erase",
}

func isImplicitDeleteText(cleaned string) bool {
	s := strings.ToLower(strings.TrimSpace(cleaned))
	s = strings.TrimRight(s, ".!?,;:")
	if s == "" {
		return false
	}
	if slices.Contains(implicitDeleteTriggers, s) {
		return true
	}
	return false
}
func patchMessageTargetsFromReply(
	targets []llm.Target,
	replyID string,
) []llm.Target {
	if replyID == "" {
		return targets
	}
	out := make([]llm.Target, len(targets))
	fixed := false
	for i, t := range targets {
		if !fixed && t.Type == "message" && !llm.IsValidSnowflake(t.ID) {
			out[i] = llm.Target{Type: "message", ID: replyID}
			fixed = true
			continue
		}
		out[i] = t
	}
	return out
}
func extractReplyTargetID(m *discordgo.MessageCreate) string {
	if m == nil || m.MessageReference == nil {
		return ""
	}
	id := m.MessageReference.MessageID
	if id == "" {
		return ""
	}
	if !llm.IsValidSnowflake(id) {
		return ""
	}
	return id
}
func isVoiceClassIntent(intent string) bool {
	switch intent {
	case "mute", "unmute", "deafen", "undeafen":
		return true
	}
	return false
}
func (h *Handler) voiceClassPreCheck(
	resp *llm.LLMResponse,
	m *discordgo.MessageCreate,
) string {
	if !isVoiceClassIntent(resp.Intent) {
		return ""
	}
	if h.discord == nil {
		return ""
	}
	userID := firstTargetByType(resp.Targets, "user")
	if userID == "" {
		return ""
	}
	vs, err := h.discord.GuildMemberVoiceState(m.GuildID, userID)
	if err != nil {
		if errors.Is(err, discordgo.ErrStateNotFound) {
			h.logger.Debug("handler: target user has no voice state (never connected); treating as not-in-voice",
				zap.String("user_id", userID),
			)
		} else {
			h.logger.Warn("handler: voice state lookup error; treating as not-in-voice",
				zap.String("user_id", userID),
				zap.Error(err),
			)
		}
	}
	if vs != nil && vs.ChannelID != "" {
		return ""
	}
	if h.replies == nil {
		return fmt.Sprintf("<@%s> isn't connected to voice.", userID)
	}
	return h.replies.Get(resp.Intent, "not_in_voice", map[string]string{
		"user_name": "<@" + userID + ">",
	})
}
