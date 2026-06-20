package bot

import (
	"context"
	"github.com/0x4A4FRN/fine/internal/executor"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"time"
)

func (h *Handler) executeResponse(
	ctx context.Context,
	resp *llm.LLMResponse,
	m *discordgo.MessageCreate,
	sudo, verbose bool,
	ph *placeholder,
) {
	meta := executor.ActionMeta{
		GuildID:     m.GuildID,
		ChannelID:   m.ChannelID,
		ActorID:     m.Author.ID,
		SourceMsgID: m.ID,
		Sudo:        sudo,
	}
	h.executeResponseWithMeta(ctx, resp, meta, verbose, ph)
}
func (h *Handler) guildSettingsFor(
	guildID string,
) (sudo, verbose bool) {
	if h.guildSettings == nil || guildID == "" {
		return false, false
	}
	gs := h.guildSettings.Get(guildID)
	return gs.SudoMode, gs.VerboseError
}
func (h *Handler) guildSettingsVerbose(guildID string) bool {
	_, v := h.guildSettingsFor(guildID)
	return v
}
func (h *Handler) executeResponseWithMeta(
	ctx context.Context,
	resp *llm.LLMResponse,
	meta executor.ActionMeta,
	verbose bool,
	ph *placeholder,
) bool {
	// Delete the placeholder BEFORE calling the executor. This ensures the
	// rotating "Thinking…" message is gone before any new message (from the
	// executor or from us) appears in the channel. The previous order
	// (delete after) caused a visible flash where the new reply appeared
	// alongside the still-rotating placeholder before the delete propagated.
	if ph != nil {
		h.deletePlaceholderOnly(ph)
		ph = nil // prevent double-delete in downstream helpers
	}

	err := h.executor.ExecuteResponse(ctx, resp, meta)
	if err != nil {
		if tr, ok := err.(*executor.TextResult); ok {
			// TextResult is NOT an error — it's the executor's
			// way of saying "I handled this, send this text as
			// the reply." Executors like purge use it for success
			// messages with auto-delete. Return true so the
			// caller (e.g. handleConfirmYesButton) treats this
			// as a successful execution.
			h.handleExecutorTextResult(meta.ChannelID, meta.SourceMsgID, tr, nil)
			return true
		}
		h.logger.Error("handler: executor returned error",
			zap.String("intent", resp.Intent),
			zap.Error(err),
		)

		// Placeholder already deleted above; just send the error reply.
		botMsgID, _ := h.sendReply(
			meta.ChannelID,
			h.failReplyText(resp.Intent, resp, err, verbose),
			meta.SourceMsgID,
		)
		_ = botMsgID
		return false
	}

	// Send the success reply and capture its message id so the natural-end
	// or lift path can edit it later instead of posting a redundant new one.
	// Placeholder already deleted above; just send the reply.
	botMsgID, _ := h.sendReply(
		meta.ChannelID,
		h.renderDefaultSuccess(resp.Intent, resp),
		meta.SourceMsgID,
	)

	h.recordTimeoutTransition(resp, meta, botMsgID)

	// Untimeout also edits the original timeout-grant message in place so
	// the channel timeline shows the lift transition. The fresh success
	// reply above is the command acknowledgement; the edit updates the
	// state of the previously posted message.
	if resp.Intent == "untimeout" {
		h.handleTimeoutLift(resp, meta)
	}
	return true
}
func (h *Handler) handleExecutorTextResult(
	channelID string,
	sourceMsgID string,
	tr *executor.TextResult,
	ph *placeholder,
) {
	if h.messageAPI == nil {
		h.logger.Warn("handler: cannot send executor TextResult; no message API configured")
		return
	}

	// Safety net: callers (executeUtilityResponse, executeResponseWithMeta)
	// pre-delete the placeholder before invoking the executor, so ph should
	// be nil here. If it isn't (defensive), clean it up before sending.
	if ph != nil && ph.msgID != "" {
		h.deletePlaceholderOnly(ph)
	}

	var msgID string
	if tr.SkipReply || sourceMsgID == "" {
		// No reply reference requested (or no source to reply to) — plain send.
		msgID = h.editOrSend(channelID, "", tr.Text)
		if msgID == "" {
			return
		}
		h.logger.Info("handler: sent executor TextResult (plain)",
			zap.String("channel_id", channelID),
			zap.Int("text_len", len(tr.Text)),
			zap.String("message_id", msgID),
		)
	} else {
		// Send as a reply to the invoking message. Placeholder is already
		// gone (pre-deleted by caller), so no delete-and-reply dance needed.
		var ok bool
		msgID, ok = h.sendReply(channelID, tr.Text, sourceMsgID)
		if !ok || msgID == "" {
			return
		}
		h.logger.Info("handler: sent executor TextResult (as reply)",
			zap.String("channel_id", channelID),
			zap.Int("text_len", len(tr.Text)),
			zap.String("message_id", msgID),
			zap.String("reply_to", sourceMsgID),
		)
	}

	if tr.AutoDeleteAfter <= 0 {
		return
	}
	after := tr.AutoDeleteAfter
	channel := channelID
	id := msgID
	time.AfterFunc(after, func() {
		if delErr := h.messageAPI.ChannelMessageDelete(channel, id); delErr != nil {
			h.logger.Warn("handler: auto-delete of executor reply failed",
				zap.String("channel_id", channel),
				zap.String("message_id", id),
				zap.Duration("after", after),
				zap.Error(delErr),
			)
			return
		}
		h.logger.Info("handler: auto-delete of executor reply completed",
			zap.String("channel_id", channel),
			zap.String("message_id", id),
			zap.Duration("after", after),
		)
	})
}
func (h *Handler) executeUtilityResponse(
	ctx context.Context,
	resp *llm.LLMResponse,
	m *discordgo.MessageCreate,
	ph *placeholder,
) {
	h.logger.Info("handler: dispatching utility intent",
		zap.String("intent", resp.Intent),
	)

	// Snipe sends its own message with buttons inside Execute. The
	// placeholder must be deleted BEFORE Execute so the user doesn't see
	// both the placeholder and the snipe message briefly coexist.
	//
	// Other utility executors (ping, help, info, status) return a
	// TextResult. For those, we want the placeholder visible DURING
	// execution (e.g. status does gateway + DB + S3 round-trips that can
	// take a moment). The placeholder is deleted by handleExecutorTextResult
	// after the result is sent.
	if resp.Intent == "snipe" && ph != nil {
		h.deletePlaceholderOnly(ph)
		ph = nil
	}

	action := executor.Action{
		Intent:      resp.Intent,
		Targets:     resp.Targets,
		Parameters:  resp.Parameters,
		GuildID:     m.GuildID,
		ChannelID:   m.ChannelID,
		ActorID:     m.Author.ID,
		SourceMsgID: m.ID,
	}
	err := h.executor.Execute(ctx, action)
	if err != nil {
		if tr, ok := err.(*executor.TextResult); ok {
			h.logger.Info("handler: utility intent produced reply",
				zap.String("intent", resp.Intent),
				zap.Int("text_len", len(tr.Text)),
			)

			h.handleExecutorTextResult(m.ChannelID, m.ID, tr, ph)
			return
		}
		h.logger.Error("handler: utility executor failed",
			zap.String("intent", resp.Intent),
			zap.Error(err),
		)
		// Clean up placeholder if it wasn't already (non-snipe case).
		if ph != nil {
			h.deletePlaceholderOnly(ph)
		}
		return
	}
	// err == nil — the executor handled its own message directly (only
	// snipe currently). Placeholder was already pre-deleted for snipe.
	// For any future executor that returns nil without pre-delete, clean up.
	if ph != nil {
		h.deletePlaceholderOnly(ph)
	}
	h.logger.Info("handler: utility executor handled its own message",
		zap.String("intent", resp.Intent),
	)
}
