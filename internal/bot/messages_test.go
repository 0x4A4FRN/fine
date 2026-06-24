package bot

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestIsMentioned_BotInList(t *testing.T) {
	mentions := []*discordgo.User{
		{ID: "111111111111111111"},
		{ID: "222222222222222222"},
	}
	if !isMentioned(mentions, "222222222222222222") {
		t.Fatal("expected true when botID is in mentions")
	}
}

func TestIsMentioned_BotNotInList(t *testing.T) {
	mentions := []*discordgo.User{
		{ID: "111111111111111111"},
		{ID: "333333333333333333"},
	}
	if isMentioned(mentions, "222222222222222222") {
		t.Fatal("expected false when botID is not in mentions")
	}
}

func TestIsMentioned_EmptyList(t *testing.T) {
	if isMentioned(nil, "222222222222222222") {
		t.Fatal("expected false for empty mention list")
	}
	if isMentioned([]*discordgo.User{}, "222222222222222222") {
		t.Fatal("expected false for empty slice")
	}
}

func TestIsMentioned_OnlyBot(t *testing.T) {
	mentions := []*discordgo.User{{ID: "bot-id"}}
	if !isMentioned(mentions, "bot-id") {
		t.Fatal("expected true when only bot is mentioned")
	}
}

func TestIsMentioned_NilUserInList(t *testing.T) {

}

func TestStripMention_StripsBareAt(t *testing.T) {
	result := stripMention("<@222222222222222222> ban Alice", "222222222222222222")
	if result != "ban Alice" {
		t.Fatalf("expected 'ban Alice', got %q", result)
	}
}

func TestStripMention_StripsNicknameAt(t *testing.T) {
	result := stripMention("<@!222222222222222222> kick Bob", "222222222222222222")
	if result != "kick Bob" {
		t.Fatalf("expected 'kick Bob', got %q", result)
	}
}

func TestStripMention_BothFormsSameMessage(t *testing.T) {
	content := "<@123> hello <@!123>"
	result := stripMention(content, "123")
	if result != "hello" {
		t.Fatalf("expected 'hello', got %q", result)
	}
}

func TestStripMention_TrimsSurroundingWhitespace(t *testing.T) {
	result := stripMention("  <@bot>   ban user  ", "bot")
	if result != "ban user" {
		t.Fatalf("expected 'ban user', got %q", result)
	}
}

func TestStripMention_UnrelatedMentionUntouched(t *testing.T) {

	result := stripMention("<@other-user> hello", "bot-id")
	if result != "<@other-user> hello" {
		t.Fatalf("expected original content, got %q", result)
	}
}

func TestStripMention_EmptyContent(t *testing.T) {
	result := stripMention("", "bot-id")
	if result != "" {
		t.Fatalf("expected empty string, got %q", result)
	}
}

func TestStripMention_ContentWithNoMention(t *testing.T) {
	result := stripMention("just a message", "bot-id")
	if result != "just a message" {
		t.Fatalf("expected unchanged content, got %q", result)
	}
}

func TestStripMention_MultipleMentions(t *testing.T) {

	content := "<@bot> <@bot> please help"
	result := stripMention(content, "bot")
	if result != "please help" {
		t.Fatalf("expected 'please help', got %q", result)
	}
}
