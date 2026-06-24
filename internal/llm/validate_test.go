package llm

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func ptr[T any](v T) *T { return &v }

func validUserTarget() Target {
	return Target{Type: "user", ID: "111111111111111111"}
}

func validRoleTarget() Target {
	return Target{Type: "role", ID: "222222222222222222"}
}

func validMessageTarget() Target {
	return Target{Type: "message", ID: "333333333333333333"}
}

func TestValidateLLMResponse_Nil(t *testing.T) {
	if err := ValidateLLMResponse(nil, nil); err == nil {
		t.Fatal("expected error for nil response")
	}
}

func TestValidateLLMResponse_EmptyIntent_IsOK(t *testing.T) {
	resp := &LLMResponse{}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error for empty intent: %v", err)
	}
}

func TestValidateLLMResponse_UnknownIntent_ClearedToConversational(t *testing.T) {
	resp := &LLMResponse{Intent: "obliterate_server"}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Intent != "" {
		t.Fatalf("expected intent cleared to \"\", got %q", resp.Intent)
	}
}

func TestValidateLLMResponse_ConfidenceOutOfRange(t *testing.T) {
	tests := []struct {
		name       string
		confidence float64
	}{
		{"negative", -0.1},
		{"above_one", 1.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &LLMResponse{
				Intent:     "ban",
				Confidence: tt.confidence,
				Targets:    []Target{validUserTarget()},
			}
			if err := ValidateLLMResponse(resp, nil); err == nil {
				t.Fatalf("expected error for confidence=%f", tt.confidence)
			}
		})
	}
}

func TestValidateLLMResponse_ConfidenceBoundaries_Valid(t *testing.T) {
	for _, c := range []float64{0.0, 0.5, 1.0} {
		resp := &LLMResponse{
			Intent:     "ban",
			Confidence: c,
			Targets:    []Target{validUserTarget()},
		}
		if err := ValidateLLMResponse(resp, nil); err != nil {
			t.Errorf("unexpected error for confidence=%f: %v", c, err)
		}
	}
}

func TestValidateLLMResponse_UtilityIntent_TargetsForbidden(t *testing.T) {
	for _, intent := range []string{"ping", "help", "info", "status"} {
		t.Run(intent, func(t *testing.T) {
			resp := &LLMResponse{
				Intent:     intent,
				Confidence: 0.9,
				Targets:    []Target{validUserTarget()},
			}
			if err := ValidateLLMResponse(resp, nil); err == nil {
				t.Fatalf("expected error: utility intent %q must have 0 targets", intent)
			}
		})
	}
}

func TestValidateLLMResponse_UtilityIntent_ZeroTargets_OK(t *testing.T) {
	for _, intent := range []string{"ping", "help", "info", "status"} {
		t.Run(intent, func(t *testing.T) {
			resp := &LLMResponse{Intent: intent, Confidence: 0.9}
			if err := ValidateLLMResponse(resp, nil); err != nil {
				t.Fatalf("unexpected error for %q with 0 targets: %v", intent, err)
			}
		})
	}
}

func TestValidateLLMResponse_BanRequiresExactlyOneTarget(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
		wantErr bool
	}{
		{"zero_targets", nil, true},
		{"one_target", []Target{validUserTarget()}, false},
		{"two_targets", []Target{validUserTarget(), validUserTarget()}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &LLMResponse{Intent: "ban", Confidence: 0.9, Targets: tt.targets}
			err := ValidateLLMResponse(resp, nil)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateLLMResponse_PurgeFiltersInvalidTargets(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "purge_messages",
		Confidence: 0.9,
		Targets: []Target{
			{Type: "message", ID: "111111111111111111"},
			{Type: "alien", ID: "222222222222222222"},
			{Type: "user", ID: "not-a-snowflake"},
		},
	}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Targets) != 1 {
		t.Fatalf("expected 1 valid target after filter, got %d: %+v", len(resp.Targets), resp.Targets)
	}
}

func TestValidateLLMResponse_PurgeTooManyTargets(t *testing.T) {
	targets := make([]Target, MaxTargets+1)
	for i := range targets {
		targets[i] = validMessageTarget()
	}
	resp := &LLMResponse{Intent: "purge_messages", Confidence: 0.9, Targets: targets}
	if err := ValidateLLMResponse(resp, nil); err == nil {
		t.Fatalf("expected error for %d purge targets (max %d)", len(targets), MaxTargets)
	}
}

func TestValidateLLMResponse_RoleIntent(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
		wantErr bool
	}{
		{"one_user_one_role", []Target{validUserTarget(), validRoleTarget()}, false},
		{"two_users_no_role", []Target{validUserTarget(), validUserTarget()}, true},
		{"only_one_target", []Target{validUserTarget()}, true},
		{"zero_targets", nil, true},
	}
	for _, intent := range []string{"add_role", "remove_role"} {
		for _, tt := range tests {
			name := intent + "/" + tt.name
			t.Run(name, func(t *testing.T) {
				resp := &LLMResponse{Intent: intent, Confidence: 0.9, Targets: tt.targets}
				err := ValidateLLMResponse(resp, nil)
				if tt.wantErr && err == nil {
					t.Fatalf("expected error for %s with %d targets", intent, len(tt.targets))
				}
				if !tt.wantErr && err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			})
		}
	}
}

func TestValidateLLMResponse_NoTargetIntents_ZeroTargets_OK(t *testing.T) {
	for _, intent := range []string{"toggle_setting", "set_nickname", "reset_nickname", "snipe"} {
		t.Run(intent, func(t *testing.T) {
			resp := &LLMResponse{Intent: intent, Confidence: 0.9}
			if err := ValidateLLMResponse(resp, nil); err != nil {
				t.Fatalf("unexpected error for %q with 0 targets: %v", intent, err)
			}
		})
	}
}

func TestValidateLLMResponse_AuditLookup_WithTargets_Rejected(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "audit_lookup",
		Confidence: 0.9,
		Targets:    []Target{validUserTarget()},
		AuditQuery: &AuditQuery{Info: "actor", Action: ptr("ban")},
	}
	if err := ValidateLLMResponse(resp, nil); err == nil {
		t.Fatal("expected error: audit_lookup must have no targets")
	}
}

func TestValidateLLMResponse_AuditLookup_BrokenQuery_DemotedToChat(t *testing.T) {

	resp := &LLMResponse{
		Intent:     "audit_lookup",
		Confidence: 0.9,
		AuditQuery: &AuditQuery{Info: ""},
	}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Intent != "" {
		t.Fatalf("expected intent demoted to \"\", got %q", resp.Intent)
	}
	if resp.AuditQuery != nil {
		t.Fatal("expected AuditQuery cleared on demotion")
	}
}

func TestValidateLLMResponse_AuditLookup_ValidWithAction(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "audit_lookup",
		Confidence: 0.9,
		AuditQuery: &AuditQuery{Info: "actor", Action: ptr("ban")},
	}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateLLMResponse_AuditLookup_ValidWithTargetID(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "audit_lookup",
		Confidence: 0.9,
		AuditQuery: &AuditQuery{Info: "reason", TargetID: ptr("111111111111111111")},
	}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateLLMResponse_NonAudit_WithAuditQuery_Rejected(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "ban",
		Confidence: 0.9,
		Targets:    []Target{validUserTarget()},
		AuditQuery: &AuditQuery{Info: "actor"},
	}
	if err := ValidateLLMResponse(resp, nil); err == nil {
		t.Fatal("expected error: non-audit intent must not have auditQuery")
	}
}

func TestValidateLLMResponse_Actions_InvalidIntent_Rejected(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "ban",
		Confidence: 0.9,
		Targets:    []Target{validUserTarget()},
		Actions: []Action{
			{Intent: "destroy_everything", Targets: []Target{validUserTarget()}},
		},
	}
	if err := ValidateLLMResponse(resp, nil); err == nil {
		t.Fatal("expected error for action with invalid intent")
	}
}

func TestValidateLLMResponse_Actions_ValidIntent_OK(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "ban",
		Confidence: 0.9,
		Targets:    []Target{validUserTarget()},
		Actions: []Action{
			{Intent: "kick", Targets: []Target{validUserTarget()}},
		},
	}
	if err := ValidateLLMResponse(resp, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateLLMResponse_InvalidTargetType(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "ban",
		Confidence: 0.9,
		Targets:    []Target{{Type: "planet", ID: "111111111111111111"}},
	}
	if err := ValidateLLMResponse(resp, nil); err == nil {
		t.Fatal("expected error for invalid target type")
	}
}

func TestValidateLLMResponse_InvalidTargetSnowflake(t *testing.T) {
	resp := &LLMResponse{
		Intent:     "ban",
		Confidence: 0.9,
		Targets:    []Target{{Type: "user", ID: "not-a-snowflake"}},
	}
	if err := ValidateLLMResponse(resp, nil); err == nil {
		t.Fatal("expected error for non-snowflake target ID")
	}
}

func TestValidateParameters_NilFields_OK(t *testing.T) {
	if err := validateParameters(Parameters{}); err != nil {
		t.Fatalf("unexpected error for empty parameters: %v", err)
	}
}

func TestValidateParameters_DurationRange(t *testing.T) {
	tests := []struct {
		name    string
		secs    int
		wantErr bool
	}{
		{"below_min", DurationSecondsMin - 1, true},
		{"at_min", DurationSecondsMin, false},
		{"midpoint", 3600, false},
		{"at_max", DurationSecondsMax, false},
		{"above_max", DurationSecondsMax + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParameters(Parameters{DurationSeconds: ptr(tt.secs)})
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for durationSeconds=%d", tt.secs)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for durationSeconds=%d: %v", tt.secs, err)
			}
		})
	}
}

func TestValidateParameters_ReasonLength(t *testing.T) {
	tooLong := strings.Repeat("x", 501)
	atLimit := strings.Repeat("x", 500)

	if err := validateParameters(Parameters{Reason: &tooLong}); err == nil {
		t.Fatal("expected error for reason > 500 chars")
	}
	if err := validateParameters(Parameters{Reason: &atLimit}); err != nil {
		t.Fatalf("unexpected error for reason = 500 chars: %v", err)
	}
}

func TestValidateParameters_MessageCountBelowOne(t *testing.T) {
	if err := validateParameters(Parameters{MessageCount: ptr(0)}); err == nil {
		t.Fatal("expected error for messageCount=0")
	}
	if err := validateParameters(Parameters{MessageCount: ptr(-1)}); err == nil {
		t.Fatal("expected error for messageCount=-1")
	}
	if err := validateParameters(Parameters{MessageCount: ptr(1)}); err != nil {
		t.Fatalf("unexpected error for messageCount=1: %v", err)
	}
}

func TestIsBrokenAuditLookup_Nil(t *testing.T) {
	if !isBrokenAuditLookup(nil) {
		t.Fatal("expected true for nil query")
	}
}

func TestIsBrokenAuditLookup_EmptyInfo(t *testing.T) {
	if !isBrokenAuditLookup(&AuditQuery{Info: ""}) {
		t.Fatal("expected true for empty info")
	}
}

func TestIsBrokenAuditLookup_InvalidInfo(t *testing.T) {
	if !isBrokenAuditLookup(&AuditQuery{Info: "something_weird"}) {
		t.Fatal("expected true for unknown info value")
	}
}

func TestIsBrokenAuditLookup_ValidAction_NotBroken(t *testing.T) {
	q := &AuditQuery{Info: "actor", Action: ptr("ban")}
	if isBrokenAuditLookup(q) {
		t.Fatal("expected false: valid info + known action")
	}
}

func TestIsBrokenAuditLookup_ValidTargetID_NotBroken(t *testing.T) {
	q := &AuditQuery{Info: "reason", TargetID: ptr("111111111111111111")}
	if isBrokenAuditLookup(q) {
		t.Fatal("expected false: valid info + valid targetId")
	}
}

func TestIsBrokenAuditLookup_ValidInfoNoActionNoTarget_IsBroken(t *testing.T) {
	q := &AuditQuery{Info: "details"}
	if !isBrokenAuditLookup(q) {
		t.Fatal("expected true: valid info but neither action nor targetId")
	}
}

func TestIsBrokenAuditLookup_EmptyAction_IsBroken(t *testing.T) {
	q := &AuditQuery{Info: "actor", Action: ptr("")}
	if !isBrokenAuditLookup(q) {
		t.Fatal("expected true: empty action string is unusable")
	}
}

func TestIsBrokenAuditLookup_InvalidTargetID_IsBroken(t *testing.T) {
	q := &AuditQuery{Info: "actor", TargetID: ptr("not-a-snowflake")}
	if !isBrokenAuditLookup(q) {
		t.Fatal("expected true: invalid snowflake target ID")
	}
}

func TestIsBrokenAuditLookup_UnknownAction_IsBroken(t *testing.T) {

	q := &AuditQuery{Info: "actor", Action: ptr("unknown_intent")}
	if !isBrokenAuditLookup(q) {
		t.Fatal("expected true: unknown action")
	}
}

func TestValidateAuditQuery_NilAction_OK(t *testing.T) {
	q := &AuditQuery{Info: "actor"}
	if err := validateAuditQuery(q, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAuditQuery_AliasNormalized(t *testing.T) {

	log := zap.NewNop()
	tests := []struct {
		alias    string
		expected string
	}{
		{"delete", "delete_message"},
		{"purge", "purge_messages"},
		{"add", "add_role"},
		{"remove", "remove_role"},
		{"nick", "set_nickname"},
		{"pin", "pin_message"},
		{"unpin", "unpin_message"},
	}
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			action := tt.alias
			q := &AuditQuery{Info: "actor", Action: &action}
			if err := validateAuditQuery(q, log); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *q.Action != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, *q.Action)
			}
		})
	}
}

func TestValidateAuditQuery_UnknownAction_Rejected(t *testing.T) {
	action := "blow_up"
	q := &AuditQuery{Info: "actor", Action: &action}
	if err := validateAuditQuery(q, nil); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestValidateAuditQuery_ValidTargetID(t *testing.T) {
	id := "111111111111111111"
	q := &AuditQuery{Info: "reason", TargetID: &id}
	if err := validateAuditQuery(q, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAuditQuery_InvalidTargetID_Rejected(t *testing.T) {
	id := "not-a-snowflake"
	q := &AuditQuery{Info: "actor", TargetID: &id}
	if err := validateAuditQuery(q, nil); err == nil {
		t.Fatal("expected error for invalid targetId snowflake")
	}
}

func TestValidateAuditQuery_InvalidInfo_Rejected(t *testing.T) {
	q := &AuditQuery{Info: "unknown_field"}
	if err := validateAuditQuery(q, nil); err == nil {
		t.Fatal("expected error for invalid info value")
	}
}

func TestFilterValidTargets_DropsBadType(t *testing.T) {
	targets := []Target{
		{Type: "user", ID: "111111111111111111"},
		{Type: "alien", ID: "222222222222222222"},
	}
	out := filterValidTargets(targets, zap.NewNop())
	if len(out) != 1 || out[0].Type != "user" {
		t.Fatalf("expected 1 user target, got %+v", out)
	}
}

func TestFilterValidTargets_DropsNonSnowflake(t *testing.T) {
	targets := []Target{
		{Type: "user", ID: "not-a-snowflake"},
		{Type: "role", ID: "222222222222222222"},
	}
	out := filterValidTargets(targets, zap.NewNop())
	if len(out) != 1 || out[0].Type != "role" {
		t.Fatalf("expected 1 role target, got %+v", out)
	}
}

func TestFilterValidTargets_AllValid(t *testing.T) {
	targets := []Target{
		validUserTarget(),
		validRoleTarget(),
		validMessageTarget(),
	}

	out := filterValidTargets(targets, zap.NewNop())
	if len(out) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(out))
	}
}

func TestFilterValidTargets_AllInvalid_ReturnsEmpty(t *testing.T) {
	targets := []Target{
		{Type: "bad", ID: "bad"},
	}
	out := filterValidTargets(targets, zap.NewNop())
	if len(out) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(out))
	}
}

func TestFilterValidTargets_NilInput(t *testing.T) {
	out := filterValidTargets(nil, zap.NewNop())
	if len(out) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(out))
	}
}

func TestNormalizeAuditAction_CanonicalPassthrough(t *testing.T) {
	if got := normalizeAuditAction("ban"); got != "ban" {
		t.Fatalf("expected 'ban', got %q", got)
	}
}

func TestNormalizeAuditAction_Alias(t *testing.T) {
	if got := normalizeAuditAction("delete"); got != "delete_message" {
		t.Fatalf("expected 'delete_message', got %q", got)
	}
}

func TestNormalizeAuditAction_TrimAndLower(t *testing.T) {
	if got := normalizeAuditAction("  BAN  "); got != "ban" {
		t.Fatalf("expected 'ban', got %q", got)
	}
}
