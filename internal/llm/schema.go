package llm

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

type LLMResponse struct {
	Intent       string      `json:"intent"`
	Confidence   float64     `json:"confidence"`
	IsModeration bool        `json:"is_moderation"`
	Reasoning    string      `json:"reasoning"`
	Reply        *string     `json:"reply"`
	Targets      []Target    `json:"targets"`
	Parameters   Parameters  `json:"parameters"`
	Actions      []Action    `json:"actions"`
	AuditQuery   *AuditQuery `json:"auditQuery"`
}

type llmResponseNoTargets struct {
	Intent       string      `json:"intent"`
	Confidence   float64     `json:"confidence"`
	IsModeration bool        `json:"is_moderation"`
	Reasoning    string      `json:"reasoning"`
	Reply        *string     `json:"reply"`
	Parameters   Parameters  `json:"parameters"`
	Actions      []Action    `json:"actions"`
	AuditQuery   *AuditQuery `json:"auditQuery"`
}

func (r *LLMResponse) UnmarshalJSON(data []byte) error {
	aux := struct {
		llmResponseNoTargets
		TargetsRaw json.RawMessage `json:"targets"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Intent = aux.Intent
	r.Confidence = aux.Confidence
	r.IsModeration = aux.IsModeration
	r.Reasoning = aux.Reasoning
	r.Reply = aux.Reply
	r.Parameters = aux.Parameters
	r.Actions = aux.Actions
	r.AuditQuery = aux.AuditQuery
	r.Targets = coerceTargets(aux.TargetsRaw, aux.Intent)

	// Defensive: when the LLM emits an empty auditQuery object {} for a
	// non-audit intent (some models do this reflexively), drop it so the
	// validator's "non-audit must not have auditQuery" check doesn't reject
	// otherwise valid responses.
	if r.AuditQuery != nil && r.Intent != "audit_lookup" && isEmptyAuditQuery(r.AuditQuery) {
		r.AuditQuery = nil
	}

	return nil
}

func isEmptyAuditQuery(q *AuditQuery) bool {
	if q == nil {
		return true
	}
	return q.Action == nil && q.TargetID == nil && q.Info == ""
}

func coerceTargets(raw json.RawMessage, intent string) []Target {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var arr []Target
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}

	var single Target
	if err := json.Unmarshal(raw, &single); err == nil &&
		(single.Type != "" || single.ID != "") {
		return []Target{single}
	}

	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err == nil && len(keyed) > 0 {
		out := make([]Target, 0, len(keyed))
		for ty, rawID := range keyed {
			var id string
			if err := json.Unmarshal(rawID, &id); err == nil {
				out = append(out, Target{Type: ty, ID: id})
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	var rawStrings []string
	if err := json.Unmarshal(raw, &rawStrings); err == nil && len(rawStrings) > 0 {
		fallbackType := defaultTargetTypeForIntent(intent)
		out := make([]Target, 0, len(rawStrings))
		for _, s := range rawStrings {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if ty, id, ok := parseDiscordMention(s); ok {
				out = append(out, Target{Type: ty, ID: id})
				continue
			}
			if snowflakeRe.MatchString(s) {
				out = append(out, Target{Type: fallbackType, ID: s})
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	return nil
}

var messageTypeIntents = map[string]bool{
	"delete_message": true,
	"pin_message":    true,
	"unpin_message":  true,
	"purge_messages": true,
}

func defaultTargetTypeForIntent(intent string) string {
	if messageTypeIntents[intent] {
		return "message"
	}
	return "user"
}

var discordMentionRe = regexp.MustCompile(`^<([@#][!&]?)(\d{17,20})>$`)

func parseDiscordMention(s string) (ty, id string, ok bool) {
	match := discordMentionRe.FindStringSubmatch(s)
	if match == nil {
		return "", "", false
	}
	switch match[1] {
	case "@", "@!":
		ty = "user"
	case "@&":
		ty = "role"
	case "#", "#!":
		ty = "channel"
	default:
		return "", "", false
	}
	return ty, match[2], true
}

type Target struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Parameters struct {
	DurationSeconds *int    `json:"durationSeconds"`
	Reason          *string `json:"reason"`
	MessageCount    *int    `json:"messageCount"`
	Nickname        *string `json:"nickname"`
	Setting         *string `json:"setting"`
	Value           *string `json:"value"`
}

type Action struct {
	Intent     string     `json:"intent"`
	Targets    []Target   `json:"targets"`
	Parameters Parameters `json:"parameters"`
	Reasoning  string     `json:"reasoning"`
}

type AuditQuery struct {
	Action   *string `json:"action"`
	TargetID *string `json:"targetId"`
	Info     string  `json:"info"`
}
