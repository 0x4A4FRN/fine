package bot

import (
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type MessageBuffer struct {
	mu       sync.Mutex
	capacity int
	perChan  map[string][]bufferedMessage
}

type bufferedMessage struct {
	MessageID           string
	AuthorID            string
	ChannelID           string
	Content             string
	IsBot               bool
	Timestamp           time.Time
	Interaction         *discordgo.MessageInteraction
	InteractionMetadata *discordgo.MessageInteractionMetadata
	ReferencedMessageID string
}

func NewMessageBuffer(capacityPerChannel int) *MessageBuffer {
	if capacityPerChannel <= 0 {
		capacityPerChannel = 15
	}
	return &MessageBuffer{
		capacity: capacityPerChannel,
		perChan:  make(map[string][]bufferedMessage),
	}
}

func (b *MessageBuffer) Push(m *discordgo.MessageCreate) {
	if m == nil || m.Message == nil || m.Author == nil {
		return
	}
	entry := bufferedMessage{
		MessageID:           m.ID,
		AuthorID:            m.Author.ID,
		ChannelID:           m.ChannelID,
		Content:             m.Content,
		IsBot:               m.Author.Bot,
		Timestamp:           m.Timestamp,
		Interaction:         m.Interaction,
		InteractionMetadata: m.InteractionMetadata,
	}
	if m.MessageReference != nil && m.MessageReference.MessageID != "" {
		entry.ReferencedMessageID = m.MessageReference.MessageID
	}
	b.PushEntry(entry)
}

func (b *MessageBuffer) PushEntry(e bufferedMessage) {
	if e.ChannelID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	existing := b.perChan[e.ChannelID]

	for _, prior := range existing {
		if prior.MessageID == e.MessageID {
			return
		}
	}

	inserted := append(existing, e)
	if len(inserted) > b.capacity {
		inserted = inserted[len(inserted)-b.capacity:]
	}
	b.perChan[e.ChannelID] = inserted
}

func (b *MessageBuffer) ScanRecent(channelID string) []bufferedMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.perChan[channelID]
	out := make([]bufferedMessage, len(src))
	copy(out, src)
	return out
}

func (b *MessageBuffer) ScanForAction(
	channelID, targetID string,
	intentKeywords []string,
	window time.Duration,
	now time.Time,
) *bufferedMessage {
	if targetID == "" || len(intentKeywords) == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if channelID == "" {
		return b.scanAcrossChannelsLocked("human", "", targetID, intentKeywords, window, now)
	}
	for i := len(b.perChan[channelID]) - 1; i >= 0; i-- {
		entry := b.perChan[channelID][i]
		if entry.IsBot {
			continue
		}
		if now.Sub(entry.Timestamp) > window {
			continue
		}
		if !mentionsTarget(entry.Content, targetID) {
			continue
		}
		if !containsAny(entry.Content, intentKeywords) {
			continue
		}
		out := entry
		return &out
	}
	return nil
}

func (b *MessageBuffer) ScanForBotResponse(
	channelID, botAuthorID string,
	window time.Duration,
	now time.Time,
) *bufferedMessage {
	if botAuthorID == "" {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if channelID == "" {
		return b.scanAcrossChannelsLocked("bot", botAuthorID, "", nil, window, now)
	}
	for i := len(b.perChan[channelID]) - 1; i >= 0; i-- {
		entry := b.perChan[channelID][i]
		if !entry.IsBot || entry.AuthorID != botAuthorID {
			continue
		}
		if now.Sub(entry.Timestamp) > window {
			continue
		}
		out := entry
		return &out
	}
	return nil
}

func (b *MessageBuffer) scanAcrossChannelsLocked(
	mode, authorID, targetID string,
	intentKeywords []string,
	window time.Duration,
	now time.Time,
) *bufferedMessage {
	var best *bufferedMessage
	for _, slice := range b.perChan {
		for i := len(slice) - 1; i >= 0; i-- {
			entry := slice[i]
			if now.Sub(entry.Timestamp) > window {
				break
			}
			switch mode {
			case "bot":
				if !entry.IsBot || entry.AuthorID != authorID {
					continue
				}
				out := entry
				best = &out
				return best
			case "human":
				if entry.IsBot {
					continue
				}
				if targetID != "" && !mentionsTarget(entry.Content, targetID) {
					continue
				}
				if !containsAny(entry.Content, intentKeywords) {
					continue
				}
				if best == nil || entry.Timestamp.After(best.Timestamp) {
					cp := entry
					best = &cp
				}
			}
		}
	}
	return best
}

func mentionsTarget(content, targetID string) bool {
	if content == "" || targetID == "" {
		return false
	}
	if strings.Contains(content, "<@"+targetID+">") {
		return true
	}
	if strings.Contains(content, "<@!"+targetID+">") {
		return true
	}
	return false
}

func containsAny(content string, subs []string) bool {
	for _, s := range subs {
		if s == "" {
			continue
		}
		if strings.Contains(content, s) {
			return true
		}
	}
	return false
}
