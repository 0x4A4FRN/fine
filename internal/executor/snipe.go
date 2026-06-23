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

// snipePageTTL is how long a snipe page state lives in memory after the
// snipe message is sent. Clicks arriving after this window will see
// "state expired" and the buttons will stop working. The TTL is generous
// (10 minutes) so users can scroll back through a snipe they invoked a
// few minutes ago; the background goroutine started by StartPaginationSweeper
// sweeps expired entries once a minute.
const snipePageTTL = 10 * time.Minute

// snipePage holds the list of messages displayed by a single snipe invocation.
// Stored on the SnipeExecutor (NOT at package scope — see Finding 1.4/5.2)
// keyed by the bot message ID so button clicks can navigate.
// The list is sorted by message_ts DESC (newest sent first), so:
//   - index 0 = most recently sent = "newest"
//   - last index = oldest sent
//
// "Next ▶" goes to a newer message (i-1); "◀ Previous" goes to an older
// message (i+1). This matches the natural reading direction once the sort
// order is fixed to message_ts DESC.
type snipePage struct {
	snaps       []storage.Snapshot
	currentIdx  int
	createdAt   time.Time
	sourceMsgID string // the invoking user message — used by the 🗑 Delete button to clean up the command too
}

// SnipeMessageSendComplex is the subset of Discord message send the
// SnipeExecutor needs — a complex send with components (buttons).
type SnipeMessageSendComplex interface {
	ChannelMessageSendComplex(
		channelID string,
		data *discordgo.MessageSend,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

// SnipeMessageEditComplex is the subset of Discord message edit the
// SnipeExecutor needs — used to attach the pagination buttons to the
// sent snipe message after the bot message ID (and therefore the button
// CustomIDs) are known.
type SnipeMessageEditComplex interface {
	ChannelMessageEditComplex(
		data *discordgo.MessageEdit,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

// SnipeDiscordAPI is the Discord API methods the SnipeExecutor needs.
// MemberAPI is included so the executor can verify the caller's guild
// permissions (server owner / Administrator / Manage Messages) before
// revealing deleted messages.
type SnipeDiscordAPI interface {
	SnipeMessageSendComplex
	SnipeMessageEditComplex
	MemberAPI
}

// SnipeStore is the narrow storage interface the SnipeExecutor needs.
// Defining it consumer-side (rather than accepting *storage.Store directly)
// lets tests substitute a single-method mock instead of standing up the full
// storage stack — see AGENTS.md "Discord API Interfaces" for the same
// pattern applied to DiscordAPI sub-interfaces.
type SnipeStore interface {
	QueryDeleted(ctx context.Context, channelID string, limit int) ([]storage.Snapshot, error)
}

// SnipeExecutor handles the snipe intent — viewing recently deleted
// messages in a channel with pagination buttons.
//
// The pagination cache (pages + pagesMu + cancel) is owned by the executor
// struct, NOT at package scope. The Router must call StartPaginationSweeper
// after construction to launch the TTL cleanup goroutine, and Stop during
// shutdown to cancel it. This makes the executor testable (no global state
// shared between test runs) and avoids goroutine leaks on shutdown.
type SnipeExecutor struct {
	discord  SnipeDiscordAPI
	store    SnipeStore
	uploader storage.Uploader
	replies  replies.Renderer
	logger   *zap.Logger

	// Pagination state (was package-level global; see Finding 1.4/5.2).
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

// StartPaginationSweeper launches the background goroutine that evicts
// expired snipe page entries. Must be called once after construction
// (typically by the Router). Safe to call multiple times — subsequent
// calls are no-ops.
func (e *SnipeExecutor) StartPaginationSweeper() {
	e.cancelMu.Lock()
	defer e.cancelMu.Unlock()
	if e.cancel != nil {
		// Already started.
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go e.runPaginationSweeper(ctx)
}

// Stop cancels the pagination sweeper goroutine. Safe to call multiple
// times and safe to call before StartPaginationSweeper (no-op).
func (e *SnipeExecutor) Stop() {
	e.cancelMu.Lock()
	defer e.cancelMu.Unlock()
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
}

// runPaginationSweeper is the goroutine body. Ticks once a minute and
// evicts page entries older than snipePageTTL. Exits when ctx is canceled.
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

	// Permission gate: only the server owner, Administrators, and members
	// with Manage Messages may invoke snipe. This is enforced here (not in
	// the handler) because snipe is a non-moderation utility intent and
	// therefore bypasses the moderation dispatch path's built-in gate().
	// The pagination button clicks (which re-enter via the interaction
	// handler) don't re-check this — but each click only navigates within
	// the snapshots already fetched by the originally-permission-checked
	// invocation, so no privilege escalation is possible.
	if msg := e.checkSnipePermission(action); msg != "" {
		return &TextResult{Text: msg}
	}

	// Determine count from parameters.
	count := snipeDefaultCount
	if action.Parameters.MessageCount != nil && *action.Parameters.MessageCount > 0 {
		count = *action.Parameters.MessageCount
		if count > snipeMaxCount {
			count = snipeMaxCount
		}
	}

	// Query deleted messages. Sorted by message_ts DESC (newest sent first).
	// Pass the caller's ctx so the 30-second handler timeout applies — using
	// context.Background() here would let a stalled DB query block the event
	// handler goroutine indefinitely (Finding 1.3).
	snaps, err := e.store.QueryDeleted(ctx, action.ChannelID, count)
	if err != nil {
		e.logger.Error("executor: snipe: query failed", zap.Error(err))
		return replyTextFor(e.replies, "snipe", "query_failed")
	}

	if len(snaps) == 0 {
		e.logger.Info("executor: snipe: no deleted messages")
		return replyTextFor(e.replies, "snipe", "no_messages")
	}

	// Render the initial text (we need the bot message ID for the button
	// CustomIDs, which we only get after sending — so we send the text
	// first, then edit the message to attach the buttons).
	snap := snaps[0]
	text := e.renderSnipeText(ctx, &snap)

	// Send the message as a reply to the invoking message. The placeholder
	// (if any) is deleted by the handler after this executor returns nil.
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

	// Now we have the bot message ID — render the components and edit the
	// message to attach the pagination buttons.
	//
	// Index 0 is the newest sent message:
	//   - hasNext (go to newer, i-1) is false at i=0 → disable "Next ▶"
	//   - hasPrev (go to older, i+1) is true iff N > 1 → disable "◀ Previous" when N == 1
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
		// Non-fatal — the message is sent with text but no buttons. The
		// page state is still stored so the message is at least visible.
	}

	// Store the page state keyed by the bot message ID.
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

	// Return nil — the executor sent the message directly. The handler
	// will delete the placeholder (if any) and leave this message in place.
	return nil
}

// checkSnipePermission verifies that the actor is allowed to invoke snipe.
// Allowed: server owner, Administrator, or Manage Messages. Returns "" if
// allowed; otherwise returns the denial reply text (rendered from
// replies.yaml's snipe.no_permission category, or a gateway error reply if
// the lookup itself failed).
//
// We don't use the shared gate() helper because (a) snipe has no target so
// the hierarchy / self-protection checks don't apply, and (b) the server
// owner must be allowed even if their @everyone role doesn't have the
// Administrator bit set locally — gate() would deny them in that case
// because it only inspects the role permission bitfield, not the ownership
// relation. Discord itself grants the owner all permissions at the API
// level, but our local bitfield check doesn't know that.
func (e *SnipeExecutor) checkSnipePermission(action Action) string {
	if e.discord == nil {
		return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
	}

	// Server owner bypass — owner is always allowed regardless of role perms.
	if action.GuildID != "" && action.ActorID != "" {
		guild, err := e.discord.Guild(action.GuildID)
		if err != nil || guild == nil {
			return renderReply(e.replies, "gateway", "cannot_verify_permissions", nil)
		}
		if action.ActorID == guild.OwnerID {
			return ""
		}
	}

	// Look up the actor's member record and role set.
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

// renderSnipeText formats the text body of a single deleted message snapshot
// (header, content, attachments). Truncated to Discord's 2000-char limit.
// All user-facing strings come from replies.yaml (snipe.header,
// snipe.attachment, snipe.attachment_unavailable) — see Finding 3.1/3.2.
func (e *SnipeExecutor) renderSnipeText(ctx context.Context, snap *storage.Snapshot) string {
	var b strings.Builder

	// Header line: bold mention + timestamps.
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

	// Content line (omit if empty).
	if strings.TrimSpace(snap.Content) != "" {
		b.WriteString("\n\n")
		b.WriteString(snap.Content)
	}

	// Attachment lines — presigned URLs if S3 is available. Format strings
	// come from replies.yaml so operators can restyle without redeploying
	// (Finding 3.2). Attachments are stacked on consecutive lines so
	// multiple files render as a clean list:
	//   📎 [photo.png](https://presigned-url...)
	//   📹 [video.mp4](https://presigned-url...)
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

	// Truncate to Discord's 2000-char limit if needed.
	text := b.String()
	if len(text) > 2000 {
		text = text[:2000-20] + "… (truncated)"
	}
	return text
}

// renderSnipeComponents builds the action row with pagination buttons.
//
// Semantics with message_ts DESC ordering (index 0 = newest sent):
//   - hasPrev: there is an older message (currentIdx < N-1). When false,
//     "◀ Previous" is disabled.
//   - hasNext: there is a newer message (currentIdx > 0). When false,
//     "Next ▶" is disabled.
//
// The botMessageID is embedded in the CustomIDs so the interaction
// handler can look up the in-memory page state.
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

// renderSnipeMessage formats a single deleted message snapshot with
// pagination buttons. Returns the text body and the components row.
// Convenience wrapper around renderSnipeText + renderSnipeComponents.
func (e *SnipeExecutor) renderSnipeMessage(ctx context.Context, snap *storage.Snapshot, hasPrev, hasNext bool, botMessageID string) (string, []discordgo.MessageComponent) {
	text := e.renderSnipeText(ctx, snap)
	components := e.renderSnipeComponents(hasPrev, hasNext, botMessageID)
	return text, components
}

// SourceMsgID returns the invoking user message ID for a given snipe bot
// message. Returns "" if the page state has expired or was never stored.
// Used by the 🗑 Delete button handler to clean up the original command
// message alongside the snipe result.
func (e *SnipeExecutor) SourceMsgID(botMessageID string) string {
	e.pagesMu.Lock()
	defer e.pagesMu.Unlock()
	if page, ok := e.pages[botMessageID]; ok {
		return page.sourceMsgID
	}
	return ""
}

// DeletePage removes the in-memory page state for a snipe message. Called
// when the user clicks 🗑 Delete so the state doesn't linger until TTL.
// Safe to call with an unknown ID (no-op).
func (e *SnipeExecutor) DeletePage(botMessageID string) {
	e.pagesMu.Lock()
	delete(e.pages, botMessageID)
	e.pagesMu.Unlock()
}

// HandlePagination processes a button click on a snipe message and
// returns the updated text and components for the adjacent message.
// Returns nil if at boundary, the page state has expired, or on error.
//
// The botMessageID is the Discord snowflake of the bot's snipe message
// (extracted from the button CustomID by the caller).
//
// Concurrency: pagesMu is held across the entire read-modify-write of
// page.currentIdx. The snapshot value (not pointer) is copied under the
// lock, and rendering happens outside the lock to avoid holding it during
// S3 presign calls. This prevents races between concurrent button clicks
// and the TTL sweeper goroutine.
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

	// Compute the new index. With message_ts DESC ordering:
	//   - "prev" (older) → i+1
	//   - "next" (newer) → i-1
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
		// Boundary — buttons should already be disabled, but guard anyway.
		e.pagesMu.Unlock()
		return nil, "", nil
	}

	// Mutate currentIdx under the lock, then copy the snapshot VALUE
	// (not pointer) so the renderer works on a stable copy even if the
	// sweeper evicts the page after we unlock.
	page.currentIdx = newIdx
	snap := page.snaps[newIdx] // copy
	hasPrev := newIdx < len(page.snaps)-1
	hasNext := newIdx > 0
	e.pagesMu.Unlock()

	// Render outside the lock — S3 presign can take 100ms+.
	text, components := e.renderSnipeMessage(ctx, &snap, hasPrev, hasNext, botMessageID)
	return &snap, text, components
}

var _ Executor = (*SnipeExecutor)(nil)
