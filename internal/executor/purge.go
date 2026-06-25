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

type PurgeScanResult struct {
	Deletable int
	Skipped   int
	Requested int
}

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

	if msg := gate(e.discord, e.replies, discordgo.PermissionManageMessages, "purge", action, "", true, false, false); msg != "" {
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

	if err := writeAudit(ctx, e.pool, e.logger, action, action.ChannelID, "message"); err != nil {
		return err
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
	for _, m := range []struct {
		label string
		id    string
	}{
		{"source invoke", action.SourceMsgID},
		{"bot confirmation", action.BotMessageID},
		{"user reply", action.UserReplyMessageID},
	} {
		if m.id == "" {
			continue
		}
		if err := e.discord.DeleteMessage(action.ChannelID, m.id); err != nil {
			e.logger.Warn("executor: purge: deleting "+m.label+" failed",
				zap.String("message_id", m.id),
				zap.Error(err),
			)
		} else {
			e.logger.Debug("executor: purge: "+m.label+" deleted",
				zap.String("message_id", m.id),
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
	e.logger.Info("executor: purge: starting",
		zap.String("channel_id", channelID),
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

		totalSkipped += batchSkipped

		e.logger.Debug("executor: purge: batch evaluated",
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
		}

		if len(msgs) < purgeBatchSize {
			break
		}
		beforeID = msgs[len(msgs)-1].ID
	}

	e.logger.Info("executor: purge: finished",
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

func (e *PurgeExecutor) PreCheck(_ context.Context, action Action) string {
	return gate(e.discord, e.replies, discordgo.PermissionManageMessages, "purge", action, "", true, false, false)
}
