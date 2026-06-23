package bot

import (
	"testing"
)

// ── MatchConfirmation ──────────────────────────────────────────────────────

func TestMatchConfirmation_Affirmatives(t *testing.T) {
	affirmatives := []string{
		"y", "yes", "ok", "go", "confirm", "sure", "yeah",
		"do it", "proceed", "yep", "yup", "affirmative",
	}
	for _, word := range affirmatives {
		t.Run(word, func(t *testing.T) {
			action, matched := MatchConfirmation(word)
			if !matched {
				t.Fatalf("expected matched=true for %q", word)
			}
			if action != "yes" {
				t.Fatalf("expected action='yes', got %q", action)
			}
		})
	}
}

func TestMatchConfirmation_Negatives(t *testing.T) {
	negatives := []string{
		"n", "no", "cancel", "stop", "nope", "abort",
		"don't", "dont", "never", "no way",
	}
	for _, word := range negatives {
		t.Run(word, func(t *testing.T) {
			action, matched := MatchConfirmation(word)
			if !matched {
				t.Fatalf("expected matched=true for %q", word)
			}
			if action != "no" {
				t.Fatalf("expected action='no', got %q", action)
			}
		})
	}
}

func TestMatchConfirmation_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input  string
		action string
	}{
		{"YES", "yes"},
		{"NO", "no"},
		{"Cancel", "no"},
		{"CONFIRM", "yes"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			action, matched := MatchConfirmation(tt.input)
			if !matched {
				t.Fatalf("expected matched=true for %q", tt.input)
			}
			if action != tt.action {
				t.Fatalf("expected %q, got %q", tt.action, action)
			}
		})
	}
}

func TestMatchConfirmation_TrailingPunctuationStripped(t *testing.T) {
	tests := []struct {
		input  string
		action string
	}{
		{"yes!", "yes"},
		{"no.", "no"},
		{"confirm?", "yes"},
		{"cancel,", "no"},
		{"stop;", "no"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			action, matched := MatchConfirmation(tt.input)
			if !matched {
				t.Fatalf("expected matched=true for %q", tt.input)
			}
			if action != tt.action {
				t.Fatalf("expected %q, got %q", tt.action, action)
			}
		})
	}
}

func TestMatchConfirmation_SurroundingWhitespaceIgnored(t *testing.T) {
	action, matched := MatchConfirmation("  yes  ")
	if !matched || action != "yes" {
		t.Fatalf("expected yes/true, got %q/%v", action, matched)
	}
}

func TestMatchConfirmation_NonMatch(t *testing.T) {
	inputs := []string{
		"maybe", "perhaps", "not sure", "ban alice", "", "yess", "noo",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			action, matched := MatchConfirmation(in)
			if matched {
				t.Fatalf("expected no match for %q, got action=%q", in, action)
			}
			if action != "" {
				t.Fatalf("expected empty action on non-match, got %q", action)
			}
		})
	}
}

func TestMatchConfirmation_MultiWordNegative(t *testing.T) {
	// "no way" is a valid negative — it contains a space but must match exactly.
	action, matched := MatchConfirmation("no way")
	if !matched || action != "no" {
		t.Fatalf("expected 'no'/true, got %q/%v", action, matched)
	}
}

// ── IsDestructive ──────────────────────────────────────────────────────────

func TestIsDestructive_AllDestructiveIntents(t *testing.T) {
	destructive := []string{
		"ban", "unban", "kick",
		"timeout", "untimeout",
		"mute", "unmute",
		"deafen", "undeafen",
		"add_role", "remove_role",
		"purge_messages",
	}
	for _, intent := range destructive {
		t.Run(intent, func(t *testing.T) {
			if !IsDestructive(intent) {
				t.Fatalf("expected IsDestructive(%q) = true", intent)
			}
		})
	}
}

func TestIsDestructive_NonDestructiveIntents(t *testing.T) {
	nonDestructive := []string{
		"ping", "help", "info", "status", "snipe",
		"audit_lookup",
		"pin_message", "unpin_message", "delete_message",
		"set_nickname", "reset_nickname",
		"toggle_setting",
		"",
	}
	for _, intent := range nonDestructive {
		t.Run(intent, func(t *testing.T) {
			if IsDestructive(intent) {
				t.Fatalf("expected IsDestructive(%q) = false", intent)
			}
		})
	}
}

func TestIsDestructive_ModeratedButNotDestructive(t *testing.T) {
	// These require moderation-tier permission but don't need the destructive
	// confirmation gate — IsDestructive is false, isModerationTierIntent is true.
	for _, intent := range []string{"pin_message", "unpin_message", "delete_message"} {
		t.Run(intent, func(t *testing.T) {
			if IsDestructive(intent) {
				t.Fatalf("expected IsDestructive(%q) = false (moderation-tier, not destructive)", intent)
			}
			if !isModerationTierIntent(intent) {
				t.Fatalf("expected isModerationTierIntent(%q) = true", intent)
			}
		})
	}
}
