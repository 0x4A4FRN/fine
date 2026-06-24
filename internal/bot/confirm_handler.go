package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/executor"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
)

const confirmReplyText = "Cancelled."

func isReplyToConfirmation(m *discordgo.MessageCreate, windowBotMsgID string) bool {
	if windowBotMsgID == "" {
		return false
	}
	return m.MessageReference != nil &&
		m.MessageReference.MessageID == windowBotMsgID
}
func (h *Handler) handlePendingConfirmation(
	ctx context.Context,
	m *discordgo.MessageCreate,
) bool {
	window, tx, err := GetOpenWindow(ctx, h.windowDB, m.ChannelID, m.Author.ID)
	if err != nil {
		h.logger.Error("handler: checking confirmation window", zap.Error(err))
		return false
	}

	if window == nil {
		h.logger.Debug("handler: no open confirmation window")
		return false
	}

	h.logger.Info("handler: open confirmation window found for author",
		zap.Int64("window_id", window.ID),
		zap.String("bot_message_id", window.BotMessageID),
	)

	defer func() {
		_ = tx.Rollback(ctx)
	}()

	action, _ := MatchConfirmation(m.Content)

	isReply := isReplyToConfirmation(m, window.BotMessageID)

	switch action {
	case "yes":
		if !isReply {
			h.logger.Info("handler: 'yes' without reply-chain; ignoring confirmation",
				zap.String("author_id", m.Author.ID),
				zap.String("message_id", m.ID),
				zap.String("expected_reference", window.BotMessageID),
				zap.Bool("has_reference", m.MessageReference != nil),
				func() zap.Field {
					if m.MessageReference != nil {
						return zap.String("actual_reference", m.MessageReference.MessageID)
					}
					return zap.Skip()
				}(),
			)

			_ = tx.Rollback(ctx)
			return false
		}

		var wp WindowPayload
		if err := json.Unmarshal([]byte(window.Payload), &wp); err != nil {
			h.logger.Error("handler: deserializing confirmation payload", zap.Error(err))
			if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
				h.logger.Error("handler: updating window status to cancelled", zap.Error(err))
			}
			if err := tx.Commit(ctx); err != nil {
				h.logger.Error("handler: committing confirmation tx", zap.Error(err))
			}
			return true
		}
		if wp.Response == nil {
			h.logger.Error("handler: confirmation payload missing response",
				zap.Int64("window_id", window.ID),
			)
			if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
				h.logger.Error("handler: updating window status to cancelled", zap.Error(err))
			}
			if err := tx.Commit(ctx); err != nil {
				h.logger.Error("handler: committing confirmation tx", zap.Error(err))
			}
			return true
		}
		h.logger.Info("handler: confirmation accepted; executing",
			zap.Int64("window_id", window.ID),
			zap.String("intent", wp.Response.Intent),
			zap.String("source_message_id", wp.SourceMessageID),
		)

		meta := executor.ActionMeta{
			GuildID:            m.GuildID,
			ChannelID:          m.ChannelID,
			ActorID:            m.Author.ID,
			SourceMsgID:        wp.SourceMessageID,
			BotMessageID:       window.BotMessageID,
			UserReplyMessageID: m.ID,
		}

		verbose := h.guildSettingsVerbose(m.GuildID)
		succeeded := h.executeResponseWithMeta(
			ctx, wp.Response, meta, verbose, nil,
		)

		// After execution, clean up the confirmation prompt + the
		// user's "yes" reply. This leaves only the invoke + the
		// result in the channel. Purge is the exception — its
		// executor deletes the confirmation + invoke + yes reply
		// itself (after the bulk delete completes) so bystanders
		// see the confirmation while messages are disappearing.
		if succeeded {
			h.cleanupConfirmationAfterExecute(meta, wp.Response.Intent)
		}

		if err := UpdateStatus(ctx, tx, window.ID, "executed"); err != nil {
			h.logger.Error("handler: updating window status to executed", zap.Error(err))
		}
		if err := tx.Commit(ctx); err != nil {
			h.logger.Error("handler: committing confirmation tx", zap.Error(err))
		}

		if h.store != nil {
			h.writeAssistantMessage(ctx, m, buildAssistantOutcome(wp.Response))
		}
		return true

	case "no":
		if !isReply {

			h.logger.Info("handler: 'no' without reply-chain; ignoring confirmation",
				zap.String("author_id", m.Author.ID),
				zap.String("message_id", m.ID),
				zap.String("expected_reference", window.BotMessageID),
			)
			_ = tx.Rollback(ctx)
			return false
		}

		if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
			h.logger.Error("handler: updating window status to cancelled", zap.Error(err))
		}
		if err := tx.Commit(ctx); err != nil {
			h.logger.Error("handler: committing confirmation tx", zap.Error(err))
		}
		h.sendReply(m.ChannelID, h.cancelReplyText(), m.ID)
		return true

	default:

		if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
			h.logger.Error("handler: updating window status to cancelled", zap.Error(err))
		}
		if err := tx.Commit(ctx); err != nil {
			h.logger.Error("handler: committing confirmation tx", zap.Error(err))
		}
		return false
	}
}
func (h *Handler) handleDestructiveConfirmation(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
	ph *placeholder,
) {
	expiresAt := time.Now().UTC().Add(h.confirmWindowDuration)

	// For purge, pre-scan the channel to give an accurate confirmation.
	// This tells the user exactly how many messages will be deleted vs
	// skipped (too old), so they don't confirm "purge 1000" only to find
	// the channel only has 3 messages.
	var purgeScan *executor.PurgeScanResult
	if resp.Intent == "purge_messages" && h.purgeScanFn != nil {
		count := 100
		if resp.Parameters.MessageCount != nil && *resp.Parameters.MessageCount > 0 {
			count = *resp.Parameters.MessageCount
			if count > 1000 {
				count = 1000
			}
		}
		scan, err := h.purgeScanFn(ctx, m.ChannelID, m.ID, count)
		if err != nil {
			h.logger.Warn("handler: purge pre-scan failed; falling back to generic confirmation",
				zap.String("channel_id", m.ChannelID),
				zap.Error(err),
			)
		} else {
			purgeScan = scan
			h.logger.Info("handler: purge pre-scan complete",
				zap.Int("deletable", scan.Deletable),
				zap.Int("skipped", scan.Skipped),
				zap.Int("requested", scan.Requested),
			)
			// If nothing can be deleted, deny without confirmation.
			if scan.Deletable == 0 {
				noDeletableMsg := h.renderPurgeNothingDeletable(scan.Skipped)
				if ph != nil {
					h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, noDeletableMsg)
				} else {
					h.sendReply(m.ChannelID, noDeletableMsg, m.ID)
				}
				return
			}
		}
	}

	confirmMsg := buildConfirmMessage(resp, expiresAt, h.replies, purgeScan)

	wp := WindowPayload{
		Response:            resp,
		SourceMessageID:     m.ID,
		OriginalConfirmText: confirmMsg,
	}
	payload, err := json.Marshal(wp)
	if err != nil {
		h.logger.Error("handler: serializing confirmation payload", zap.Error(err))
		return
	}

	if h.messageAPI == nil {
		h.logger.Warn("handler: cannot send confirmation message: no message API configured")
		return
	}

	msgID := h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, confirmMsg)
	if msgID == "" {

		return
	}

	if h.windowDB == nil {

		h.logger.Warn("handler: cannot persist confirmation window: no windowDB")
		return
	}

	windowID, err := CreateWindow(
		ctx,
		h.windowDB,
		m.GuildID,
		m.ChannelID,
		m.Author.ID,
		msgID,
		string(payload),
		expiresAt,
	)
	if err != nil {
		h.logger.Error("handler: creating confirmation window", zap.Error(err))
		return
	}
	// Attach confirmation buttons to the message (additive to text-based yes/no).
	components := BuildConfirmButtonComponents(windowID)
	if h.discord != nil {
		_, editErr := h.discord.ChannelMessageEditComplex(
			&discordgo.MessageEdit{
				Channel:    m.ChannelID,
				ID:         msgID,
				Content:    &confirmMsg,
				Components: &components,
			},
		)
		if editErr != nil {
			h.logger.Warn("handler: adding confirmation buttons failed",
				zap.Error(editErr),
			)
		}
	}

	h.logger.Info("handler: confirmation prompt sent; awaiting reply",
		zap.String("intent", resp.Intent),
		zap.String("bot_message_id", msgID),
		zap.String("source_message_id", m.ID),
	)
}
func (h *Handler) handleMultiActionConfirmation(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
	ph *placeholder,
) {
	expiresAt := time.Now().UTC().Add(h.confirmWindowDuration)
	confirmMsg := buildMultiActionConfirmMessage(resp, expiresAt, h.replies)

	wp := WindowPayload{
		Response:            resp,
		SourceMessageID:     m.ID,
		OriginalConfirmText: confirmMsg,
	}
	payload, err := json.Marshal(wp)
	if err != nil {
		h.logger.Error("handler: serializing multi-action payload", zap.Error(err))
		return
	}

	if h.messageAPI == nil {
		h.logger.Warn("handler: cannot send multi-action confirmation: no message API")
		return
	}

	msgID := h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, confirmMsg)
	if msgID == "" {
		return
	}

	if h.windowDB == nil {
		return
	}

	windowID, err := CreateWindow(
		ctx,
		h.windowDB,
		m.GuildID,
		m.ChannelID,
		m.Author.ID,
		msgID,
		string(payload),
		expiresAt,
	)
	if err != nil {
		h.logger.Error("handler: creating multi-action window", zap.Error(err))
		return
	}
	// Attach confirmation buttons to the message (additive to text-based yes/no).
	components := BuildConfirmButtonComponents(windowID)
	if h.discord != nil {
		_, editErr := h.discord.ChannelMessageEditComplex(
			&discordgo.MessageEdit{
				Channel:    m.ChannelID,
				ID:         msgID,
				Content:    &confirmMsg,
				Components: &components,
			},
		)
		if editErr != nil {
			h.logger.Warn("handler: adding multi-action confirmation buttons failed",
				zap.Error(editErr),
			)
		}
	}

	h.logger.Info("handler: multi-action confirmation prompt sent",
		zap.Int("action_count", len(resp.Actions)),
		zap.String("bot_message_id", msgID),
		zap.String("source_message_id", m.ID),
	)
}
func buildMultiActionConfirmMessage(resp *llm.LLMResponse, expiresAt time.Time, r replies.Renderer) string {
	// Build a human-readable summary of the queued actions for the template.
	var b strings.Builder
	for i, a := range resp.Actions {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.Intent)
		if len(a.Targets) > 0 {
			fmt.Fprintf(&b, " <@%s>", a.Targets[0].ID)
		}
	}
	actionsSummary := b.String()

	if r != nil {
		return r.Get("confirmation", "multi_action", map[string]string{
			"count":   strconv.Itoa(len(resp.Actions)),
			"actions": actionsSummary,
			"expires": strconv.FormatInt(expiresAt.Unix(), 10),
		})
	}

	// Fallback when no replies renderer is configured.
	var fb strings.Builder
	fmt.Fprintf(&fb, "Confirm %d actions: ", len(resp.Actions))
	fb.WriteString(actionsSummary)
	fmt.Fprintf(&fb, "? Reply yes/no. (Expires <t:%d:R>)", expiresAt.Unix())
	return fb.String()
}
func (h *Handler) editConfirmationDone(
	channelID, messageID, original string,
) {
	if h.messageAPI == nil || messageID == "" || original == "" {
		return
	}
	doneText := h.renderConfirmationDone(original)
	if _, err := h.messageAPI.ChannelMessageEdit(
		channelID, messageID, doneText,
	); err != nil {
		h.logger.Warn("handler: editing confirmation prompt to Done; failed",
			zap.String("channel_id", channelID),
			zap.String("message_id", messageID),
			zap.Error(err),
		)
		return
	}
	h.logger.Info("handler: confirmation prompt edited; Done",
		zap.String("channel_id", channelID),
		zap.String("message_id", messageID),
	)
}

// cleanupConfirmationAfterExecute deletes the bot's confirmation prompt and
// (in the text flow) the user's "yes" reply after a confirmed action has
// executed. This leaves only the invoke + the result in the channel.
// Purge is the exception — its executor handles its own cleanup so the
// confirmation stays visible while the bulk delete is in progress.
func (h *Handler) cleanupConfirmationAfterExecute(meta executor.ActionMeta, intent string) {
	// Purge's executor (deleteConfirmationFlowMessages) handles cleanup so
	// the confirmation stays visible during the bulk delete.
	if intent == "purge_messages" {
		return
	}
	if h.messageAPI == nil {
		return
	}
	// Delete the bot's confirmation prompt.
	if meta.BotMessageID != "" {
		if err := h.messageAPI.ChannelMessageDelete(meta.ChannelID, meta.BotMessageID); err != nil {
			h.logger.Debug("handler: cleanup: confirmation prompt already gone",
				zap.String("channel_id", meta.ChannelID),
				zap.String("message_id", meta.BotMessageID),
				zap.Error(err),
			)
		} else {
			h.logger.Info("handler: cleanup: confirmation prompt deleted",
				zap.String("channel_id", meta.ChannelID),
				zap.String("message_id", meta.BotMessageID),
			)
		}
	}
	// Delete the user's "yes" reply (text flow only; button flow has no
	// separate reply message).
	if meta.UserReplyMessageID != "" {
		if err := h.messageAPI.ChannelMessageDelete(meta.ChannelID, meta.UserReplyMessageID); err != nil {
			h.logger.Debug("handler: cleanup: user yes-reply already gone",
				zap.String("channel_id", meta.ChannelID),
				zap.String("message_id", meta.UserReplyMessageID),
				zap.Error(err),
			)
		} else {
			h.logger.Info("handler: cleanup: user yes-reply deleted",
				zap.String("channel_id", meta.ChannelID),
				zap.String("message_id", meta.UserReplyMessageID),
			)
		}
	}
}
func (h *Handler) renderConfirmationDone(original string) string {
	if h.replies == nil {
		return "~~" + original + "~~ Done."
	}
	return h.replies.Get("confirmation", "done", map[string]string{
		"original": original,
	})
}

// renderPurgeNothingDeletable renders the message shown when a purge
// pre-scan finds zero deletable messages (all are older than 14 days).
// skipped is the count of too-old messages found during the scan.
func (h *Handler) renderPurgeNothingDeletable(skipped int) string {
	if h.replies == nil {
		if skipped > 0 {
			return fmt.Sprintf("All %d messages here are older than 14 days. Nothing I can delete.", skipped)
		}
		return "Nothing to delete. The channel has no messages."
	}
	return h.replies.Get("purge", "nothing_deletable", map[string]string{
		"skipped":      strconv.Itoa(skipped),
		"max_age_days": "14",
	})
}

func buildConfirmMessage(
	resp *llm.LLMResponse,
	expiresAt time.Time,
	r replies.Renderer,
	purgeScan *executor.PurgeScanResult,
) string {
	if resp.Intent == "purge_messages" {
		expiresStr := strconv.FormatInt(expiresAt.Unix(), 10)

		// If we have scan results, show accurate counts.
		if purgeScan != nil {
			deletable := strconv.Itoa(purgeScan.Deletable)
			skipped := strconv.Itoa(purgeScan.Skipped)
			maxAgeDays := "14"

			// If deletable >= requested, the user gets what they asked for.
			// If deletable < requested, some messages are too old or the
			// channel has fewer than requested.
			if purgeScan.Deletable >= purgeScan.Requested {
				messages := "message"
				if purgeScan.Deletable != 1 {
					messages = "messages"
				}
				if r == nil {
					return fmt.Sprintf(
						"Confirm: **purge %d %s**? Reply yes/no. (Expires <t:%d:R>)",
						purgeScan.Deletable, messages, expiresAt.Unix(),
					)
				}
				return r.Get("confirmation", "purge", map[string]string{
					"deleted":  strconv.Itoa(purgeScan.Requested),
					"messages": messages,
					"expires":  expiresStr,
				})
			}

			// Deletable < requested — show the partial picture.
			messages := "message"
			if purgeScan.Deletable != 1 {
				messages = "messages"
			}
			if r == nil {
				return fmt.Sprintf(
					"I can only delete **%s %s** — %s are older than %s days. Yes/No? (Expires <t:%d:R>)",
					deletable, messages, skipped, maxAgeDays, expiresAt.Unix(),
				)
			}
			return r.Get("confirmation", "purge_partial", map[string]string{
				"deletable":    deletable,
				"messages":     messages,
				"skipped":      skipped,
				"max_age_days": maxAgeDays,
				"expires":      expiresStr,
			})
		}

		// No scan results (scan failed or not configured) — fall back to
		// the old behavior: show the requested count.
		const maxPurgeDisplay = 1000
		deleted := "?"
		if resp.Parameters.MessageCount != nil {
			count := *resp.Parameters.MessageCount
			if count > maxPurgeDisplay {
				count = maxPurgeDisplay
			}
			if count < 1 {
				count = 1
			}
			deleted = strconv.Itoa(count)
		}
		messages := "message"
		if deleted != "1" {
			messages = "messages"
		}
		if r == nil {
			return fmt.Sprintf(
				"Confirm: **purge %s %s**? Reply yes/no. (Expires <t:%d:R>)",
				deleted, messages, expiresAt.Unix(),
			)
		}
		return r.Get("confirmation", "purge", map[string]string{
			"deleted":  deleted,
			"messages": messages,
			"expires":  expiresStr,
		})
	}

	targetDesc := "target"
	if len(resp.Targets) > 0 {
		targetDesc = fmt.Sprintf("<@%s>", resp.Targets[0].ID)
	}

	if r == nil {
		return fmt.Sprintf(
			"Confirm: **%s** %s? Reply yes/no. (Expires <t:%d:R>)",
			resp.Intent,
			targetDesc,
			expiresAt.Unix(),
		)
	}
	return r.Get("confirmation", "generic", map[string]string{
		"intent":  resp.Intent,
		"target":  targetDesc,
		"expires": strconv.FormatInt(expiresAt.Unix(), 10),
	})
}
