package bot

import (
	"context"
	"fmt"
	"net/http"
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

type CacheStore interface {
	Get(ctx context.Context, guildID, template string) (*cache.CacheEntry, error)
	Set(ctx context.Context, guildID, template string, entry cache.CacheEntry) error
}

type VoiceStateAPI interface {
	GuildMemberVoiceState(
		guildID, userID string,
		opts ...discordgo.RequestOption,
	) (*discordgo.VoiceState, error)
}

type GuildMemberAPI interface {
	GuildMember(
		guildID, userID string,
		opts ...discordgo.RequestOption,
	) (*discordgo.Member, error)
}

type MessageEditComplexAPI interface {
	ChannelMessageEditComplex(
		data *discordgo.MessageEdit,
		opts ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type InteractionAPI interface {
	InteractionRespond(
		ic *discordgo.Interaction,
		resp *discordgo.InteractionResponse,
		opts ...discordgo.RequestOption,
	) error
	ChannelMessageDelete(channelID, messageID string, opts ...discordgo.RequestOption) error
}

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

	httpClient *http.Client
}

type Option func(*Handler)

func WithSystemPrompt(prompt string) Option {
	return func(h *Handler) {
		h.systemPrompt = prompt
	}
}

func WithDiscord(s DiscordSessionAPI) Option {
	return func(h *Handler) {
		h.discord = s

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
	if h.httpClient == nil {
		h.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if h.replies == nil {
		h.replies = replies.NopRenderer{}
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

	if h.windowDB != nil {
		if h.handlePendingConfirmation(ctx, m) {
			h.logger.Debug("handler: message consumed as confirmation reply")
			return
		}
	} else {
		h.logger.Debug("handler: confirmation check skipped (no windowDB)")
	}

	if !h.isMentionedOrReply(m) {
		h.logger.Debug("handler: not mentioned, no reply chain; dropping",
			zap.String("author_id", m.Author.ID),
		)
		return
	}

	cleaned := stripMention(m.Content, h.BotID())
	if cleaned == "" {
		h.logger.Debug("handler: cleaned content empty after stripping mention")
		return
	}

	if intent := matchBareUtilityCommand(cleaned); intent != "" {
		h.handleBareUtilityCommand(ctx, m, cleaned, intent)
		return
	}

	if h.cacheStore != nil {
		if h.handleCacheCheck(ctx, m, cleaned) {
			h.logger.Debug("handler: short-circuited via cache hit")
			return
		}
		h.logger.Debug("handler: cache miss")
	} else {
		h.logger.Debug("handler: cache check skipped (no cacheStore)")
	}

	h.handleLLMPath(ctx, m, cleaned)
}

func (h *Handler) isMentionedOrReply(m *discordgo.MessageCreate) bool {
	isMention := isMentioned(m.Mentions, h.BotID())
	isReply := !isMention && h.isReplyToBot(m.Message)
	if isMention || isReply {
		h.logger.Debug("handler: mention gate passed",
			zap.Bool("is_mention", isMention),
			zap.Bool("is_reply_to_bot", isReply),
		)
	}
	return isMention || isReply
}

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

	var ph *placeholder
	if intent == "status" {
		var stop func()
		ph, stop = h.startPlaceholder(m.ChannelID, m.ID, ctx)
		defer stop()
	}

	h.executeUtilityResponse(ctx, resp, m, ph)
}

func (h *Handler) handleLLMPath(
	ctx context.Context,
	m *discordgo.MessageCreate,
	cleaned string,
) {
	h.logger.Debug("handler: mention stripped",
		zap.String("cleaned_preview", truncateContent(cleaned, 120)),
		zap.Int("cleaned_len", len(cleaned)),
	)

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

	ph, stopPlaceholder := h.startPlaceholder(m.ChannelID, m.ID, ctx)
	defer stopPlaceholder()

	resp, err := h.ProcessMessageWithHistory(ctx, history, llmContent, replyTargetID)
	if err != nil {

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

	if resp.IsModeration && IsDestructive(resp.Intent) {
		if safety.IsNegation(cleaned) {
			h.logger.Info("handler: negation gate override",
				zap.String("intent", resp.Intent),
			)
			h.deletePlaceholderAndReply(ph, m.ChannelID, m.ID, h.negationReplyText())
			return
		}
	}

	isUtility := utilityIntents[resp.Intent]
	isEmptyIntent := resp.Intent == ""
	isAuditLookup := resp.Intent == "audit_lookup"

	if isAuditLookup {
		h.handleAuditLookupRoute(ctx, m, resp, ph)
		return
	}
	if (isEmptyIntent || !resp.IsModeration) && !isUtility {
		h.handleChatReply(ctx, m, resp, ph)
		return
	}
	if isUtility {
		h.executeUtilityResponse(ctx, resp, m, ph)
		return
	}

	h.dispatchModerationResponse(ctx, m, cleaned, resp, ph)
}

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
