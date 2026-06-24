package bot

import (
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestMessageBufferPushEvolicts(t *testing.T) {
	buf := NewMessageBuffer(2)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		buf.PushEntry(bufferedMessage{
			MessageID: makeID("m", i),
			ChannelID: "c1",
			AuthorID:  "user1",
			Content:   "msg",
			Timestamp: now.Add(time.Duration(i) * time.Second),
		})
	}

	got := buf.ScanRecent("c1")
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after eviction; got %d", len(got))
	}
	if got[0].MessageID != "m3" || got[1].MessageID != "m4" {
		t.Errorf("expected [m3 m4]; got [%s %s]",
			got[0].MessageID, got[1].MessageID)
	}
}

func TestMessageBufferPushDedups(t *testing.T) {
	buf := NewMessageBuffer(5)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		buf.PushEntry(bufferedMessage{
			MessageID: "m-a",
			ChannelID: "c1",
			AuthorID:  "user1",
			Content:   "msg",
			Timestamp: now,
		})
	}

	got := buf.ScanRecent("c1")
	if len(got) != 1 {
		t.Fatalf("expected dedup-collapse to 1 entry; got %d", len(got))
	}
}

func TestMessageBufferScanForActionEmptyChannelIDScansAll(t *testing.T) {
	buf := NewMessageBuffer(15)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	target := "987654321098765432"

	buf.PushEntry(bufferedMessage{
		MessageID: "h1",
		ChannelID: "c1",
		AuthorID:  "mod1",
		Content:   "please ban <@" + target + ">",
		Timestamp: now,
	})
	buf.PushEntry(bufferedMessage{
		MessageID: "b1",
		ChannelID: "c1",
		AuthorID:  "bot1",
		IsBot:     true,
		Content:   "Banning the user.",
		Timestamp: now.Add(time.Millisecond),
	})

	got := buf.ScanForAction(
		"", target,
		[]string{"ban"},
		5*time.Second, now.Add(2*time.Second),
	)
	if got == nil {
		t.Fatalf("expected to find the human command; got nil")
	}
	if got.AuthorID != "mod1" {
		t.Errorf("expected mod1; got %q", got.AuthorID)
	}
}

func TestMessageBufferScanForActionIgnoresBotsAndExpired(t *testing.T) {
	buf := NewMessageBuffer(15)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	target := "987654321098765432"

	buf.PushEntry(bufferedMessage{
		MessageID: "old",
		ChannelID: "c1",
		AuthorID:  "mod1",
		Content:   "ban <@" + target + ">",
		Timestamp: now.Add(-30 * time.Second),
	})
	buf.PushEntry(bufferedMessage{
		MessageID: "botty",
		ChannelID: "c1",
		AuthorID:  "bot1",
		IsBot:     true,
		Content:   "ban <@" + target + ">",
		Timestamp: now,
	})
	buf.PushEntry(bufferedMessage{
		MessageID: "noise",
		ChannelID: "c1",
		AuthorID:  "mod2",
		Content:   "hello there",
		Timestamp: now,
	})

	got := buf.ScanForAction(
		"c1", target,
		[]string{"ban"},
		5*time.Second, now,
	)
	if got != nil {
		t.Fatalf("expected nil; got %+v", got)
	}
}

func TestMessageBufferScanForBotResponseAcrossChannels(t *testing.T) {
	buf := NewMessageBuffer(15)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	buf.PushEntry(bufferedMessage{
		MessageID:   "b1",
		ChannelID:   "c2",
		AuthorID:    "bot-dyno",
		IsBot:       true,
		Content:     "OK",
		Timestamp:   now,
		Interaction: &discordgo.MessageInteraction{User: &discordgo.User{ID: "mod42"}},
	})

	got := buf.ScanForBotResponse(
		"", "bot-dyno", 5*time.Second, now,
	)
	if got == nil {
		t.Fatalf("expected to find bot response across channels; got nil")
	}
	if got.Interaction == nil || got.Interaction.User == nil {
		t.Fatalf("expected interaction metadata populated; got %+v", got)
	}
	if got.Interaction.User.ID != "mod42" {
		t.Errorf("expected mod42; got %q", got.Interaction.User.ID)
	}
}

func TestMessageBufferConcurrentPush(t *testing.T) {
	buf := NewMessageBuffer(50)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			buf.PushEntry(bufferedMessage{
				MessageID: makeID("concurrent", i),
				ChannelID: "c1",
				AuthorID:  "user1",
				Content:   "msg",
				Timestamp: time.Now(),
			})
		}(i)
	}
	wg.Wait()

	got := buf.ScanRecent("c1")
	if len(got) != 50 {
		t.Errorf("expected exactly 50 entries after concurrent push; got %d", len(got))
	}
}

func makeID(prefix string, i int) string {
	return prefix + string(rune('0'+i))
}
