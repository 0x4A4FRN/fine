package bot

import (
	"context"
	"fmt"
	"github.com/0x4A4FRN/fine/internal/conversation"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"strings"
)

func (h *Handler) ProcessMessageWithHistory(
	ctx context.Context,
	history []llm.Message,
	content string,
	replyTargetID string,
) (*llm.LLMResponse, error) {
	messages := llm.BuildMessages(h.systemPrompt, history, content)

	resp, err := h.provider.CompletionWithMessages(
		ctx, messages, llm.ResponseSchema,
	)
	if err != nil {
		return nil, fmt.Errorf("llm completion: %w", err)
	}

	if replyTargetID != "" && needsMessageReplyFixup(resp.Intent) {
		h.logger.Info("handler: applying reply-chain target fixup",
			zap.String("intent", resp.Intent),
			zap.String("reply_target_id", replyTargetID),
			zap.Int("original_targets", len(resp.Targets)),
		)
		resp.Targets = patchMessageTargetsFromReply(resp.Targets, replyTargetID)
	}

	if err := llm.ValidateLLMResponse(resp, h.logger); err != nil {
		return nil, fmt.Errorf("llm validation: %w", err)
	}

	return resp, nil
}
func (h *Handler) writeAssistantMessage(
	ctx context.Context,
	m *discordgo.MessageCreate,
	content string,
) {
	if content == "" {
		return
	}
	if err := h.store.WriteMessage(
		ctx,
		m.GuildID,
		m.ChannelID,
		m.Author.ID,
		"assistant",
		content,
		"",
	); err != nil {
		h.logger.Error("handler: writing assistant message", zap.Error(err))
	}
}
func buildAssistantOutcome(resp *llm.LLMResponse) string {
	if resp.Reply != nil && *resp.Reply != "" {
		return *resp.Reply
	}

	var b strings.Builder
	b.WriteString("Executed: ")
	b.WriteString(resp.Intent)
	if len(resp.Targets) > 0 {
		b.WriteString(" on ")
		b.WriteString(resp.Targets[0].ID)
	}
	if resp.Parameters.Reason != nil && *resp.Parameters.Reason != "" {
		b.WriteString(" (reason: ")
		b.WriteString(*resp.Parameters.Reason)
		b.WriteString(")")
	}
	return b.String()
}
func toLLMMessages(msgs []conversation.Message) []llm.Message {
	result := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return result
}
