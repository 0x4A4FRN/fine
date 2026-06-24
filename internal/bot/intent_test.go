package bot

import (
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/llm"
)

func TestMatchBareUtilityCommand_Canonical(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ping", "ping"},
		{"help", "help"},
		{"info", "info"},
		{"information", "info"},
		{"status", "status"},
		{"health", "status"},
		{"stats", "status"},
		{"snipe", "snipe"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := matchBareUtilityCommand(tt.input); got != tt.want {
				t.Fatalf("matchBareUtilityCommand(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMatchBareUtilityCommand_CaseInsensitive(t *testing.T) {
	cases := []string{"PING", "Ping", "HELP", "INFO", "STATUS", "SNIPE"}
	wants := []string{"ping", "ping", "help", "info", "status", "snipe"}
	for i, c := range cases {
		if got := matchBareUtilityCommand(c); got != wants[i] {
			t.Errorf("matchBareUtilityCommand(%q) = %q, want %q", c, got, wants[i])
		}
	}
}

func TestMatchBareUtilityCommand_LeadingTrailingSpaces(t *testing.T) {
	if got := matchBareUtilityCommand("  ping  "); got != "ping" {
		t.Fatalf("expected 'ping', got %q", got)
	}
}

func TestMatchBareUtilityCommand_SnipeWithCount(t *testing.T) {
	inputs := []string{"snipe 1", "snipe 10", "snipe 25", "SNIPE 5"}
	for _, in := range inputs {
		if got := matchBareUtilityCommand(in); got != "snipe" {
			t.Errorf("matchBareUtilityCommand(%q) = %q, want 'snipe'", in, got)
		}
	}
}

func TestMatchBareUtilityCommand_Unknown(t *testing.T) {

	cases := []string{"ban", "kick", "", "snipe abc", "snipe 1 extra", "helpp", "pingping"}
	for _, c := range cases {
		if got := matchBareUtilityCommand(c); got != "" {
			t.Errorf("matchBareUtilityCommand(%q) = %q, want ''", c, got)
		}
	}
}

func TestParseSnipeCount_BareSnipe_ReturnsOne(t *testing.T) {
	if got := parseSnipeCount("snipe"); got != 1 {
		t.Fatalf("expected 1 for bare 'snipe', got %d", got)
	}
}

func TestParseSnipeCount_WithCount(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"snipe 1", 1},
		{"snipe 5", 5},
		{"snipe 25", 25},
		{"snipe 10", 10},
	}
	for _, tt := range tests {
		if got := parseSnipeCount(tt.input); got != tt.want {
			t.Errorf("parseSnipeCount(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseSnipeCount_ClampedToMax(t *testing.T) {
	if got := parseSnipeCount("snipe 30"); got != 25 {
		t.Fatalf("expected clamped to 25, got %d", got)
	}
	if got := parseSnipeCount("snipe 100"); got != 25 {
		t.Fatalf("expected clamped to 25, got %d", got)
	}
}

func TestParseSnipeCount_ClampedToMin(t *testing.T) {

	if got := parseSnipeCount("snipe 0"); got != 1 {
		t.Fatalf("expected 1 for count=0, got %d", got)
	}
}

func TestParseSnipeCount_NoMatchReturnsOne(t *testing.T) {
	for _, s := range []string{"", "ping", "ban alice"} {
		if got := parseSnipeCount(s); got != 1 {
			t.Errorf("parseSnipeCount(%q) = %d, want 1", s, got)
		}
	}
}

func TestIsModerationTierIntent_Destructive(t *testing.T) {
	destructive := []string{
		"ban", "unban", "kick", "timeout", "untimeout",
		"mute", "unmute", "deafen", "undeafen",
		"add_role", "remove_role", "purge_messages",
	}
	for _, intent := range destructive {
		t.Run(intent, func(t *testing.T) {
			if !isModerationTierIntent(intent) {
				t.Fatalf("expected isModerationTierIntent(%q) = true", intent)
			}
		})
	}
}

func TestIsModerationTierIntent_NonDestructiveModerationActions(t *testing.T) {
	moderation := []string{"pin_message", "unpin_message", "delete_message", "set_nickname", "reset_nickname"}
	for _, intent := range moderation {
		t.Run(intent, func(t *testing.T) {
			if !isModerationTierIntent(intent) {
				t.Fatalf("expected isModerationTierIntent(%q) = true", intent)
			}
		})
	}
}

func TestIsModerationTierIntent_UtilityIntents(t *testing.T) {
	utility := []string{"ping", "help", "info", "status", "snipe", "audit_lookup", ""}
	for _, intent := range utility {
		t.Run(intent, func(t *testing.T) {
			if isModerationTierIntent(intent) {
				t.Fatalf("expected isModerationTierIntent(%q) = false", intent)
			}
		})
	}
}

func TestApplyModerationOverride_Nil_ReturnsFalse(t *testing.T) {
	if applyModerationOverride(nil) {
		t.Fatal("expected false for nil response")
	}
}

func TestApplyModerationOverride_ModerationIntent_SetsFlag(t *testing.T) {
	resp := &llm.LLMResponse{Intent: "ban", IsModeration: false}
	if !applyModerationOverride(resp) {
		t.Fatal("expected true (flag was overridden)")
	}
	if !resp.IsModeration {
		t.Fatal("expected IsModeration to be set to true")
	}
}

func TestApplyModerationOverride_AlreadySet_NoChange(t *testing.T) {
	resp := &llm.LLMResponse{Intent: "ban", IsModeration: true}
	if applyModerationOverride(resp) {
		t.Fatal("expected false (already set, no override needed)")
	}
}

func TestApplyModerationOverride_NonModerationIntent_NoChange(t *testing.T) {
	resp := &llm.LLMResponse{Intent: "ping", IsModeration: false}
	if applyModerationOverride(resp) {
		t.Fatal("expected false for non-moderation intent")
	}
	if resp.IsModeration {
		t.Fatal("IsModeration should not have been changed")
	}
}

func TestApplyModerationOverride_DeleteMessage(t *testing.T) {

	resp := &llm.LLMResponse{Intent: "delete_message", IsModeration: false}
	if !applyModerationOverride(resp) {
		t.Fatal("expected true for delete_message")
	}
}

func TestNeedsMessageReplyFixup_True(t *testing.T) {
	for _, intent := range []string{"pin_message", "unpin_message", "delete_message"} {
		t.Run(intent, func(t *testing.T) {
			if !needsMessageReplyFixup(intent) {
				t.Fatalf("expected true for %q", intent)
			}
		})
	}
}

func TestNeedsMessageReplyFixup_False(t *testing.T) {
	for _, intent := range []string{"ban", "kick", "ping", "snipe", ""} {
		t.Run(intent, func(t *testing.T) {
			if needsMessageReplyFixup(intent) {
				t.Fatalf("expected false for %q", intent)
			}
		})
	}
}

func TestIsImplicitDeleteText_Triggers(t *testing.T) {
	cases := []string{"delete", "remove", "trash", "wipe", "erase"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if !isImplicitDeleteText(c) {
				t.Fatalf("expected true for %q", c)
			}
		})
	}
}

func TestIsImplicitDeleteText_CaseInsensitive(t *testing.T) {
	if !isImplicitDeleteText("DELETE") {
		t.Fatal("expected true for 'DELETE'")
	}
	if !isImplicitDeleteText("Remove") {
		t.Fatal("expected true for 'Remove'")
	}
}

func TestIsImplicitDeleteText_TrailingPunctuation(t *testing.T) {
	for _, s := range []string{"delete.", "remove!", "trash?", "wipe,", "erase;"} {
		if !isImplicitDeleteText(s) {
			t.Errorf("expected true for %q (trailing punct stripped)", s)
		}
	}
}

func TestIsImplicitDeleteText_False(t *testing.T) {
	cases := []string{"", "ban", "kick", "delete message", "please delete", "  "}
	for _, c := range cases {
		if isImplicitDeleteText(c) {
			t.Errorf("expected false for %q", c)
		}
	}
}

func TestPatchMessageTargetsFromReply_EmptyReplyID_Unchanged(t *testing.T) {
	targets := []llm.Target{{Type: "message", ID: "fake"}}
	got := patchMessageTargetsFromReply(targets, "")
	if len(got) != 1 || got[0].ID != "fake" {
		t.Fatalf("expected unchanged targets, got %+v", got)
	}
}

func TestPatchMessageTargetsFromReply_InvalidSnowflake_Replaced(t *testing.T) {

	targets := []llm.Target{{Type: "message", ID: "not-valid"}}
	replyID := "123456789012345678"
	got := patchMessageTargetsFromReply(targets, replyID)
	if len(got) != 1 || got[0].ID != replyID {
		t.Fatalf("expected target patched to %q, got %+v", replyID, got)
	}
}

func TestPatchMessageTargetsFromReply_ValidSnowflake_NotReplaced(t *testing.T) {
	originalID := "123456789012345678"
	targets := []llm.Target{{Type: "message", ID: originalID}}
	got := patchMessageTargetsFromReply(targets, "999999999999999999")
	if got[0].ID != originalID {
		t.Fatalf("expected valid snowflake left unchanged, got %q", got[0].ID)
	}
}

func TestPatchMessageTargetsFromReply_OnlyFirstInvalidReplaced(t *testing.T) {
	replyID := "111111111111111111"
	targets := []llm.Target{
		{Type: "message", ID: "bad-1"},
		{Type: "message", ID: "bad-2"},
	}
	got := patchMessageTargetsFromReply(targets, replyID)
	if len(got) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(got))
	}
	if got[0].ID != replyID {
		t.Errorf("first target should be patched to %q, got %q", replyID, got[0].ID)
	}
	if got[1].ID != "bad-2" {
		t.Errorf("second target should be unchanged, got %q", got[1].ID)
	}
}

func TestPatchMessageTargetsFromReply_NonMessageTarget_Untouched(t *testing.T) {
	replyID := "111111111111111111"
	targets := []llm.Target{{Type: "user", ID: "bad"}}
	got := patchMessageTargetsFromReply(targets, replyID)
	if got[0].ID != "bad" {
		t.Fatalf("non-message target should not be patched, got %q", got[0].ID)
	}
}

func TestExtractReplyTargetID_NilMessage(t *testing.T) {
	if got := extractReplyTargetID(nil); got != "" {
		t.Fatalf("expected '' for nil message, got %q", got)
	}
}

func TestExtractReplyTargetID_NilReference(t *testing.T) {
	m := &discordgo.MessageCreate{Message: &discordgo.Message{}}
	if got := extractReplyTargetID(m); got != "" {
		t.Fatalf("expected '' for nil reference, got %q", got)
	}
}

func TestExtractReplyTargetID_EmptyMessageID(t *testing.T) {
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			MessageReference: &discordgo.MessageReference{MessageID: ""},
		},
	}
	if got := extractReplyTargetID(m); got != "" {
		t.Fatalf("expected '' for empty messageID, got %q", got)
	}
}

func TestExtractReplyTargetID_InvalidSnowflake(t *testing.T) {
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			MessageReference: &discordgo.MessageReference{MessageID: "not-a-snowflake"},
		},
	}
	if got := extractReplyTargetID(m); got != "" {
		t.Fatalf("expected '' for invalid snowflake, got %q", got)
	}
}

func TestExtractReplyTargetID_ValidSnowflake(t *testing.T) {
	const validID = "123456789012345678"
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			MessageReference: &discordgo.MessageReference{MessageID: validID},
		},
	}
	if got := extractReplyTargetID(m); got != validID {
		t.Fatalf("expected %q, got %q", validID, got)
	}
}

func TestIsVoiceClassIntent_True(t *testing.T) {
	for _, intent := range []string{"mute", "unmute", "deafen", "undeafen"} {
		if !isVoiceClassIntent(intent) {
			t.Errorf("expected true for %q", intent)
		}
	}
}

func TestIsVoiceClassIntent_False(t *testing.T) {
	for _, intent := range []string{"ban", "kick", "ping", "timeout", ""} {
		if isVoiceClassIntent(intent) {
			t.Errorf("expected false for %q", intent)
		}
	}
}
