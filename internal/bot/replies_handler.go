package bot

import (
	"fmt"
	"strconv"
	"time"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
)

func defaultSuccessText(intent string) string {
	switch intent {
	case "pin_message":
		return "Pinned."
	case "unpin_message":
		return "Unpinned."
	case "delete_message":
		return "Message deleted."
	case "ban":
		return "Banned."
	case "unban":
		return "Unbanned."
	case "kick":
		return "Kicked."
	case "mute":
		return "Muted."
	case "unmute":
		return "Unmuted."
	case "deafen":
		return "Deafened."
	case "undeafen":
		return "Undeafened."
	case "timeout":
		return "Timed out."
	case "untimeout":
		return "Timeout removed."
	case "set_nickname":
		return "Nickname updated."
	case "reset_nickname":
		return "Nickname reset."
	case "add_role":
		return "Role added."
	case "remove_role":
		return "Role removed."
	case "purge_messages":
		return "Purged."
	}
	return "Done."
}

var successReplyKey = map[string]struct {
	category string
	key      string
}{
	"pin_message":    {"pin", "success"},
	"unpin_message":  {"unpin", "success"},
	"delete_message": {"delete", "success"},
	"ban":            {"ban", "success"},
	"unban":          {"unban", "success"},
	"kick":           {"kick", "success"},
	"timeout":        {"timeout", "success"},
	"untimeout":      {"untimeout", "success"},
	"mute":           {"mute", "success"},
	"unmute":         {"unmute", "success"},
	"deafen":         {"deafen", "success"},
	"undeafen":       {"undeafen", "success"},
	"set_nickname":   {"nickname", "set_success"},
	"reset_nickname": {"nickname", "reset_success"},
	"add_role":       {"role", "add_success"},
	"remove_role":    {"role", "remove_success"},
	"purge_messages": {"purge", "success"},
}

func (h *Handler) renderDefaultSuccess(
	intent string,
	resp *llm.LLMResponse,
) string {
	if h.replies == nil {
		return defaultSuccessText(intent)
	}
	loc, ok := successReplyKey[intent]
	if !ok {

		return defaultSuccessText(intent)
	}
	vars := successVars(intent, resp)
	return h.replies.Get(loc.category, loc.key, vars)
}
func successVars(intent string, resp *llm.LLMResponse) map[string]string {
	vars := map[string]string{}
	if resp == nil {
		return vars
	}
	if u := firstTargetByType(resp.Targets, "user"); u != "" {
		vars["user_name"] = "<@" + u + ">"
	}
	if r := firstTargetByType(resp.Targets, "role"); r != "" {
		vars["role"] = "<@&" + r + ">"
	}
	if intent == "set_nickname" && resp.Parameters.Nickname != nil {
		vars["nick"] = *resp.Parameters.Nickname
	}
	if intent == "timeout" && resp.Parameters.DurationSeconds != nil {
		end := time.Now().Add(
			time.Duration(*resp.Parameters.DurationSeconds) * time.Second,
		)
		vars["end_timestamp"] = strconv.FormatInt(end.Unix(), 10)
	}
	return vars
}

const fallbackFailureText = "I couldn't complete that."
const verboseDebugPrefix = "Debug: "

func (h *Handler) failReplyText(
	intent string, resp *llm.LLMResponse, err error, verbose bool,
) string {
	if h.replies == nil || !h.replies.Has(intent, "failed") {
		text := fallbackFailureText
		if verbose {
			text += "\n" + verboseDebugPrefix + err.Error()
		}
		return text
	}
	vars := successVars(intent, resp)
	text := h.replies.Get(intent, "failed", vars)
	if verbose {
		text += "\n" + verboseDebugPrefix + err.Error()
	}
	return text
}
func firstTargetByType(targets []llm.Target, ty string) string {
	for _, t := range targets {
		if t.Type == ty {
			return t.ID
		}
	}
	return ""
}
func renderAuditReplyFallback(templateName string, data audit.TemplateData) string {
	switch templateName {
	case "audit.no_record":
		return "I don't have a record of that."
	case "audit.actor_with_target":
		return fmt.Sprintf("Last time <@%s> was %s, it was <@%s>.",
			data.TargetID, data.PastTenseIntent, data.ModeratorID)
	case "audit.actor_no_target":
		return fmt.Sprintf("The last %s was <@%s> by <@%s>.",
			data.IntentNoun, data.TargetID, data.ModeratorID)
	case "audit.reason_with_reason":
		return fmt.Sprintf("Because <@%s> said they were %s.",
			data.ModeratorID, data.Reason)
	case "audit.reason_without_reason":
		return fmt.Sprintf("I don't know. <@%s> didn't say why.", data.ModeratorID)
	case "audit.details_with_reason":
		return fmt.Sprintf("<@%s> was %s by <@%s> for %s (%s).",
			data.TargetID, data.PastTenseIntent, data.ModeratorID,
			data.Reason, data.RelativeTime)
	case "audit.details_without_reason":
		return fmt.Sprintf("<@%s> was %s by <@%s> (%s). They didn't say why.",
			data.TargetID, data.PastTenseIntent, data.ModeratorID,
			data.RelativeTime)
	default:
		return "I don't have a record of that."
	}
}
func (h *Handler) cloudyReplyText() string {
	if h.replies == nil {
		return "I had a hiccup processing that. Try again in a moment."
	}
	return h.replies.Get("handler", "cloudy", nil)
}
func (h *Handler) negationReplyText() string {
	if h.replies == nil {
		return negationReplyText
	}
	return h.replies.Get("audit", "negation_override", nil)
}
func (h *Handler) cancelReplyText() string {
	if h.replies == nil {
		return confirmReplyText
	}
	return h.replies.Get("audit", "cancelled", nil)
}

const negationReplyText = "I think you said you don't want me to do this, so I won't."
