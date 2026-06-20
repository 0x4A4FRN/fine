package bot

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/executor"
)

// interactionTimeout bounds how long a button-click handler may run.
// Matches handlerTimeout in handler.go — interactions are scoped to the
// same per-event deadline so a stalled downstream call (S3 presign, DB
// query) cannot block the interaction handler goroutine indefinitely.
const interactionTimeout = 30 * time.Second

// HandleInteractionCreate dispatches Discord interaction events (button
// clicks) to the appropriate handler. It handles both snipe pagination
// buttons and confirmation yes/no buttons.
func (h *Handler) HandleInteractionCreate(_ *discordgo.Session, i *discordgo.InteractionCreate) {
	if h.logger == nil {
		return
	}

	data := i.MessageComponentData()

	customID := data.CustomID

	userID := ""
	if i.Member != nil && i.Member.User != nil {
		userID = i.Member.User.ID
	}

	h.logger.Info("handler: interaction: received",
		zap.String("custom_id", customID),
		zap.String("user_id", userID),
		zap.String("channel_id", i.ChannelID),
	)

	// Dispatch snipe buttons.
	if prefix, _, ok := parseSnipeCustomID(customID); ok {
		switch prefix {
		case "snipe_prev":
			h.handleSnipePagination(i, "prev")
		case "snipe_next":
			h.handleSnipePagination(i, "next")
		case "snipe_delete":
			h.handleSnipeDelete(i)
		}
		return
	}

	// Dispatch confirmation buttons.
	if prefix, _, ok := parseConfirmCustomID(customID); ok {
		switch prefix {
		case "confirm_yes":
			h.handleConfirmButton(i, true)
		case "confirm_no":
			h.handleConfirmButton(i, false)
		}
		return
	}

	h.logger.Warn("handler: interaction: unknown custom ID",
		zap.String("custom_id", customID),
	)
}

// handleSnipePagination handles prev/next button clicks on snipe messages.
// It looks up the in-memory page state (keyed by the bot message ID embedded
// in the button CustomID), navigates to the adjacent snapshot, and updates
// the interaction message in place via InteractionResponseUpdateMessage.
//
// With in-memory pagination and boundary-disabled buttons, the snap == nil
// case should never be reached from a button click (buttons are disabled at
// boundaries). It can still happen if the page state has expired (TTL) or
// was never stored. In that case we acknowledge with a deferred update so
// the interaction doesn't error out, and leave the message content as-is.
//
// A 30-second context timeout is applied so a stalled S3 presign call
// (or any other downstream slowness in the snipe renderer) cannot block
// the interaction handler indefinitely.
func (h *Handler) handleSnipePagination(i *discordgo.InteractionCreate, direction string) {
	if h.snipePaginationFn == nil {
		h.respondInteraction(i, "Snipe feature not configured.")
		return
	}

	// Parse the bot message ID from the custom ID.
	_, botMsgID, ok := parseSnipeCustomID(i.MessageComponentData().CustomID)
	if !ok {
		h.respondInteraction(i, "Invalid snipe button.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), interactionTimeout)
	defer cancel()

	snap, text, components := h.snipePaginationFn(ctx, i.ChannelID, botMsgID, direction)
	if snap == nil {
		// State expired or unexpected boundary — acknowledge with a deferred
		// update so the interaction doesn't error, and leave the message as-is.
		// This is the safety net for the case where the in-memory page state
		// has been evicted (e.g. bot restarted, or TTL expired).
		_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		return
	}

	// Update the message with the new snapshot content.
	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    text,
			Components: components,
		},
	})
}

// handleSnipeDelete handles the "Delete" button on snipe messages by
// deleting the bot's snipe message AND the original invoking user message
// (the "snipe 3" command). This keeps the channel clean — no trace of the
// snipe invocation remains after the mod dismisses the result.
func (h *Handler) handleSnipeDelete(i *discordgo.InteractionCreate) {
	if h.discord == nil {
		return
	}

	botMsgID := i.Message.ID

	// Acknowledge the interaction first, then delete the messages.
	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	// Delete the bot's snipe message.
	if err := h.discord.ChannelMessageDelete(i.ChannelID, botMsgID); err != nil {
		h.logger.Error("handler: interaction: snipe delete failed (bot message)",
			zap.String("message_id", botMsgID),
			zap.Error(err),
		)
		// Continue — we still want to try deleting the source message and
		// cleaning up page state.
	} else {
		h.logger.Info("handler: interaction: snipe bot message deleted",
			zap.String("message_id", botMsgID),
			zap.String("channel_id", i.ChannelID),
		)
	}

	// Delete the original invoking user message (e.g. "snipe 3") so the
	// channel doesn't show a dangling command after the result is dismissed.
	// Best-effort — ignore errors (message may already be gone).
	if h.snipeSourceMsgIDFn != nil {
		if sourceMsgID := h.snipeSourceMsgIDFn(botMsgID); sourceMsgID != "" {
			if err := h.discord.ChannelMessageDelete(i.ChannelID, sourceMsgID); err != nil {
				h.logger.Debug("handler: interaction: snipe source message already gone",
					zap.String("source_message_id", sourceMsgID),
					zap.Error(err),
				)
			} else {
				h.logger.Info("handler: interaction: snipe source message deleted",
					zap.String("source_message_id", sourceMsgID),
					zap.String("channel_id", i.ChannelID),
				)
			}
		}
	}

	// Clean up the in-memory page state immediately rather than waiting
	// for TTL expiry.
	if h.snipeDeletePageFn != nil {
		h.snipeDeletePageFn(botMsgID)
	}
}

// handleConfirmButton processes yes/no confirmation buttons. It mirrors
// the text-based confirmation flow but is triggered via button click.
// The "no" branch cancels the window immediately. The "yes" branch defers
// the interaction update, then looks up the window, executes the queued
// action, updates the window status to "executed", and edits the
// confirmation message to "Done".
func (h *Handler) handleConfirmButton(i *discordgo.InteractionCreate, confirmed bool) {
	if h.windowDB == nil || h.discord == nil {
		return
	}

	_, windowID, ok := parseConfirmCustomID(i.MessageComponentData().CustomID)
	if !ok {
		h.respondInteraction(i, "Invalid confirmation button.")
		return
	}

	clickerID := ""
	if i.Member != nil && i.Member.User != nil {
		clickerID = i.Member.User.ID
	}

	if !confirmed {
		h.handleConfirmNoButton(i, windowID, clickerID)
		return
	}

	// "Yes" — defer the interaction update so Discord doesn't time out
	// while we look up the window and execute the action. The actual
	// message edit (to "Done" or an error) happens after execution.
	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	h.handleConfirmYesButton(i, windowID, clickerID)
}

// handleConfirmNoButton cancels the confirmation window and edits the
// message to show the cancellation. Only the original requester may cancel.
func (h *Handler) handleConfirmNoButton(i *discordgo.InteractionCreate, windowID int64, clickerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), interactionTimeout)
	defer cancel()

	window, tx, err := GetWindowByID(ctx, h.windowDB, windowID)
	if err != nil {
		h.logger.Error("handler: interaction: confirm no: window lookup failed",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		h.respondInteraction(i, "Something went wrong looking up that confirmation.")
		return
	}
	if window == nil {
		h.respondInteraction(i, "That confirmation no longer exists.")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Only the original requester may cancel.
	if clickerID != "" && window.UserID != clickerID {
		h.respondInteraction(i, "Only the person who started this confirmation can cancel it.")
		return
	}

	// If the window is no longer open, it was already resolved.
	if window.Status != "open" {
		h.respondInteraction(i, "This confirmation has already been resolved.")
		return
	}

	if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
		h.logger.Error("handler: interaction: confirm cancel failed",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("handler: interaction: committing cancel tx",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
	}

	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: "~~Confirm...~~ Cancelled.",
		},
	})
	h.logger.Info("handler: interaction: confirmation cancelled via button",
		zap.Int64("window_id", windowID),
		zap.String("user_id", clickerID),
	)
}

// handleConfirmYesButton executes the queued action after a "Yes" button
// click. The interaction was already deferred by the caller; this function
// does the window lookup, execution, status update, and message edit.
func (h *Handler) handleConfirmYesButton(i *discordgo.InteractionCreate, windowID int64, clickerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), interactionTimeout)
	defer cancel()

	window, tx, err := GetWindowByID(ctx, h.windowDB, windowID)
	if err != nil {
		h.logger.Error("handler: interaction: confirm yes: window lookup failed",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		return
	}
	if window == nil {
		h.logger.Warn("handler: interaction: confirm yes: window not found",
			zap.Int64("window_id", windowID),
		)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Only the original requester may confirm.
	if clickerID != "" && window.UserID != clickerID {
		h.logger.Info("handler: interaction: confirm yes: rejected (not original requester)",
			zap.Int64("window_id", windowID),
			zap.String("clicker_id", clickerID),
			zap.String("original_user_id", window.UserID),
		)
		h.respondInteraction(i, "Only the person who started this confirmation can confirm it.")
		return
	}

	// If the window is no longer open, it was already resolved (e.g. by
	// a text-based "yes" that arrived first, or by another button click).
	if window.Status != "open" {
		h.logger.Info("handler: interaction: confirm yes: window already resolved",
			zap.Int64("window_id", windowID),
			zap.String("status", window.Status),
		)
		return
	}

	// Deserialize the payload to recover the queued LLM response.
	var wp WindowPayload
	if err := json.Unmarshal([]byte(window.Payload), &wp); err != nil {
		h.logger.Error("handler: interaction: confirm yes: deserializing payload",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
			h.logger.Error("handler: interaction: updating status to cancelled",
				zap.Int64("window_id", windowID),
				zap.Error(err),
			)
		}
		if err := tx.Commit(ctx); err != nil {
			h.logger.Error("handler: interaction: committing tx", zap.Error(err))
		}
		return
	}
	if wp.Response == nil {
		h.logger.Error("handler: interaction: confirm yes: payload missing response",
			zap.Int64("window_id", windowID),
		)
		if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
			h.logger.Error("handler: interaction: updating status to cancelled",
				zap.Int64("window_id", windowID),
				zap.Error(err),
			)
		}
		if err := tx.Commit(ctx); err != nil {
			h.logger.Error("handler: interaction: committing tx", zap.Error(err))
		}
		return
	}

	h.logger.Info("handler: interaction: confirm yes: executing",
		zap.Int64("window_id", windowID),
		zap.String("intent", wp.Response.Intent),
		zap.String("source_message_id", wp.SourceMessageID),
		zap.String("user_id", clickerID),
	)

	// Build the ActionMeta from the window + interaction context.
	// UserReplyMessageID is empty because the button flow has no separate
	// user reply message — the user clicked a button on the confirmation
	// prompt itself.
	guildID := i.GuildID
	if guildID == "" {
		guildID = window.GuildID
	}
	meta := executor.ActionMeta{
		GuildID:      guildID,
		ChannelID:    window.ChannelID,
		ActorID:      window.UserID,
		SourceMsgID:  wp.SourceMessageID,
		BotMessageID: window.BotMessageID,
	}

	verbose := h.guildSettingsVerbose(guildID)
	succeeded := h.executeResponseWithMeta(ctx, wp.Response, meta, verbose, nil)

	// After execution, clean up the confirmation prompt. For the button
	// flow there's no separate user "yes" message (the user clicked a
	// button on the confirmation itself). Purge is the exception — its
	// executor handles its own cleanup so bystanders see the
	// confirmation while messages are disappearing.
	if succeeded {
		h.cleanupConfirmationAfterExecute(meta, wp.Response.Intent)
	}

	// Mark the window as executed (or cancelled if execution failed).
	status := "executed"
	if !succeeded {
		status = "cancelled"
	}
	if err := UpdateStatus(ctx, tx, window.ID, status); err != nil {
		h.logger.Error("handler: interaction: updating window status",
			zap.Int64("window_id", windowID),
			zap.String("status", status),
			zap.Error(err),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("handler: interaction: committing tx",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
	}

	// Write the assistant outcome to the conversation store for future
	// LLM context.
	if h.store != nil && succeeded {
		h.writeAssistantMessage(ctx, &discordgo.MessageCreate{
			Message: &discordgo.Message{
				GuildID:   guildID,
				ChannelID: window.ChannelID,
				Author:    &discordgo.User{ID: window.UserID},
				ID:        wp.SourceMessageID,
			},
		}, buildAssistantOutcome(wp.Response))
	}

	h.logger.Info("handler: interaction: confirm yes: completed",
		zap.Int64("window_id", windowID),
		zap.String("intent", wp.Response.Intent),
		zap.Bool("succeeded", succeeded),
	)
}

// respondInteraction is a helper that acknowledges an interaction with a
// simple ephemeral message when the interaction hasn't been responded to yet.
func (h *Handler) respondInteraction(i *discordgo.InteractionCreate, message string) {
	if h.discord == nil {
		return
	}
	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// BuildConfirmButtonComponents returns the Discord action row with
// Yes/No confirmation buttons for use in destructive and multi-action
// confirmation messages.
func BuildConfirmButtonComponents(windowID int64) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Yes",
					Style:    discordgo.SuccessButton,
					CustomID: "confirm_yes_" + strconv.FormatInt(windowID, 10),
				},
				discordgo.Button{
					Label:    "No",
					Style:    discordgo.DangerButton,
					CustomID: "confirm_no_" + strconv.FormatInt(windowID, 10),
				},
			},
		},
	}
}
