package llm

import (
	"encoding/json"
	"testing"
)

func TestCoerceTargets_Empty(t *testing.T) {
	if coerceTargets(nil, "ban") != nil {
		t.Fatal("expected nil for empty input")
	}
	if coerceTargets(json.RawMessage("null"), "ban") != nil {
		t.Fatal("expected nil for null input")
	}
}

func TestCoerceTargets_Array(t *testing.T) {
	raw := json.RawMessage(`[{"id":"123456789012345678","type":"user"},{"id":"987654321098765432","type":"user"}]`)
	result := coerceTargets(raw, "ban")
	if len(result) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(result))
	}
	if result[0].ID != "123456789012345678" || result[0].Type != "user" {
		t.Errorf("target[0] = %+v", result[0])
	}
}

func TestCoerceTargets_SingleObject(t *testing.T) {
	raw := json.RawMessage(`{"id":"123456789012345678","type":"user"}`)
	result := coerceTargets(raw, "ban")
	if len(result) != 1 {
		t.Fatalf("expected 1 target, got %d", len(result))
	}
	if result[0].ID != "123456789012345678" || result[0].Type != "user" {
		t.Errorf("target = %+v", result[0])
	}
}

func TestCoerceTargets_KeyedMap(t *testing.T) {
	raw := json.RawMessage(`{"user":"123456789012345678","role":"987654321098765432"}`)
	result := coerceTargets(raw, "add_role")
	if len(result) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(result))
	}
	found := map[string]string{}
	for _, t := range result {
		found[t.Type] = t.ID
	}
	if found["user"] != "123456789012345678" {
		t.Errorf("user = %s", found["user"])
	}
	if found["role"] != "987654321098765432" {
		t.Errorf("role = %s", found["role"])
	}
}

func TestCoerceTargets_RawStrings_Mentions(t *testing.T) {
	raw := json.RawMessage(`["<@123456789012345678>","<@&987654321098765432>"]`)
	result := coerceTargets(raw, "ban")
	if len(result) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(result))
	}
	types := map[string]bool{}
	for _, t := range result {
		types[t.Type] = true
	}
	if !types["user"] || !types["role"] {
		t.Errorf("expected user+role types, got %v", types)
	}
}

func TestCoerceTargets_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not json`)
	if coerceTargets(raw, "ban") != nil {
		t.Fatal("expected nil for invalid JSON")
	}
}

func TestIsValidSnowflake(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"123456789012345678", true},
		{"123456789012345", false},
		{"123456789012345678901", false},
		{"abc", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsValidSnowflake(tt.input); got != tt.want {
			t.Errorf("IsValidSnowflake(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseDiscordMention(t *testing.T) {
	ty, id, ok := parseDiscordMention("<@123456789012345678>")
	if !ok || ty != "user" || id != "123456789012345678" {
		t.Errorf("user mention: ty=%s id=%s ok=%v", ty, id, ok)
	}
	ty, id, ok = parseDiscordMention("<@&987654321098765432>")
	if !ok || ty != "role" || id != "987654321098765432" {
		t.Errorf("role mention: ty=%s id=%s ok=%v", ty, id, ok)
	}
	_, _, ok = parseDiscordMention("not a mention")
	if ok {
		t.Error("expected ok=false for non-mention")
	}
}

func TestDefaultTargetTypeForIntent(t *testing.T) {
	if got := defaultTargetTypeForIntent("delete_message"); got != "message" {
		t.Errorf("delete_message: got %q", got)
	}
	if got := defaultTargetTypeForIntent("ban"); got != "user" {
		t.Errorf("ban: got %q", got)
	}
}
