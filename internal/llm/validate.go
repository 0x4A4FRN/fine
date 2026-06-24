package llm

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

var snowflakeRe = regexp.MustCompile(`^\d{17,20}$`)

var suggestedIntents = []string{
	"ban",
	"unban",
	"kick",
	"timeout",
	"untimeout",
	"mute",
	"unmute",
	"deafen",
	"undeafen",
	"set_nickname",
	"reset_nickname",
	"add_role",
	"remove_role",
	"pin_message",
	"unpin_message",
	"delete_message",
	"purge_messages",
	"toggle_setting",
	"audit_lookup",
	"help",
	"info",
	"ping",
	"status",
	"snipe",
}

var noTargetIntents = map[string]bool{
	"toggle_setting": true,
	"set_nickname":   true,
	"reset_nickname": true,
	"snipe":          true,
}

var utilityIntents = map[string]bool{
	"ping":   true,
	"help":   true,
	"info":   true,
	"status": true,
	"snipe":  true,
}

var validTargetTypes = []string{
	"user",
	"role",
	"message",
}

var validAuditInfoValues = []string{
	"actor",
	"reason",
	"details",
}

var auditActionAliases = map[string]string{
	"delete": "delete_message",
	"purge":  "purge_messages",
	"add":    "add_role",
	"remove": "remove_role",
	"nick":   "set_nickname",
	"pin":    "pin_message",
	"unpin":  "unpin_message",
}

func normalizeAuditAction(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if canonical, ok := auditActionAliases[s]; ok {
		return canonical
	}
	return s
}

const (
	DurationSecondsMin = 1

	DurationSecondsMax = 2419200

	MaxTargets = 2
)

func ValidateLLMResponse(resp *LLMResponse, logger *zap.Logger) error {
	if logger == nil {
		logger = zap.NewNop()
	}

	if resp == nil {
		return fmt.Errorf("llm: response is nil")
	}

	if resp.Intent == "" {
		return nil
	}
	if !slices.Contains(suggestedIntents, resp.Intent) {
		logger.Warn("llm: unknown intent coerced to conversational",
			zap.String("intent", resp.Intent),
		)
		resp.Intent = ""
		return nil
	}

	if resp.Confidence < 0.0 || resp.Confidence > 1.0 {
		return fmt.Errorf(
			"llm: confidence %f out of range [0.0, 1.0]",
			resp.Confidence,
		)
	}

	if resp.Intent == "purge_messages" {
		resp.Targets = filterValidTargets(resp.Targets, logger)
	} else if err := validateTargets(resp.Targets); err != nil {
		return fmt.Errorf("llm: targets: %w", err)
	}

	if err := validateParameters(resp.Parameters); err != nil {
		return fmt.Errorf("llm: parameters: %w", err)
	}

	if resp.Intent == "add_role" || resp.Intent == "remove_role" {
		if err := validateRoleTargets(resp.Targets); err != nil {
			return err
		}
	}

	isUtilityIntent := utilityIntents[resp.Intent]
	isRoleIntent := resp.Intent == "add_role" || resp.Intent == "remove_role"
	isPurgeIntent := resp.Intent == "purge_messages"
	isAuditLookup := resp.Intent == "audit_lookup"
	isNoTarget := noTargetIntents[resp.Intent]

	if isUtilityIntent && len(resp.Targets) > 0 {
		return fmt.Errorf(
			"llm: intent %q must have 0 targets, got %d",
			resp.Intent,
			len(resp.Targets),
		)
	}
	if !isUtilityIntent && !isPurgeIntent && !isRoleIntent && !isAuditLookup && !isNoTarget && len(resp.Targets) != 1 {
		return fmt.Errorf(
			"llm: intent %q requires exactly 1 target, got %d",
			resp.Intent,
			len(resp.Targets),
		)
	}
	if isPurgeIntent && len(resp.Targets) > MaxTargets {
		return fmt.Errorf(
			"llm: intent %q allows at most %d targets, got %d",
			resp.Intent,
			MaxTargets,
			len(resp.Targets),
		)
	}

	if resp.Intent == "audit_lookup" {
		if len(resp.Targets) > 0 {
			return fmt.Errorf(
				"llm: audit_lookup must have no targets, got %d",
				len(resp.Targets),
			)
		}
		if isBrokenAuditLookup(resp.AuditQuery) {

			logger.Info("llm: audit_lookup with broken query; demoting to chat",
				zap.Any("query", resp.AuditQuery),
			)
			resp.AuditQuery = nil
			resp.Intent = ""
		} else {
			if err := validateAuditQuery(resp.AuditQuery, logger); err != nil {
				return fmt.Errorf("llm: auditQuery: %w", err)
			}
		}
	} else {
		if resp.AuditQuery != nil {
			return fmt.Errorf(
				"llm: intent %q must not have auditQuery",
				resp.Intent,
			)
		}
	}

	for i, action := range resp.Actions {
		if err := validateAction(&action, i); err != nil {
			return err
		}
	}

	return nil
}

func validateTargets(targets []Target) error {
	if len(targets) > MaxTargets {
		return fmt.Errorf(
			"too many targets: %d (max %d)",
			len(targets), MaxTargets,
		)
	}
	for i, t := range targets {
		if !slices.Contains(validTargetTypes, t.Type) {
			return fmt.Errorf(
				"target[%d]: invalid type %q",
				i, t.Type,
			)
		}
		if !snowflakeRe.MatchString(t.ID) {
			return fmt.Errorf(
				"target[%d]: id %q is not a valid snowflake",
				i, t.ID,
			)
		}
	}
	return nil
}

func filterValidTargets(targets []Target, logger *zap.Logger) []Target {
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		if !slices.Contains(validTargetTypes, t.Type) {
			logger.Warn("llm: dropping target with unknown type",
				zap.String("type", t.Type),
				zap.String("id", t.ID),
			)
			continue
		}
		if !snowflakeRe.MatchString(t.ID) {
			logger.Warn("llm: dropping target with non-snowflake id",
				zap.String("type", t.Type),
				zap.String("id", t.ID),
			)
			continue
		}
		out = append(out, t)
	}
	return out
}

func validateRoleTargets(targets []Target) error {
	if len(targets) != 2 {
		return fmt.Errorf(
			"llm: intent requires exactly 2 targets (one user + one role), got %d",
			len(targets),
		)
	}
	hasUser := false
	hasRole := false
	for _, t := range targets {
		switch t.Type {
		case "user":
			hasUser = true
		case "role":
			hasRole = true
		}
	}
	if !hasUser || !hasRole {
		return fmt.Errorf(
			"llm: role intent requires one user and one role target, got types %v",
			targetTypes(targets),
		)
	}
	return nil
}

func validateParameters(params Parameters) error {
	if params.DurationSeconds != nil {
		val := *params.DurationSeconds
		if val < DurationSecondsMin || val > DurationSecondsMax {
			return fmt.Errorf(
				"durationSeconds %d out of range [%d, %d]",
				val,
				DurationSecondsMin,
				DurationSecondsMax,
			)
		}
	}
	if params.Reason != nil && len(*params.Reason) > 500 {
		return fmt.Errorf(
			"reason length %d exceeds 500 chars",
			len(*params.Reason),
		)
	}
	if params.MessageCount != nil && *params.MessageCount < 1 {
		return fmt.Errorf(
			"messageCount %d must be >= 1",
			*params.MessageCount,
		)
	}
	return nil
}

func isBrokenAuditLookup(q *AuditQuery) bool {
	if q == nil {
		return true
	}
	if q.Info == "" || !slices.Contains(validAuditInfoValues, q.Info) {
		return true
	}
	actionUsable := q.Action != nil && *q.Action != "" &&
		slices.Contains(suggestedIntents, *q.Action)
	if actionUsable {
		return false
	}
	targetUsable := q.TargetID != nil && *q.TargetID != "" &&
		snowflakeRe.MatchString(*q.TargetID)
	return !targetUsable
}

func validateAuditQuery(q *AuditQuery, logger *zap.Logger) error {
	if !slices.Contains(validAuditInfoValues, q.Info) {
		return fmt.Errorf("invalid info %q", q.Info)
	}
	if q.Action != nil {
		normalized := normalizeAuditAction(*q.Action)
		if normalized != *q.Action {
			logger.Info("llm: audit action filter normalized",
				zap.String("given", *q.Action),
				zap.String("canonical", normalized),
			)
			*q.Action = normalized
		}
		if !slices.Contains(suggestedIntents, *q.Action) {
			return fmt.Errorf("invalid action filter %q", *q.Action)
		}
	}
	if q.TargetID != nil && !snowflakeRe.MatchString(*q.TargetID) {
		return fmt.Errorf("invalid targetId %q", *q.TargetID)
	}
	return nil
}

func validateAction(a *Action, index int) error {
	if !slices.Contains(suggestedIntents, a.Intent) {
		return fmt.Errorf(
			"actions[%d]: invalid intent %q",
			index, a.Intent,
		)
	}
	if err := validateTargets(a.Targets); err != nil {
		return fmt.Errorf("actions[%d]: %w", index, err)
	}
	if err := validateParameters(a.Parameters); err != nil {
		return fmt.Errorf("actions[%d]: %w", index, err)
	}
	return nil
}

func targetTypes(targets []Target) []string {
	types := make([]string, 0, len(targets))
	for _, t := range targets {
		types = append(types, strconv.Quote(t.Type))
	}
	return types
}

func IsValidSnowflake(id string) bool {
	return snowflakeRe.MatchString(id)
}
