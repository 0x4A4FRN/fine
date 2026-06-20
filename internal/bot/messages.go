package bot

import (
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"strings"
	"time"
)

const replyWindow = 5 * time.Minute

type MessageSender interface {
	ChannelMessageSend(
		channelID string,
		content string,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)

	ChannelMessageSendComplex(
		channelID string,
		data *discordgo.MessageSend,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type MessageEditor interface {
	ChannelMessageEdit(
		channelID, messageID, content string,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type MessageFetcher interface {
	ChannelMessage(
		channelID, messageID string,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type MessageDeleter interface {
	ChannelMessageDelete(
		channelID, messageID string,
		options ...discordgo.RequestOption,
	) error
}

// DiscordMessageAPI is the composite for handlers that need all message
// operations. The split into MessageSender/MessageEditor/MessageFetcher/
// MessageDeleter helps with testing individual operations in isolation.
type DiscordMessageAPI interface {
	MessageSender
	MessageEditor
	MessageFetcher
	MessageDeleter
}

func (h *Handler) sendReply(channelID, content, replyToMessageID string) (msgID string, ok bool) {
	if h.messageAPI == nil {
		h.logger.Warn("handler: cannot send reply; no message API configured")
		return "", false
	}
	msg, err := h.messageAPI.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: content,
		Reference: &discordgo.MessageReference{
			MessageID:       replyToMessageID,
			ChannelID:       channelID,
			FailIfNotExists: func() *bool { b := false; return &b }(),
		},
	})
	if err != nil {
		h.logger.Error("handler: sending reply",
			zap.String("channel_id", channelID),
			zap.String("reply_to", replyToMessageID),
			zap.Error(err),
		)
		return "", false
	}
	h.logger.Debug("handler: reply sent",
		zap.String("channel_id", channelID),
		zap.String("reply_to", replyToMessageID),
		zap.Int("len", len(content)),
		zap.String("message_id", msg.ID),
	)
	return msg.ID, true
}
func (h *Handler) isReplyToBot(msg *discordgo.Message) bool {
	if msg.MessageReference == nil {
		return false
	}
	if msg.MessageReference.MessageID == "" {
		return false
	}
	if h.messageAPI == nil {
		return false
	}

	refMsg, err := h.messageAPI.ChannelMessage(
		msg.MessageReference.ChannelID,
		msg.MessageReference.MessageID,
	)
	if err != nil {
		return false
	}
	if refMsg.Author == nil || refMsg.Author.ID != h.BotID() {
		return false
	}

	if time.Since(msg.Timestamp) > replyWindow {
		return false
	}

	return true
}
func isMentioned(mentions []*discordgo.User, botID string) bool {
	for _, u := range mentions {
		if u.ID == botID {
			return true
		}
	}
	return false
}
func stripMention(content, botID string) string {
	content = strings.ReplaceAll(content, "<@"+botID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botID+">", "")
	return strings.TrimSpace(content)
}

// editOrSend tries to edit the existing bot message identified by
// botMessageID in channelID to the given text. If the edit fails (or there is
// no botMessageID to edit), it falls back to a fresh ChannelMessageSend and
// returns the new message ID. Returns "" when no message API is configured or
// both operations fail.
func (h *Handler) editOrSend(channelID, botMessageID, text string) string {
	if botMessageID != "" && h.messageAPI != nil {
		if _, err := h.messageAPI.ChannelMessageEdit(channelID, botMessageID, text); err == nil {
			return botMessageID
		}
		h.logger.Warn("handler: edit failed; falling back to fresh send",
			zap.String("channel_id", channelID),
			zap.String("message_id", botMessageID),
		)
	}
	if h.messageAPI != nil {
		msg, err := h.messageAPI.ChannelMessageSend(channelID, text)
		if err != nil {
			h.logger.Error("handler: fallback send failed",
				zap.String("channel_id", channelID),
				zap.Error(err),
			)
			return ""
		}
		return msg.ID
	}
	return ""
}
