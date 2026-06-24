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

const interactionTimeout = 30 * time.Second

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

func (h *Handler) handleSnipePagination(i *discordgo.InteractionCreate, direction string) {
	if h.snipePaginationFn == nil {
		h.respondInteraction(i, h.interactionText("snipe_not_configured"))
		return
	}

	_, botMsgID, ok := parseSnipeCustomID(i.MessageComponentData().CustomID)
	if !ok {
		h.respondInteraction(i, h.interactionText("invalid_button"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), interactionTimeout)
	defer cancel()

	snap, text, components := h.snipePaginationFn(ctx, botMsgID, direction)
	if snap == nil {

		_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		return
	}

	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    text,
			Components: components,
		},
	})
}

func (h *Handler) handleSnipeDelete(i *discordgo.InteractionCreate) {
	if h.discord == nil {
		return
	}

	botMsgID := i.Message.ID

	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	if err := h.discord.ChannelMessageDelete(i.ChannelID, botMsgID); err != nil {
		h.logger.Error("handler: interaction: snipe delete failed (bot message)",
			zap.String("message_id", botMsgID),
			zap.Error(err),
		)

	} else {
		h.logger.Info("handler: interaction: snipe bot message deleted",
			zap.String("message_id", botMsgID),
			zap.String("channel_id", i.ChannelID),
		)
	}

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

	if h.snipeDeletePageFn != nil {
		h.snipeDeletePageFn(botMsgID)
	}
}

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

	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	h.handleConfirmYesButton(i, windowID, clickerID)
}

func (h *Handler) handleConfirmNoButton(i *discordgo.InteractionCreate, windowID int64, clickerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), interactionTimeout)
	defer cancel()

	window, tx, err := GetWindowByID(ctx, h.windowDB, windowID)
	if err != nil {
		h.logger.Error("handler: interaction: confirm no: window lookup failed",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		h.respondInteraction(i, h.interactionText("lookup_failed"))
		return
	}
	if window == nil {
		h.respondInteraction(i, h.interactionText("window_not_found"))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if clickerID != "" && window.UserID != clickerID {
		h.respondInteraction(i, h.interactionText("not_original_requester"))
		return
	}

	if window.Status != "open" {
		h.respondInteraction(i, h.interactionText("already_resolved"))
		return
	}

	if err := UpdateStatus(ctx, tx, window.ID, "cancelled"); err != nil {
		h.logger.Error("handler: interaction: confirm cancel failed",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		h.respondInteraction(i, h.interactionText("cancel_failed"))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("handler: interaction: committing cancel tx",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		h.respondInteraction(i, h.interactionText("cancel_failed"))
		return
	}

	_ = h.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: h.interactionText("cancelled"),
		},
	})
	h.logger.Info("handler: interaction: confirmation cancelled via button",
		zap.Int64("window_id", windowID),
		zap.String("user_id", clickerID),
	)
}

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

	if clickerID != "" && window.UserID != clickerID {
		h.logger.Info("handler: interaction: confirm yes: rejected (not original requester)",
			zap.Int64("window_id", windowID),
			zap.String("clicker_id", clickerID),
			zap.String("original_user_id", window.UserID),
		)
		h.respondInteraction(i, h.interactionText("not_original_requester"))
		return
	}

	if window.Status != "open" {
		h.logger.Info("handler: interaction: confirm yes: window already resolved",
			zap.Int64("window_id", windowID),
			zap.String("status", window.Status),
		)
		return
	}

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
			return
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
			return
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

	if succeeded {
		h.cleanupConfirmationAfterExecute(meta, wp.Response.Intent)
	}

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
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("handler: interaction: committing tx",
			zap.Int64("window_id", windowID),
			zap.Error(err),
		)
		return
	}

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

func (h *Handler) interactionText(key string) string {
	if h.replies == nil {
		return "[interaction." + key + "]"
	}
	return h.replies.Get("interaction", key, nil)
}

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
