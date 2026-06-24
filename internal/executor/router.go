package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bwmarrin/discordgo"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/replies"
	"github.com/0x4A4FRN/fine/internal/storage"
)

type Action struct {
	Intent             string
	Targets            []llm.Target
	Parameters         llm.Parameters
	GuildID            string
	ChannelID          string
	ActorID            string
	SourceMsgID        string
	BotMessageID       string
	UserReplyMessageID string
	Sudo               bool
}

type Executor interface {
	Execute(ctx context.Context, action Action) error
}

type ResponseExecutor interface {
	Executor
	ExecuteResponse(ctx context.Context, resp *llm.LLMResponse, meta ActionMeta) error
}

type ActionMeta struct {
	GuildID            string
	ChannelID          string
	ActorID            string
	SourceMsgID        string
	BotMessageID       string
	UserReplyMessageID string
	Sudo               bool
}

type MemberAPI interface {
	GuildMember(
		guildID, userID string,
		options ...discordgo.RequestOption,
	) (*discordgo.Member, error)
	GuildRoles(
		guildID string,
		options ...discordgo.RequestOption,
	) ([]*discordgo.Role, error)
	Guild(
		guildID string,
		options ...discordgo.RequestOption,
	) (*discordgo.Guild, error)
	BotUserID() string
}

type BanAPI interface {
	GuildBanCreate(guildID, userID, reason string, deleteMessageDays int) error
	GuildBanDelete(guildID, userID string) error
}

type KickAPI interface {
	GuildMemberDelete(guildID, userID string) error
}

type MemberEditAPI interface {
	GuildMemberEdit(guildID, userID string, data *discordgo.GuildMemberParams) error
	GuildMemberNickname(
		guildID, userID, nickname string,
		options ...discordgo.RequestOption,
	) error
}

type RoleAPI interface {
	GuildMemberRoleAdd(guildID, userID, roleID string) error
	GuildMemberRoleRemove(guildID, userID, roleID string) error
}

type ChannelMessageAPI interface {
	ChannelMessages(
		channelID string,
		limit int,
		beforeID, afterID, aroundID string,
	) ([]*discordgo.Message, error)
	ChannelMessagesBulkDelete(channelID string, messageIDs []string) error
	DeleteMessage(channelID, messageID string) error
}

type PinAPI interface {
	ChannelMessagePin(channelID, messageID string) error
	ChannelMessageUnpin(channelID, messageID string) error
}

type MessageSendAPI interface {
	ChannelMessageSendComplex(
		channelID string,
		data *discordgo.MessageSend,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type MessageEditComplexAPI interface {
	ChannelMessageEditComplex(
		data *discordgo.MessageEdit,
		options ...discordgo.RequestOption,
	) (*discordgo.Message, error)
}

type VoiceStateAPI interface {
	GuildMemberVoiceState(
		guildID, userID string,
		options ...discordgo.RequestOption,
	) (*discordgo.VoiceState, error)
}

type BotInfoAPI interface {
	HeartbeatLatency() time.Duration
	GuildCount() int
	TotalMemberCount() int
}

type DiscordAPI interface {
	MemberAPI
	BanAPI
	KickAPI
	MemberEditAPI
	RoleAPI
	ChannelMessageAPI
	PinAPI
	MessageSendAPI
	MessageEditComplexAPI
	VoiceStateAPI
	BotInfoAPI
}

type TextResult struct {
	Text            string
	AutoDeleteAfter time.Duration
	SkipReply       bool
}

func (t *TextResult) Error() string { return t.Text }

type Router struct {
	discord          DiscordAPI
	pool             audit.DB
	settingsDB       GuildSettingsDB
	replies          replies.Renderer
	settingsSnapshot *GuildSettingsSnapshot
	startedAt        time.Time
	buildInfo        BuildInfo
	logger           *zap.Logger
	executors        map[string]Executor
	snipeExecutor    *SnipeExecutor
	snipeStore       *storage.Store
	snipeUploader    storage.Uploader
}

type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
	GoVersion string
}

type Option func(*Router)

func WithLogger(l *zap.Logger) Option {
	return func(r *Router) {
		r.logger = l
	}
}

func WithGuildSettings(snap *GuildSettingsSnapshot, db GuildSettingsDB) Option {
	return func(r *Router) {
		r.settingsSnapshot = snap
		r.settingsDB = db
	}
}

func WithBuildInfo(b BuildInfo) Option {
	return func(r *Router) {
		r.buildInfo = b
	}
}

func WithSnipeExecutor(store *storage.Store, uploader storage.Uploader) Option {
	return func(r *Router) {
		r.snipeStore = store
		r.snipeUploader = uploader
	}
}

func NewRouter(
	discord DiscordAPI,
	pool audit.DB,
	replies replies.Renderer,
	startedAt time.Time,
	opts ...Option,
) *Router {
	r := &Router{
		discord:   discord,
		pool:      pool,
		replies:   replies,
		startedAt: startedAt,
		logger:    zap.NewNop(),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.logger == nil {
		r.logger = zap.NewNop()
	}
	r.registerExecutors()
	return r
}

func (r *Router) StartBackgroundWorkers() {
	if r.snipeExecutor != nil {
		r.snipeExecutor.StartPaginationSweeper()
	}
}

func (r *Router) Stop() {
	if r.snipeExecutor != nil {
		r.snipeExecutor.Stop()
	}
}

func (r *Router) Execute(ctx context.Context, action Action) error {
	r.logger.Info("router: Executing single action",
		zap.String("intent", action.Intent),
		zap.String("guild_id", action.GuildID),
		zap.String("channel_id", action.ChannelID),
		zap.String("actor_id", action.ActorID),
	)
	return r.Route(ctx, action)
}

func (r *Router) registerExecutors() {

	snipe := NewSnipeExecutor(r.discord, r.snipeStore, r.snipeUploader, r.replies, r.logger)
	r.snipeExecutor = snipe

	r.executors = map[string]Executor{
		"delete_message": NewDeleteMessageExecutor(r.discord, r.pool, r.replies, r.logger),
		"ban":            NewBanExecutor(r.discord, r.pool, r.replies, r.logger),
		"unban":          NewUnbanExecutor(r.discord, r.pool, r.replies, r.logger),
		"kick":           NewKickExecutor(r.discord, r.pool, r.replies, r.logger),
		"timeout":        NewTimeoutExecutor(r.discord, r.pool, r.replies, r.logger),
		"untimeout":      NewUntimeoutExecutor(r.discord, r.pool, r.replies, r.logger),
		"mute":           NewMuteExecutor(r.discord, r.pool, r.replies, r.logger),
		"unmute":         NewUnmuteExecutor(r.discord, r.pool, r.replies, r.logger),
		"deafen":         NewDeafenExecutor(r.discord, r.pool, r.replies, r.logger),
		"undeafen":       NewUndeafenExecutor(r.discord, r.pool, r.replies, r.logger),
		"set_nickname":   NewNicknameExecutor(r.discord, r.pool, r.replies, r.logger),
		"reset_nickname": NewNicknameExecutor(r.discord, r.pool, r.replies, r.logger),
		"add_role":       NewRoleExecutor(r.discord, r.pool, r.replies, r.logger),
		"remove_role":    NewRoleExecutor(r.discord, r.pool, r.replies, r.logger),
		"pin_message":    NewPinExecutor(r.discord, r.pool, r.replies, r.logger),
		"unpin_message":  NewUnpinExecutor(r.discord, r.pool, r.replies, r.logger),
		"purge_messages": NewPurgeExecutor(r.discord, r.pool, r.replies, r.logger),
		"toggle_setting": NewSettingExecutor(r.discord, r.settingsDB, r.settingsSnapshot, r.replies, r.logger),
		"ping":           NewPingExecutor(r.replies, r.logger),
		"help":           NewHelpExecutor(r.replies, r.logger),
		"info":           NewInfoExecutor(r.discord, r.replies, r.startedAt, r.buildInfo, r.logger),
		"status":         NewStatusExecutor(r.discord, r.pool, r.snipeUploader, r.replies, r.startedAt, r.logger),
		"snipe":          snipe,
	}
}

func (r *Router) Route(ctx context.Context, action Action) error {
	r.logger.Info("router: Routing intent",
		zap.String("intent", action.Intent),
	)

	exec, ok := r.executors[action.Intent]
	if !ok {
		if action.Intent == "" {
			r.logger.Warn("router: empty intent routed; no-op")
			return nil
		}
		r.logger.Error("router: unsupported intent",
			zap.String("intent", action.Intent),
		)
		return fmt.Errorf("executor: unsupported intent: %q", action.Intent)
	}
	return exec.Execute(ctx, action)
}

type MultiError struct {
	Succeeded []string
	Failed    []failedAction
}

type failedAction struct {
	Intent string
	Err    error
}

func (e *MultiError) Error() string {
	if len(e.Failed) == 0 && len(e.Succeeded) == 0 {
		return "no actions executed"
	}

	var b strings.Builder
	if len(e.Failed) == 0 {
		b.WriteString("Executed ")
		b.WriteString(strings.Join(e.Succeeded, ", "))
		return b.String()
	}

	if len(e.Succeeded) > 0 {
		b.WriteString("Executed ")
		b.WriteString(strings.Join(e.Succeeded, ", "))
		b.WriteString(", but failed ")
	} else {
		b.WriteString("Failed ")
	}

	for i, fa := range e.Failed {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(fa.Intent)
		b.WriteString(": ")
		b.WriteString(fa.Err.Error())
	}

	return b.String()
}

func (e *MultiError) HasFailures() bool {
	return len(e.Failed) > 0
}

func actionFromMeta(meta ActionMeta) Action {
	return Action{
		GuildID:            meta.GuildID,
		ChannelID:          meta.ChannelID,
		ActorID:            meta.ActorID,
		SourceMsgID:        meta.SourceMsgID,
		BotMessageID:       meta.BotMessageID,
		UserReplyMessageID: meta.UserReplyMessageID,
		Sudo:               meta.Sudo,
	}
}

func (r *Router) ExecuteResponse(
	ctx context.Context,
	resp *llm.LLMResponse,
	meta ActionMeta,
) error {
	r.logger.Info("router: ExecuteResponse entry",
		zap.String("intent", resp.Intent),
		zap.Bool("is_moderation", resp.IsModeration),
		zap.Int("actions_count", len(resp.Actions)),
	)

	if !resp.IsModeration {

		r.logger.Warn("router: ExecuteResponse received non-moderation; no-op",
			zap.String("intent", resp.Intent),
		)
		return nil
	}

	if len(resp.Actions) > 0 {
		return r.executeMultiAction(ctx, resp, meta)
	}

	action := actionFromMeta(meta)
	action.Intent = resp.Intent
	action.Targets = resp.Targets
	action.Parameters = resp.Parameters
	return r.Route(ctx, action)
}

func (r *Router) executeMultiAction(
	ctx context.Context,
	resp *llm.LLMResponse,
	meta ActionMeta,
) error {
	r.logger.Info("router: executing multi-action",
		zap.Int("count", len(resp.Actions)),
	)

	multiErr := &MultiError{}

	for _, a := range resp.Actions {
		action := actionFromMeta(meta)
		action.Intent = a.Intent
		action.Targets = a.Targets
		action.Parameters = a.Parameters
		if err := r.Route(ctx, action); err != nil {
			r.logger.Error("router: multi-action failed",
				zap.String("intent", a.Intent),
				zap.Error(err),
			)
			multiErr.Failed = append(multiErr.Failed, failedAction{
				Intent: a.Intent,
				Err:    err,
			})
		} else {
			r.logger.Info("router: multi-action succeeded",
				zap.String("intent", a.Intent),
			)
			multiErr.Succeeded = append(multiErr.Succeeded, a.Intent)
		}
	}

	if multiErr.HasFailures() {
		r.logger.Warn("router: multi-action completed with failures",
			zap.Int("succeeded", len(multiErr.Succeeded)),
			zap.Int("failed", len(multiErr.Failed)),
		)
		return multiErr
	}
	return nil
}

var (
	_ Executor         = (*Router)(nil)
	_ ResponseExecutor = (*Router)(nil)
)

func (r *Router) PreCheckPermission(ctx context.Context, resp *llm.LLMResponse, meta ActionMeta) string {
	exec, ok := r.executors[resp.Intent]
	if !ok {
		return ""
	}
	pc, ok := exec.(PreChecker)
	if !ok {
		return ""
	}
	action := actionFromMeta(meta)
	action.Intent = resp.Intent
	action.Targets = resp.Targets
	action.Parameters = resp.Parameters
	return pc.PreCheck(ctx, action)
}

func (r *Router) PurgeScan(ctx context.Context, channelID, sourceMsgID string, maxCount int) (*PurgeScanResult, error) {
	exec, ok := r.executors["purge_messages"]
	if !ok {
		return nil, fmt.Errorf("router: purge_messages executor not registered")
	}
	pe, ok := exec.(*PurgeExecutor)
	if !ok {
		return nil, fmt.Errorf("router: purge executor has unexpected type")
	}
	return pe.ScanChannel(ctx, channelID, sourceMsgID, maxCount)
}

func (r *Router) PreCheckActionPermission(ctx context.Context, action Action) string {
	exec, ok := r.executors[action.Intent]
	if !ok {
		return ""
	}
	pc, ok := exec.(PreChecker)
	if !ok {
		return ""
	}
	return pc.PreCheck(ctx, action)
}

func (r *Router) SnipePagination(ctx context.Context, botMessageID string, direction string) (*storage.Snapshot, string, []discordgo.MessageComponent) {
	if r.snipeExecutor == nil {
		return nil, "", nil
	}
	return r.snipeExecutor.HandlePagination(ctx, botMessageID, direction)
}

func (r *Router) SnipeSourceMsgID(botMessageID string) string {
	if r.snipeExecutor == nil {
		return ""
	}
	return r.snipeExecutor.SourceMsgID(botMessageID)
}

func (r *Router) SnipeDeletePage(botMessageID string) {
	if r.snipeExecutor == nil {
		return
	}
	r.snipeExecutor.DeletePage(botMessageID)
}
