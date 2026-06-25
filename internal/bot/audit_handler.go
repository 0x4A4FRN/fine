package bot

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
)

func (h *Handler) handleAuditLookup(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
	ph *placeholder,
) {
	if h.auditDB == nil || resp.AuditQuery == nil {
		h.logger.Warn("handler: audit lookup skipped; missing auditDB or AuditQuery")
		return
	}

	query := audit.AuditQuery{
		Info: resp.AuditQuery.Info,
	}
	if resp.AuditQuery.Action != nil {
		query.Action = resp.AuditQuery.Action
	}
	if resp.AuditQuery.TargetID != nil {
		query.TargetID = resp.AuditQuery.TargetID
	}

	result, err := audit.Lookup(ctx, h.auditDB, m.GuildID, query)
	if err != nil {
		h.logger.Error("handler: audit lookup", zap.Error(err))
		h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, h.cloudyReplyText())
		return
	}

	hasTarget := query.TargetID != nil
	templateName := audit.SelectTemplate(query.Info, result, hasTarget)
	h.logger.Debug("handler: audit lookup template",
		zap.String("template", templateName),
		zap.Bool("has_target", hasTarget),
	)

	var data audit.TemplateData
	if result != nil {
		data = audit.BuildTemplateData(result, h.replies)
	}

	replyText, err := h.replies.Render(templateName, data)
	if err != nil {
		h.logger.Error("handler: rendering audit reply",
			zap.String("template", templateName),
			zap.Error(err),
		)
		fallback, ferr := h.replies.Render("audit.no_record", data)
		if ferr != nil {
			h.logger.Error("handler: rendering audit fallback",
				zap.String("fallback_template", "audit.no_record"),
				zap.Error(ferr),
			)
			fallback = "I don't have a record of that."
		}
		replyText = fallback
	}

	h.logger.Info("handler: sending audit reply",
		zap.Int("reply_len", len(replyText)),
	)
	h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, replyText)

	if h.store != nil {
		h.writeAssistantMessage(ctx, m, replyText)
	}
}
