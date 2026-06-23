package executor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
)

const (
	discordBulkDeleteMaxAge = 14 * 24 * time.Hour

	purgeBatchSize = 100

	maxAllPurge = 1000

	purgeAutoDeleteAfter = 5 * time.Second
)

// PurgeScanResult holds the result of a pre-confirmation channel scan.
// It tells the user exactly what will happen before they click "Yes".
type PurgeScanResult struct {
	Deletable int // messages < 14 days old (can be bulk-deleted)
	Skipped   int // messages >= 14 days old (Discord won't allow bulk delete)
	Requested int // the count the user asked for (clamped to maxAllPurge)
}

// PurgeDiscordAPI is the narrow set of Discord operations PurgeExecutor needs:
// MemberAPI for the permission gate, ChannelMessageAPI for the actual operation.
// Defining it consumer-side lets tests mock only these sub-interfaces
// instead of the full DiscordAPI composite.
type PurgeDiscordAPI interface {
	MemberAPI
	ChannelMessageAPI
}

type PurgeExecutor struct {
	discord PurgeDiscordAPI
	pool    audit.DB
	replies replies.Renderer
	logger  *zap.Logger
}

func NewPurgeExecutor(
	discord PurgeDiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	logger *zap.Logger,
) *PurgeExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PurgeExecutor{
		discord: discord,
		pool:    pool,
		replies: replies,
		logger:  logger,
	}
}

func (e *PurgeExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: purge: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
		zap.String("source_message_id", action.SourceMsgID),
		zap.String("bot_message_id", action.BotMessageID),
		zap.String("user_reply_message_id", action.UserReplyMessageID),
		zap.Int("requested_count", derefMessageCount(action.Parameters.MessageCount)),
	)

	purgePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0
	}
	if msg := gate(e.discord, e.replies, purgePermFn, "purge", action, "", true, false, false); msg != "" {
		return &TextResult{Text: msg}
	}

	count := 100
	if action.Parameters.MessageCount != nil {
		count = *action.Parameters.MessageCount
	}
	if count <= 0 {
		e.logger.Info("executor: purge: zero count, returning no_messages")

		e.deleteConfirmationFlowMessages(action)
		return &TextResult{
			Text:            e.render("purge", "no_messages", nil),
			AutoDeleteAfter: purgeAutoDeleteAfter,
			SkipReply:       true,
		}
	}
	if count > maxAllPurge {
		count = maxAllPurge
	}

	var userFilter *string
	if uid, err := extractUserTarget(action.Targets); err == nil {
		userFilter = &uid
	}
	e.logger.Info("executor: purge: parameters resolved",
		zap.Int("effective_count", count),
		zap.Bool("user_filter_active", userFilter != nil),
		func() zap.Field {
			if userFilter != nil {
				return zap.String("user_filter_id", *userFilter)
			}
			return zap.Skip()
		}(),
	)

	deleted, skipped, err := e.purgeMessages(
		action.ChannelID,
		action.SourceMsgID,
		count,
		userFilter,
	)
	if err != nil {
		e.logger.Error("executor: purge: failed",
			zap.Int("deleted", deleted),
			zap.Int("skipped", skipped),
			zap.Error(err),
		)
		return fmt.Errorf("executor: purge_messages: %w", err)
	}

	e.logger.Info("executor: purge: targeted sweep complete",
		zap.Int("deleted_user_messages", deleted),
		zap.Int("skipped_too_old", skipped),
		zap.Int("requested", count),
	)

	e.deleteConfirmationFlowMessages(action)

	if deleted == 0 {
		e.logger.Info("executor: purge: nothing targeted; rendering no_messages",
			zap.Int("requested", count),
		)
		return &TextResult{
			Text:            e.render("purge", "no_messages", nil),
			AutoDeleteAfter: purgeAutoDeleteAfter,
			SkipReply:       true,
		}
	}

	if err := audit.WriteAction(ctx, e.pool, audit.ModAction{
		GuildID:         action.GuildID,
		ChannelID:       action.ChannelID,
		ActorID:         action.ActorID,
		TargetID:        action.ChannelID,
		TargetType:      "message",
		Intent:          action.Intent,
		Reason:          orEmpty(action.Parameters.Reason),
		Parameters:      auditParameters(action, action.Parameters),
		SourceMessageID: action.SourceMsgID,
		ExecutedAt:      time.Now().UTC(),
	}); err != nil {
		e.logger.Error("executor: purge: audit write failed", zap.Error(err))
		return fmt.Errorf("executor: purge_messages: audit write: %w", err)
	}

	maxAgeDays := int(discordBulkDeleteMaxAge.Hours() / 24)
	vars := map[string]string{
		"deleted":      strconv.Itoa(deleted),
		"requested":    strconv.Itoa(count),
		"skipped":      strconv.Itoa(skipped),
		"max_age_days": strconv.Itoa(maxAgeDays),
		"messages":     pluralize("message", deleted),
	}

	var key string
	// "all" variants are for when the user asked to purge everything
	// (count >= maxAllPurge, i.e. 1000) AND we exhausted the channel
	// (deleted+skipped < count means there weren't enough eligible
	// messages to fill the request). This is the "purge everything
	// in the channel" semantic.
	//
	// success_all_skipped specifically means we deleted NOTHING — all
	// messages in the channel were too old. The template variants say
	// things like "Zero deleted" and "Total waste of time".
	//
	// For any case where we deleted SOME and skipped SOME (regardless
	// of whether it was an "all" purge or a specific count), use
	// success_partial_skipped — the template says "Deleted X of Y.
	// The other Z are older than..." which fits both scenarios.
	isAllPurge := count >= maxAllPurge && (deleted+skipped) < count
	switch {
	case isAllPurge && deleted == 0 && skipped > 0:
		key = "success_all_skipped"
	case isAllPurge && skipped == 0:
		key = "success_all"
	case skipped > 0:
		key = "success_partial_skipped"
	default:
		key = "success_partial"
	}

	text := e.render("purge", key, vars)
	e.logger.Info("executor: purge: rendered success notification",
		zap.Int("deleted", deleted),
		zap.Int("skipped", skipped),
		zap.Int("requested", count),
		zap.String("template_key", key),
		zap.String("rendered_text", text),
	)
	return &TextResult{
		Text:            text,
		AutoDeleteAfter: purgeAutoDeleteAfter,
		SkipReply:       true,
	}
}

func (e *PurgeExecutor) deleteConfirmationFlowMessages(action Action) {
	// Delete the three confirmation-flow messages: the original invoke
	// (SourceMsgID), the bot's confirmation prompt (BotMessageID), and the
	// user's text "yes" reply (UserReplyMessageID, if the text flow was
	// used rather than the button flow).
	//
	// These are deleted AFTER the bulk purge completes so that other users
	// in the channel see the invoke + confirmation while messages are
	// disappearing, giving them context for the purge. Only after the
	// purge is done do we clean up the command trail.
	if action.SourceMsgID != "" {
		if err := e.discord.DeleteMessage(action.ChannelID, action.SourceMsgID); err != nil {
			e.logger.Warn("executor: purge: deleting source invoke failed",
				zap.String("message_id", action.SourceMsgID),
				zap.Error(err),
			)
		} else {
			e.logger.Debug("executor: purge: source invoke deleted",
				zap.String("message_id", action.SourceMsgID),
			)
		}
	}
	if action.BotMessageID != "" {
		if err := e.discord.DeleteMessage(action.ChannelID, action.BotMessageID); err != nil {
			e.logger.Warn("executor: purge: deleting bot confirmation failed",
				zap.String("message_id", action.BotMessageID),
				zap.Error(err),
			)
		} else {
			e.logger.Debug("executor: purge: bot confirmation deleted",
				zap.String("message_id", action.BotMessageID),
			)
		}
	}
	if action.UserReplyMessageID != "" {
		if err := e.discord.DeleteMessage(action.ChannelID, action.UserReplyMessageID); err != nil {
			e.logger.Warn("executor: purge: deleting user reply failed",
				zap.String("message_id", action.UserReplyMessageID),
				zap.Error(err),
			)
		} else {
			e.logger.Debug("executor: purge: user reply deleted",
				zap.String("message_id", action.UserReplyMessageID),
			)
		}
	}
}

func derefMessageCount(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ScanChannel counts how many messages in the channel (before sourceMsgID)
// are deletable (< 14 days old) vs too old (>= 14 days), up to maxCount.
//
// Discord has no "count messages" endpoint, so we paginate ChannelMessages
// (100 per call). Optimization: Discord returns messages newest-first, and
// snowflakes are chronological. Once we hit a message older than 14 days,
// ALL subsequent messages are also too old — we stop scanning immediately.
// This makes the scan fast for channels with mostly-old content (the common
// case for "purge" requests).
//
// The scan is bounded by maxCount: we stop once deletable+skipped reaches
// maxCount, or once we hit too-old messages, or the channel is exhausted.
func (e *PurgeExecutor) ScanChannel(
	ctx context.Context,
	channelID, sourceMsgID string,
	maxCount int,
) (*PurgeScanResult, error) {
	deletable := 0
	skipped := 0
	beforeID := sourceMsgID

	for deletable+skipped < maxCount {
		msgs, err := e.discord.ChannelMessages(
			channelID, purgeBatchSize, beforeID, "", "",
		)
		if err != nil {
			return nil, fmt.Errorf("executor: purge scan: %w", err)
		}
		if len(msgs) == 0 {
			break
		}

		// Walk the batch newest-first. Once we encounter a too-old
		// message, every subsequent message in this batch and all
		// future batches is also too old (Discord returns newest-first,
		// snowflakes are chronological). Count the remaining too-old
		// messages in this batch and stop scanning.
		hitTooOld := false
		for _, msg := range msgs {
			if msg.ID == sourceMsgID {
				continue
			}
			if time.Since(msg.Timestamp) > discordBulkDeleteMaxAge {
				hitTooOld = true
				skipped++
				continue
			}
			if hitTooOld {
				// Shouldn't happen (too-old messages are contiguous),
				// but guard anyway.
				skipped++
				continue
			}
			deletable++
			if deletable+skipped >= maxCount {
				break
			}
		}

		if hitTooOld {
			break
		}
		if len(msgs) < purgeBatchSize {
			break
		}
		beforeID = msgs[len(msgs)-1].ID
	}

	return &PurgeScanResult{
		Deletable: deletable,
		Skipped:   skipped,
		Requested: maxCount,
	}, nil
}

func (e *PurgeExecutor) purgeMessages(
	channelID, sourceMsgID string,
	maxCount int,
	userFilter *string,
) (int, int, error) {
	e.logger.Info("executor: purge: purgeMessages starting",
		zap.String("channel_id", channelID),
		zap.String("source_message_id", sourceMsgID),
		zap.Int("max_count", maxCount),
		zap.Bool("user_filter_active", userFilter != nil),
	)

	totalDeleted := 0
	totalSkipped := 0
	beforeID := sourceMsgID
	batchNum := 0

	for totalDeleted < maxCount {
		batchNum++
		msgs, err := e.discord.ChannelMessages(
			channelID, purgeBatchSize, beforeID, "", "",
		)
		if err != nil {
			e.logger.Error("executor: purge: ChannelMessages failed",
				zap.Int("batch", batchNum),
				zap.Error(err),
			)
			return totalDeleted, totalSkipped, err
		}
		if len(msgs) == 0 {
			e.logger.Info("executor: purge: batch exhausted; no more messages",
				zap.Int("batch", batchNum),
			)
			break
		}

		var ids []string
		var batchSkipped int
		for _, msg := range msgs {
			if msg.ID == sourceMsgID {
				continue
			}
			if !matchesUserFilter(msg, userFilter) {
				continue
			}
			if time.Since(msg.Timestamp) > discordBulkDeleteMaxAge {
				batchSkipped++
				continue
			}
			ids = append(ids, msg.ID)
			if totalDeleted+len(ids) >= maxCount {
				break
			}
		}
		// Accumulate the batch's skipped count into the running total.
		// Without this, totalSkipped stays 0 and the template selection
		// logic picks success_partial instead of success_partial_skipped.
		totalSkipped += batchSkipped

		e.logger.Info("executor: purge: batch evaluated",
			zap.Int("batch", batchNum),
			zap.Int("messages_fetched", len(msgs)),
			zap.Int("ids_to_bulk_delete", len(ids)),
			zap.Int("batch_skipped_too_old", batchSkipped),
		)

		if len(ids) > 0 {
			if err := e.discord.ChannelMessagesBulkDelete(channelID, ids); err != nil {
				e.logger.Error("executor: purge: ChannelMessagesBulkDelete failed",
					zap.Int("batch", batchNum),
					zap.Int("requested_count", len(ids)),
					zap.Error(err),
				)
				return totalDeleted, totalSkipped, err
			}
			totalDeleted += len(ids)
			e.logger.Info("executor: purge: bulk delete ok from discord",
				zap.Int("batch", batchNum),
				zap.Int("requested_count", len(ids)),
				zap.Int("running_total_deleted", totalDeleted),
			)
		}

		if len(msgs) < purgeBatchSize {
			e.logger.Info("executor: purge: short batch, no further pagination needed",
				zap.Int("batch", batchNum),
			)
			break
		}
		beforeID = msgs[len(msgs)-1].ID
	}

	// NOTE: The source/invoking message is NOT deleted here. It is
	// deleted by deleteConfirmationFlowMessages (called from Execute
	// after this function returns) so that the invoke + confirmation
	// prompt remain visible in the channel WHILE the bulk delete is
	// happening. This gives other users context for why messages are
	// disappearing — they see the "delete 200 messages" command and
	// the confirmation prompt, then both get cleaned up after the
	// purge completes.

	e.logger.Info("executor: purge: purgeMessages finished",
		zap.Int("deleted", totalDeleted),
		zap.Int("skipped_too_old", totalSkipped),
		zap.Int("requested_max", maxCount),
		zap.Int("batches_processed", batchNum),
	)
	return totalDeleted, totalSkipped, nil
}

func matchesUserFilter(msg *discordgo.Message, userFilter *string) bool {
	if userFilter == nil || *userFilter == "" {
		return true
	}
	return msg.Author.ID == *userFilter
}

func pluralize(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (e *PurgeExecutor) render(category, key string, vars any) string {
	if e.replies == nil {
		return fmt.Sprintf("[%s.%s]", category, key)
	}
	return e.replies.Get(category, key, vars)
}

var _ Executor = (*PurgeExecutor)(nil)

// PreCheck runs the permission gate without executing the action. Returns ""
// if allowed, or the denial reply text. Called by the handler before showing
// the destructive confirmation prompt.
func (e *PurgeExecutor) PreCheck(_ context.Context, action Action) string {
	purgePermFn := func(_ string, guildPerms int64) bool {
		return guildPerms&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0
	}
	return gate(e.discord, e.replies, purgePermFn, "purge", action, "", true, false, false)
}
