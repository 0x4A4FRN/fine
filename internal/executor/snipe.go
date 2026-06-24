package executor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/replies"
	"github.com/0x4A4FRN/fine/internal/storage"
)

const snipeMaxCount = 25
const snipeDefaultCount = 1

const snipePageTTL = 10 * time.Minute

type snipePage struct {
	snaps       []storage.Snapshot
	currentIdx  int
	createdAt   time.Time
	sourceMsgID string
}

type SnipeMessageSendComplex interface {
	ChannelMessageSendComplex(
		channelID string,
		data *discordgo.MessageSend,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type SnipeMessageEditComplex interface {
	ChannelMessageEditComplex(
		data *discordgo.MessageEdit,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type SnipeDiscordAPI interface {
	SnipeMessageSendComplex
	SnipeMessageEditComplex
	MemberAPI
}

type SnipeStore interface {
	QueryDeleted(ctx context.Context, channelID string, limit int) ([]storage.Snapshot, error)
}

type SnipeExecutor struct {
	discord  SnipeDiscordAPI
	store    SnipeStore
	uploader storage.Uploader
	replies  replies.Renderer
	logger   *zap.Logger

	pages    map[string]*snipePage
	pagesMu  sync.Mutex
	cancel   context.CancelFunc
	cancelMu sync.Mutex
}

func NewSnipeExecutor(
	discord SnipeDiscordAPI,
	store SnipeStore,
	uploader storage.Uploader,
	r replies.Renderer,
	logger *zap.Logger,
) *SnipeExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SnipeExecutor{
		discord:  discord,
		store:    store,
		uploader: uploader,
		replies:  r,
		logger:   logger,
		pages:    make(map[string]*snipePage),
	}
}

func (e *SnipeExecutor) StartPaginationSweeper() {
	e.cancelMu.Lock()
	defer e.cancelMu.Unlock()
	if e.cancel != nil {

		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go e.runPaginationSweeper(ctx)
}

func (e *SnipeExecutor) Stop() {
	e.cancelMu.Lock()
	defer e.cancelMu.Unlock()
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
}

func (e *SnipeExecutor) runPaginationSweeper(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.evictExpiredPages()
		}
	}
}

func (e *SnipeExecutor) evictExpiredPages() {
	e.pagesMu.Lock()
	defer e.pagesMu.Unlock()
	now := time.Now()
	for id, p := range e.pages {
		if now.Sub(p.createdAt) > snipePageTTL {
			delete(e.pages, id)
		}
	}
}

func (e *SnipeExecutor) Execute(ctx context.Context, action Action) error {
	e.logger.Info("executor: snipe: executing",
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)

	if msg := e.checkSnipePermission(action); msg != "" {
		return &TextResult{Text: msg}
	}

	count := snipeDefaultCount
	if action.Parameters.MessageCount != nil && *action.Parameters.MessageCount > 0 {
		count = *action.Parameters.MessageCount
		if count > snipeMaxCount {
			count = snipeMaxCount
		}
	}

	snaps, err := e.store.QueryDeleted(ctx, action.ChannelID, count)
	if err != nil {
		e.logger.Error("executor: snipe: query failed", zap.Error(err))
		return replyTextFor(e.replies, "snipe", "query_failed")
	}

	if len(snaps) == 0 {
		e.logger.Info("executor: snipe: no deleted messages")
		return replyTextFor(e.replies, "snipe", "no_messages")
	}

	snap := snaps[0]
	text := e.renderSnipeText(ctx, &snap)

	msg, err := e.discord.ChannelMessageSendComplex(action.ChannelID, &discordgo.MessageSend{
		Content: text,
		Reference: &discordgo.MessageReference{
			MessageID:       action.SourceMsgID,
			ChannelID:       action.ChannelID,
			FailIfNotExists: func() *bool { b := false; return &b }(),
		},
	})
	if err != nil {
		e.logger.Error("executor: snipe: send failed", zap.Error(err))
		return fmt.Errorf("executor: snipe: %w", err)
	}

	hasPrev := len(snaps) > 1
	hasNext := false
	components := e.renderSnipeComponents(hasPrev, hasNext, msg.ID)

	if _, editErr := e.discord.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    action.ChannelID,
		ID:         msg.ID,
		Content:    &text,
		Components: &components,
	}); editErr != nil {
		e.logger.Warn("executor: snipe: edit to attach buttons failed",
			zap.String("message_id", msg.ID),
			zap.Error(editErr),
		)

	}

	e.pagesMu.Lock()
	e.pages[msg.ID] = &snipePage{
		snaps:       snaps,
		currentIdx:  0,
		createdAt:   time.Now(),
		sourceMsgID: action.SourceMsgID,
	}
	e.pagesMu.Unlock()

	e.logger.Info("executor: snipe: message sent",
		zap.String("message_id", msg.ID),
		zap.Int("snapshot_count", len(snaps)),
		zap.Int("count_param", count),
	)

	return nil
}

func (e *SnipeExecutor) checkSnipePermission(action Action) string {
	if e.discord == nil {
		return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
	}

	if action.GuildID != "" && action.ActorID != "" {
		guild, err := e.discord.Guild(action.GuildID)
		if err != nil || guild == nil {
			return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
		}
		if action.ActorID == guild.OwnerID {
			return ""
		}
	}

	member, err := e.discord.GuildMember(action.GuildID, action.ActorID)
	if err != nil || member == nil {
		return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
	}
	roles, err := e.discord.GuildRoles(action.GuildID)
	if err != nil || roles == nil {
		return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
	}

	perms := guildPermsForRoles(roles, member.Roles)
	if perms&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0 {
		return ""
	}
	return renderReply(e.replies, "snipe", "no_permission", nil)
}

func (e *SnipeExecutor) renderSnipeText(ctx context.Context, snap *storage.Snapshot) string {
	var b strings.Builder

	if e.replies != nil && e.replies.Has("snipe", "header") {
		headerVars := map[string]string{
			"author_id":  snap.AuthorID,
			"sent_ts":    strconv.FormatInt(snap.MessageTS.Unix(), 10),
			"deleted_ts": strconv.FormatInt(snap.DeletedAt.Unix(), 10),
		}
		b.WriteString(e.replies.Get("snipe", "header", headerVars))
	} else {
		fmt.Fprintf(&b, "**<@%s>**  | sent: <t:%d:F> | deleted: <t:%d:F>",
			snap.AuthorID,
			snap.MessageTS.Unix(),
			snap.DeletedAt.Unix(),
		)
	}

	if strings.TrimSpace(snap.Content) != "" {
		b.WriteString("\n\n")
		b.WriteString(snap.Content)
	}

	if len(snap.Attachments) > 0 {
		b.WriteString("\n")
		for i, att := range snap.Attachments {
			if i > 0 {
				b.WriteString("\n")
			}
			if att.S3Key != "" && e.uploader != nil {
				url, presignErr := e.uploader.Presign(ctx, att.S3Key, 15*time.Minute)
				if presignErr != nil {
					b.WriteString(renderReply(e.replies, "snipe", "attachment_unavailable", map[string]string{
						"filename": att.Filename,
					}))
				} else {
					b.WriteString(renderReply(e.replies, "snipe", "attachment", map[string]string{
						"filename": att.Filename,
						"url":      url,
					}))
				}
			} else {
				b.WriteString(renderReply(e.replies, "snipe", "attachment_unavailable", map[string]string{
					"filename": att.Filename,
				}))
			}
		}
	}

	text := b.String()
	if len(text) > 2000 {
		text = text[:2000-20] + "… (truncated)"
	}
	return text
}

func (e *SnipeExecutor) renderSnipeComponents(hasPrev, hasNext bool, botMessageID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "◀ Previous",
					Style:    discordgo.SecondaryButton,
					CustomID: "snipe_prev_" + botMessageID,
					Disabled: !hasPrev,
				},
				discordgo.Button{
					Label:    "Next ▶",
					Style:    discordgo.PrimaryButton,
					CustomID: "snipe_next_" + botMessageID,
					Disabled: !hasNext,
				},
				discordgo.Button{
					Label:    "🗑 Delete",
					Style:    discordgo.DangerButton,
					CustomID: "snipe_delete_" + botMessageID,
				},
			},
		},
	}
}

func (e *SnipeExecutor) renderSnipeMessage(ctx context.Context, snap *storage.Snapshot, hasPrev, hasNext bool, botMessageID string) (string, []discordgo.MessageComponent) {
	text := e.renderSnipeText(ctx, snap)
	components := e.renderSnipeComponents(hasPrev, hasNext, botMessageID)
	return text, components
}

func (e *SnipeExecutor) SourceMsgID(botMessageID string) string {
	e.pagesMu.Lock()
	defer e.pagesMu.Unlock()
	if page, ok := e.pages[botMessageID]; ok {
		return page.sourceMsgID
	}
	return ""
}

func (e *SnipeExecutor) DeletePage(botMessageID string) {
	e.pagesMu.Lock()
	delete(e.pages, botMessageID)
	e.pagesMu.Unlock()
}

func (e *SnipeExecutor) HandlePagination(
	ctx context.Context,
	botMessageID string,
	direction string,
) (*storage.Snapshot, string, []discordgo.MessageComponent) {
	e.pagesMu.Lock()
	page, ok := e.pages[botMessageID]
	if !ok {
		e.pagesMu.Unlock()
		e.logger.Warn("executor: snipe: pagination state expired or not found",
			zap.String("bot_message_id", botMessageID),
		)
		return nil, "", nil
	}

	var newIdx int
	switch direction {
	case "prev":
		newIdx = page.currentIdx + 1
	case "next":
		newIdx = page.currentIdx - 1
	default:
		e.pagesMu.Unlock()
		return nil, "", nil
	}
	if newIdx < 0 || newIdx >= len(page.snaps) {

		e.pagesMu.Unlock()
		return nil, "", nil
	}

	page.currentIdx = newIdx
	snap := page.snaps[newIdx]
	hasPrev := newIdx < len(page.snaps)-1
	hasNext := newIdx > 0
	e.pagesMu.Unlock()

	text, components := e.renderSnipeMessage(ctx, &snap, hasPrev, hasNext, botMessageID)
	return &snap, text, components
}

var _ Executor = (*SnipeExecutor)(nil)
