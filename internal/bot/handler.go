package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/cache"
	"github.com/0x4A4FRN/fine/internal/conversation"
	"github.com/0x4A4FRN/fine/internal/executor"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
	"github.com/0x4A4FRN/fine/internal/safety"
	"github.com/0x4A4FRN/fine/internal/storage"
)

const handlerTimeout = 30 * time.Second

// cacheHitThreshold is now a configurable field on Handler (see WithCacheHitThreshold).

// placeholder is the live "Thinking…" message the bot posts immediately
// after a mention reaches the LLM-bound code path. The reply rotates
// every `placeholderRotationInterval` to a fresh random variant of
// `handler.on_receive` until the LLM resolves; at resolve time, the bot
// edits the placeholder in place to the final reply. The mutex
// serialises the rotation goroutine against the resolve-time edit so a
// final edit can never race a tick edit. Rotation is stopped via
// stop() (idempotent via stopOnce).

// noopStop is the placeholder stop function returned when the initial
// send failed. Calling it is harmless.

// startPlaceholder posts an immediate "Thinking…" reply (random variant
// from handler.on_receive) and starts a background goroutine that
// edits the reply to a fresh variant every `placeholderRotationInterval`.
// Returns a pointer to the placeholder struct (nil if the initial send
// failed) and a stop function. Pass the placeholder pointer to
// deletePlaceholderAndReply (done in HandleMessageCreate) to finalize
// to the actual reply. Stop is safe to call multiple times and is the
// caller's defer responsibility to halt the rotation goroutine.
//
// When the initial send fails, the returned ph is nil but the stop
// function is a no-op so callers can defer freely.

// runPlaceholderRotation is the goroutine body. It ticks, picks a fresh
// random variant, and edits the placeholder. Errors exit the loop so a
// placeholder that has been externally deleted does not keep firing.
//
// Concurrency: Go's `select` is fair — when both ctx.Done and ticker.C
// are ready, the goroutine may pick either. To prevent a final tick edit
// racily overwriting the resolve-time reply (handler finalize-edit at
// T_release, deferred stopFn cancel at T_cancel = T_release + ε, tick
// arriving at T_tick = T_release + 2s: by the time we hold the lock
// ctx may already be canceled), we re-check ctx.Err() AFTER acquiring
// the mutex. If ctx is canceled, we skip the edit and return. The
// handler's < stopFn waits for `ph.done`, so this return path is
// observable.

// deletePlaceholderAndReply finalises the placeholder to the given
// final text by deleting the rotating placeholder and sending a fresh
// reply to the invoking message. Returns the new bot message id.

// ConversationStore is the narrow interface the Handler needs from
// conversation.Store. Defining it consumer-side allows mock substitution
// in tests without a real Postgres pool.
type ConversationStore interface {
	WriteMessage(
		ctx context.Context,
		guildID, channelID, userID, role, content, discordMsgID string,
	) error
	GetHistory(
		ctx context.Context,
		guildID, channelID, userID string,
	) ([]conversation.Message, error)
}

// CacheStore is the narrow interface the Handler needs from cache.Store.
type CacheStore interface {
	Get(ctx context.Context, guildID, template string) (*cache.CacheEntry, error)
	Set(ctx context.Context, guildID, template string, entry cache.CacheEntry) error
}

// VoiceStateAPI provides voice state lookup for the voice-class pre-check
// in the moderation dispatch path. Defined consumer-side so the handler
// can be tested with a mock instead of a real *discord.Session.
type VoiceStateAPI interface {
	GuildMemberVoiceState(
		guildID, userID string,
		opts ...discordgo.RequestOption,
	) (*discordgo.VoiceState, error)
}

// GuildMemberAPI provides guild member lookup for the timeout tracker's
// OnGuildMemberUpdate handler (used to detect natural timeout expiry).
type GuildMemberAPI interface {
	GuildMember(
		guildID, userID string,
		opts ...discordgo.RequestOption,
	) (*discordgo.Member, error)
}

// MessageEditComplexAPI provides complex message edit for attaching
// confirmation buttons to a posted confirmation prompt.
type MessageEditComplexAPI interface {
	ChannelMessageEditComplex(
		data *discordgo.MessageEdit,
		opts ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

// InteractionAPI provides Discord interaction response + channel message
// delete for the button-click handler (snipe pagination + confirm yes/no).
type InteractionAPI interface {
	InteractionRespond(
		ic *discordgo.Interaction,
		resp *discordgo.InteractionResponse,
		opts ...discordgo.RequestOption,
	) error
	ChannelMessageDelete(channelID, messageID string, opts ...discordgo.RequestOption) error
}

// DiscordSessionAPI is the composite of all Discord API sub-interfaces the
// Handler needs beyond messageAPI. Each sub-interface is narrow (1-2
// methods) so tests can mock only the operations they exercise.
type DiscordSessionAPI interface {
	VoiceStateAPI
	GuildMemberAPI
	MessageEditComplexAPI
	InteractionAPI
}

type Handler struct {
	provider       llm.Provider
	systemPrompt   string
	discord        DiscordSessionAPI
	messageAPI     DiscordMessageAPI
	botIDMu        sync.RWMutex
	botID          string
	executor       executor.ResponseExecutor
	windowDB       WindowDB
	store          ConversationStore
	cacheStore     CacheStore
	auditDB        audit.DB
	replies        replies.Renderer
	timeoutTracker *timeoutTracker
	guildSettings  *executor.GuildSettingsSnapshot
	logger         *zap.Logger

	storageStore               *storage.Store
	storageUploader            storage.Uploader
	snipePaginationFn          func(ctx context.Context, botMessageID string, direction string) (*storage.Snapshot, string, []discordgo.MessageComponent)
	snipeSourceMsgIDFn         func(botMessageID string) string
	snipeDeletePageFn          func(botMessageID string)
	preCheckPermissionFn       func(ctx context.Context, resp *llm.LLMResponse, meta executor.ActionMeta) string
	preCheckActionPermissionFn func(ctx context.Context, action executor.Action) string
	purgeScanFn                func(ctx context.Context, channelID, sourceMsgID string, maxCount int) (*executor.PurgeScanResult, error)

	cacheHitThreshold     float64
	confirmWindowDuration time.Duration
}

// TrySettle atomically marks an entry as settled and reports whether the
// caller is the first settler. Returns true if the caller just marked the
// entry (and should perform the edit/audit); false if the entry was already
// settled by someone else (and should skip duplicate work).
//
// Concurrent callers (the GuildMemberUpdate listener and the polling
// expiry sweeper) use TrySettle as the dedupe gate so we never double-edit
// or write two audit rows for the same expiry event.

// Snapshot returns a copy of all currently tracked entries. Callers iterate
// the returned slice outside the tracker lock; concurrent updates from
// other code paths do not race with the snapshot read.

type Option func(*Handler)

func WithSystemPrompt(prompt string) Option {
	return func(h *Handler) {
		h.systemPrompt = prompt
	}
}

func WithDiscord(s DiscordSessionAPI) Option {
	return func(h *Handler) {
		h.discord = s
		// If the session also satisfies DiscordMessageAPI (the real
		// discord.Session does), wire it as the message API too.
		if ma, ok := s.(DiscordMessageAPI); ok {
			h.messageAPI = ma
		}
	}
}

func WithExecutor(e executor.ResponseExecutor) Option {
	return func(h *Handler) {
		h.executor = e
	}
}

func WithWindowDB(db WindowDB) Option {
	return func(h *Handler) {
		h.windowDB = db
	}
}

func WithConversationStore(s ConversationStore) Option {
	return func(h *Handler) {
		h.store = s
	}
}

func WithCacheStore(s CacheStore) Option {
	return func(h *Handler) {
		h.cacheStore = s
	}
}

func WithAuditDB(db audit.DB) Option {
	return func(h *Handler) {
		h.auditDB = db
	}
}

func WithReplies(r replies.Renderer) Option {
	return func(h *Handler) {
		h.replies = r
	}
}

func WithGuildSettings(snap *executor.GuildSettingsSnapshot) Option {
	return func(h *Handler) {
		h.guildSettings = snap
	}
}

func WithLogger(l *zap.Logger) Option {
	return func(h *Handler) {
		h.logger = l
	}
}

func WithCacheHitThreshold(f float64) Option {
	return func(h *Handler) {
		h.cacheHitThreshold = f
	}
}

func WithConfirmWindowDuration(d time.Duration) Option {
	return func(h *Handler) {
		h.confirmWindowDuration = d
	}
}

func WithStorageStore(s *storage.Store) Option {
	return func(h *Handler) {
		h.storageStore = s
	}
}

func WithStorageUploader(u storage.Uploader) Option {
	return func(h *Handler) {
		h.storageUploader = u
	}
}

func WithSnipePaginationFn(fn func(ctx context.Context, botMessageID string, direction string) (*storage.Snapshot, string, []discordgo.MessageComponent)) Option {
	return func(h *Handler) {
		h.snipePaginationFn = fn
	}
}

func WithSnipeSourceMsgIDFn(fn func(botMessageID string) string) Option {
	return func(h *Handler) {
		h.snipeSourceMsgIDFn = fn
	}
}

func WithSnipeDeletePageFn(fn func(botMessageID string)) Option {
	return func(h *Handler) {
		h.snipeDeletePageFn = fn
	}
}

func WithPreCheckPermissionFn(fn func(ctx context.Context, resp *llm.LLMResponse, meta executor.ActionMeta) string) Option {
	return func(h *Handler) {
		h.preCheckPermissionFn = fn
	}
}

func WithPreCheckActionPermissionFn(fn func(ctx context.Context, action executor.Action) string) Option {
	return func(h *Handler) {
		h.preCheckActionPermissionFn = fn
	}
}

func WithPurgeScanFn(fn func(ctx context.Context, channelID, sourceMsgID string, maxCount int) (*executor.PurgeScanResult, error)) Option {
	return func(h *Handler) {
		h.purgeScanFn = fn
	}
}

func NewHandler(provider llm.Provider, opts ...Option) *Handler {
	h := &Handler{
		provider:       provider,
		systemPrompt:   llm.DefaultSystemPrompt(),
		timeoutTracker: newTimeoutTracker(),
		logger:         zap.NewNop(),
	}

	for _, opt := range opts {
		opt(h)
	}

	if h.logger == nil {
		h.logger = zap.NewNop()
	}
	if h.cacheHitThreshold == 0 {
		h.cacheHitThreshold = 0.7
	}
	if h.confirmWindowDuration == 0 {
		h.confirmWindowDuration = 60 * time.Second
	}

	return h
}

func (h *Handler) SetBotID(id string) {
	h.botIDMu.Lock()
	defer h.botIDMu.Unlock()
	h.botID = id
}

func (h *Handler) BotID() string {
	h.botIDMu.RLock()
	defer h.botIDMu.RUnlock()
	return h.botID
}

func truncateContent(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// HandleMessageCreate is the entry point for all Discord MESSAGE_CREATE
// events. It is a thin orchestrator that delegates each distinct concern
// to a named helper method. The ordering of the helpers matters — see the
// comments inline.
//
// If you add a new concern, extract it into its own method rather than
// inlining it here. This function should stay under ~40 lines.
func (h *Handler) HandleMessageCreate(
	_ *discordgo.Session,
	m *discordgo.MessageCreate,
) {
	h.logger.Info("handler: message received",
		zap.String("channel_id", m.ChannelID),
		zap.String("guild_id", m.GuildID),
		zap.String("author_id", m.Author.ID),
		zap.Int("content_len", len(m.Content)),
		zap.String("content_preview", truncateContent(m.Content, 120)),
	)

	if m.Author.Bot {
		h.logger.Debug("handler: skipping bot author",
			zap.String("author_id", m.Author.ID),
		)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

	// 1. Pending-confirmation window (text-based yes/no flow).
	if h.windowDB != nil {
		if h.handlePendingConfirmation(ctx, m) {
			h.logger.Debug("handler: message consumed as confirmation reply")
			return
		}
	} else {
		h.logger.Debug("handler: confirmation check skipped (no windowDB)")
	}

	// 2. Mention gate.
	if !h.isMentionedOrReply(m) {
		h.logger.Debug("handler: not mentioned, no reply chain; dropping",
			zap.String("author_id", m.Author.ID),
		)
		return
	}

	// 3. Strip mention.
	cleaned := stripMention(m.Content, h.BotID())
	if cleaned == "" {
		h.logger.Debug("handler: cleaned content empty after stripping mention")
		return
	}

	// 4. Bare utility command (ping/help/info/status/snipe) — bypasses LLM.
	if intent := matchBareUtilityCommand(cleaned); intent != "" {
		h.handleBareUtilityCommand(ctx, m, cleaned, intent)
		return
	}

	// 5. Intent cache lookup.
	if h.cacheStore != nil {
		if h.handleCacheCheck(ctx, m, cleaned) {
			h.logger.Debug("handler: short-circuited via cache hit")
			return
		}
		h.logger.Debug("handler: cache miss")
	} else {
		h.logger.Debug("handler: cache check skipped (no cacheStore)")
	}

	// 6. Full LLM path.
	h.handleLLMPath(ctx, m, cleaned)
}

// isMentionedOrReply returns true if the bot was directly mentioned in the
// message OR the message is a reply to one of the bot's recent messages.
// The reply-chain check is bounded by replyWindow (5 minutes).
func (h *Handler) isMentionedOrReply(m *discordgo.MessageCreate) bool {
	mentioned := isMentioned(m.Mentions, h.BotID())
	if !mentioned {
		mentioned = h.isReplyToBot(m.Message)
	}
	if mentioned {
		h.logger.Debug("handler: mention gate passed",
			zap.Bool("is_mention", isMentioned(m.Mentions, h.BotID())),
			zap.Bool("is_reply_to_bot", mentioned && !isMentioned(m.Mentions, h.BotID())),
		)
	}
	return mentioned
}

// handleBareUtilityCommand builds an LLMResponse for a bare utility command
// (ping/help/info/status/snipe) and dispatches it through the utility path.
// The LLM is bypassed entirely. For snipe, the count parameter is parsed
// from the command text.
func (h *Handler) handleBareUtilityCommand(
	ctx context.Context,
	m *discordgo.MessageCreate,
	cleaned, intent string,
) {
	resp := &llm.LLMResponse{
		Intent:       intent,
		Confidence:   1.0,
		IsModeration: false,
		Reasoning:    "bare utility command; bypassing LLM",
	}
	if intent == "snipe" {
		count := parseSnipeCount(cleaned)
		resp.Parameters.MessageCount = &count
		h.logger.Info("handler: bare snipe command; parsed count",
			zap.Int("count", count),
			zap.String("cleaned_preview", truncateContent(cleaned, 120)),
		)
	} else {
		h.logger.Info("handler: bare utility command; bypassing LLM",
			zap.String("intent", intent),
			zap.String("cleaned_preview", truncateContent(cleaned, 120)),
		)
	}

	if h.store != nil {
		if err := h.store.WriteMessage(
			ctx, m.GuildID, m.ChannelID, m.Author.ID,
			"user", cleaned, m.ID,
		); err != nil {
			h.logger.Error("handler: writing user message", zap.Error(err))
		}
	}

	// Start a rotating placeholder for status since it does multiple
	// round-trips (gateway, DB, S3) that can take a moment. Other
	// utility commands (ping, help, info) are fast enough to not
	// need one. Snipe gets its own message and doesn't need a
	// placeholder (the executor sends its result directly).
	var ph *placeholder
	if intent == "status" {
		var stop func()
		ph, stop = h.startPlaceholder(m.ChannelID, m.ID, ctx)
		defer stop()
	}

	h.executeUtilityResponse(ctx, resp, m, ph)
}

// handleLLMPath is the full LLM-bound flow: conversation history retrieval,
// placeholder lifecycle, LLM round-trip, response validation, and dispatch
// to the appropriate downstream handler (chat reply, utility, audit lookup,
// or moderation dispatch).
func (h *Handler) handleLLMPath(
	ctx context.Context,
	m *discordgo.MessageCreate,
	cleaned string,
) {
	h.logger.Debug("handler: mention stripped",
		zap.String("cleaned_preview", truncateContent(cleaned, 120)),
		zap.Int("cleaned_len", len(cleaned)),
	)

	// Retrieve conversation history for LLM context.
	var history []llm.Message
	if h.store != nil {
		msgs, err := h.store.GetHistory(ctx, m.GuildID, m.ChannelID, m.Author.ID)
		if err != nil {
			h.logger.Error("handler: retrieving history",
				zap.String("author_id", m.Author.ID),
				zap.Error(err),
			)
		} else {
			history = toLLMMessages(msgs)
			h.logger.Debug("handler: history retrieved",
				zap.Int("messages", len(history)),
			)
		}
	}

	validationReplyText := h.cloudyReplyText()
	replyTargetID := extractReplyTargetID(m)

	// Compose the LLM-bound content with an actor prefix so the model has
	// the actor's Discord snowflake available for pronoun-driven self-targets
	// (e.g. "set my nickname to X") without us baking it into the system
	// prompt. The conversation store still receives `cleaned` so future
	// turns read human-readable history, not actor-prefixed text.
	llmContent := cleaned
	if m.Author != nil && m.Author.ID != "" {
		llmContent = fmt.Sprintf("[actor:%s] %s", m.Author.ID, cleaned)
	}

	h.logger.Info("handler: sending to LLM",
		zap.Int("history_len", len(history)),
		zap.String("cleaned_preview", truncateContent(cleaned, 200)),
		zap.Bool("has_reply_target", replyTargetID != ""),
	)
	h.logger.Debug("handler: LLM content bound",
		zap.String("actor_id", m.Author.ID),
		zap.Int("content_len", len(llmContent)),
		zap.String("content_preview", truncateContent(llmContent, 200)),
	)

	// Post a rotating "Thinking…" placeholder before the LLM round-trip
	// so the user sees the bot is alive while the model processes.
	ph, stopPlaceholder := h.startPlaceholder(m.ChannelID, m.ID, ctx)
	defer stopPlaceholder()

	resp, err := h.ProcessMessageWithHistory(ctx, history, llmContent, replyTargetID)
	if err != nil {
		// Distinguish context-canceled (handler timeout) from other
		// errors so operators can diagnose slow LLM providers.
		if ctx.Err() != nil {
			h.logger.Warn("handler: LLM processing timed out",
				zap.String("cleaned_preview", truncateContent(cleaned, 120)),
				zap.Duration("timeout", handlerTimeout),
				zap.Error(err),
			)
		} else {
			h.logger.Error("handler: LLM processing failed",
				zap.String("cleaned_preview", truncateContent(cleaned, 120)),
				zap.Error(err),
			)
		}
		h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, validationReplyText)
		return
	}

	// Implicit delete via reply chain: if the user replied to a message
	// with just "delete" (or a synonym), treat it as a delete_message intent.
	if resp.Intent == "" && replyTargetID != "" && isImplicitDeleteText(cleaned) {
		h.logger.Info("handler: implicit delete via reply chain",
			zap.String("reply_target_id", replyTargetID),
			zap.String("cleaned_preview", truncateContent(cleaned, 120)),
		)
		resp.Intent = "delete_message"
		resp.IsModeration = true
		resp.Confidence = 1.0
		resp.Targets = []llm.Target{
			{Type: "message", ID: replyTargetID},
		}
		resp.Reasoning = "implicit delete via reply chain"
	}

	if applyModerationOverride(resp) {
		h.logger.Info("handler: forced is_moderation=true for destructive intent",
			zap.String("intent", resp.Intent),
		)
	}

	h.logger.Info("handler: LLM classification received",
		zap.String("intent", resp.Intent),
		zap.Bool("is_moderation", resp.IsModeration),
		zap.Float64("confidence", resp.Confidence),
		zap.Bool("reply_present", resp.Reply != nil),
	)

	if h.store != nil {
		if err := h.store.WriteMessage(
			ctx, m.GuildID, m.ChannelID, m.Author.ID,
			"user", cleaned, m.ID,
		); err != nil {
			h.logger.Error("handler: writing user message", zap.Error(err))
		}
	}

	// Negation gate: if the user said "don't" / "never" / "cancel" / "abort"
	// in a destructive request, abort instead of executing.
	if resp.IsModeration && IsDestructive(resp.Intent) {
		if safety.IsNegation(cleaned) {
			h.logger.Info("handler: negation gate override",
				zap.String("intent", resp.Intent),
			)
			h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, h.negationReplyText())
			return
		}
	}

	// Dispatch based on response classification.
	// An empty intent means the LLM identified no moderation action —
	// treat it as chat regardless of the is_moderation flag (which
	// may be mis-set by the LLM). Without this, an empty-intent +
	// is_moderation=true response falls through to the moderation
	// dispatch, which no-ops and the LLM's chat reply is lost.
	isUtility := utilityIntents[resp.Intent]
	isEmptyIntent := resp.Intent == ""
	if (isEmptyIntent || !resp.IsModeration) && !isUtility {
		h.handleChatReply(ctx, m, resp, ph)
		return
	}
	if isUtility {
		h.executeUtilityResponse(ctx, resp, m, ph)
		return
	}
	if resp.Intent == "audit_lookup" {
		h.handleAuditLookupRoute(ctx, m, resp, ph)
		return
	}

	h.dispatchModerationResponse(ctx, m, cleaned, resp, ph)
}

// handleChatReply sends a non-moderation, non-utility chat reply from the
// LLM. If the reply is empty, the placeholder is left in place (no-op).
func (h *Handler) handleChatReply(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
	ph *placeholder,
) {
	if resp.Reply != nil && *resp.Reply != "" {
		h.logger.Info("handler: sending chat reply",
			zap.String("channel_id", m.ChannelID),
			zap.Int("reply_len", len(*resp.Reply)),
		)
		h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, *resp.Reply)
	} else {
		h.logger.Debug("handler: non-moderation response with empty Reply; no-op",
			zap.String("intent", resp.Intent),
		)
	}
	if h.store != nil && resp.Reply != nil && *resp.Reply != "" {
		h.writeAssistantMessage(ctx, m, *resp.Reply)
	}
}

// handleAuditLookupRoute dispatches an audit_lookup intent to the audit
// handler with the appropriate logging context.
func (h *Handler) handleAuditLookupRoute(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
	ph *placeholder,
) {
	h.logger.Info("handler: audit lookup",
		zap.String("info", func() string {
			if resp.AuditQuery != nil {
				return resp.AuditQuery.Info
			}
			return ""
		}()),
	)
	h.handleAuditLookup(ctx, m, resp, ph)
}

// dispatchModerationResponse is the single dispatch point for moderation
// responses, used by both the LLM classification path and the cache-hit
// path. It handles negation gate (already checked by caller for LLM path),
// sudo bypass, multi-action confirmation, destructive confirmation with
// voice pre-check, and default execution.
func (h *Handler) dispatchModerationResponse(
	ctx context.Context,
	m *discordgo.MessageCreate,
	cleaned string,
	resp *llm.LLMResponse,
	ph *placeholder,
) {
	sudo, verbose := h.guildSettingsFor(m.GuildID)

	if resp.Intent != "toggle_setting" {
		h.maybeWriteCache(ctx, m.GuildID, cleaned, resp)
	}

	if resp.Intent == "toggle_setting" {
		h.logger.Info("handler: toggle_setting direct route",
			zap.String("guild_id", m.GuildID),
		)
		h.runAndRecord(ctx, m, resp, ph, false, verbose)
		return
	}

	sudoBypassNeeded := sudo && (IsDestructive(resp.Intent) ||
		len(resp.Actions) > 1)
	if sudoBypassNeeded {
		h.logger.Info("handler: sudo bypass (single or multi-action)",
			zap.String("intent", resp.Intent),
			zap.Int("actions", len(resp.Actions)),
		)
		h.runAndRecord(ctx, m, resp, ph, true, verbose)
		return
	}

	if len(resp.Actions) > 1 {
		h.logger.Info("handler: multi-action confirmation",
			zap.Int("action_count", len(resp.Actions)),
		)
		// Pre-check permissions for ALL actions before showing the
		// confirmation. If any action lacks permission, deny the entire
		// batch immediately — don't make the user click "Yes" only to
		// be told they can't.
		if msg := h.preCheckMultiActionPermissions(ctx, m, resp); msg != "" {
			if ph != nil {
				h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, msg)
			} else {
				h.sendReply(m.ChannelID, msg, m.ID)
			}
			return
		}
		h.handleMultiActionConfirmation(ctx, m, resp, ph)
		return
	}

	if IsDestructive(resp.Intent) {
		h.logger.Info("handler: destructive confirmation",
			zap.String("intent", resp.Intent),
		)

		// Pre-check permission BEFORE showing the confirmation prompt.
		// Users who lack permission get denied immediately instead of
		// clicking "Yes" only to be told they can't.
		if msg := h.preCheckSingleActionPermission(ctx, m, resp); msg != "" {
			h.logger.Info("handler: permission pre-check blocked destructive confirmation",
				zap.String("intent", resp.Intent),
			)
			if ph != nil {
				h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, msg)
			} else {
				h.sendReply(m.ChannelID, msg, m.ID)
			}
			return
		}

		if msg := h.voiceClassPreCheck(resp, m); msg != "" {
			h.logger.Info("handler: voice pre-check blocked destructive confirmation",
				zap.String("intent", resp.Intent),
			)
			if ph != nil {
				h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, msg)
			} else {
				h.sendReply(m.ChannelID, msg, m.ID)
			}
			return
		}

		h.handleDestructiveConfirmation(ctx, m, resp, ph)
		return
	}

	h.logger.Info("handler: executing action",
		zap.String("intent", resp.Intent),
	)
	h.runAndRecord(ctx, m, resp, ph, false, verbose)
}

// runAndRecord executes the response and writes the assistant message to
// the conversation store. Consolidates the repeated
// executeResponse + writeAssistantMessage pattern.
func (h *Handler) runAndRecord(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
	ph *placeholder,
	sudo bool,
	verbose bool,
) {
	h.executeResponse(ctx, resp, m, sudo, verbose, ph)
	if h.store != nil {
		h.writeAssistantMessage(ctx, m, buildAssistantOutcome(resp))
	}
}

// preCheckSingleActionPermission runs the permission gate for a single-action
// destructive response. Returns "" if allowed, or the denial reply text.
func (h *Handler) preCheckSingleActionPermission(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
) string {
	if h.preCheckPermissionFn == nil {
		return ""
	}
	meta := executor.ActionMeta{
		GuildID:     m.GuildID,
		ChannelID:   m.ChannelID,
		ActorID:     m.Author.ID,
		SourceMsgID: m.ID,
	}
	return h.preCheckPermissionFn(ctx, resp, meta)
}

// preCheckMultiActionPermissions runs the permission gate for every action
// in a multi-action response. Returns "" if ALL actions are allowed, or the
// first denial reply text if any action lacks permission.
func (h *Handler) preCheckMultiActionPermissions(
	ctx context.Context,
	m *discordgo.MessageCreate,
	resp *llm.LLMResponse,
) string {
	if h.preCheckActionPermissionFn == nil {
		return ""
	}
	for _, a := range resp.Actions {
		action := executor.Action{
			Intent:      a.Intent,
			Targets:     a.Targets,
			Parameters:  a.Parameters,
			GuildID:     m.GuildID,
			ChannelID:   m.ChannelID,
			ActorID:     m.Author.ID,
			SourceMsgID: m.ID,
		}
		if msg := h.preCheckActionPermissionFn(ctx, action); msg != "" {
			return msg
		}
	}
	return ""
}
