package bot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/discord"
)

const discordEpochMillis int64 = 1420070400000

const defaultAuditFetchDelay = 400 * time.Millisecond

// auditRetryInterval is the gap between successive audit-log polls when the
// first fetch returns no matching entry. Discord's audit log API has a
// replication lag (typically 500ms-2s, occasionally longer for native UI
// actions) between when an action happens and when the entry becomes visible
// via REST. A single fetch at auditDelay was missing entries for native
// nickname changes via the right-click UI flow.
//
// Value chosen to stay under Discord's 5-requests-per-10s-per-guild rate
// limit on the audit log endpoint: with auditDelay=400ms and a 10s budget,
// this yields ~7 polls, leaving headroom for the periodic audit log reads
// other parts of the bot may issue.
const auditRetryInterval = 1500 * time.Millisecond

type ResolvedActor struct {
	ActorID    string
	ActorIsBot bool
	ActorName  string
	Source     string
}

type ExternalAudit struct {
	db         audit.DB
	session    *discord.Session
	buffer     *MessageBuffer
	auditDelay time.Duration
	logger     *zap.Logger
}

type NewExternalAuditOption func(*ExternalAudit)

func WithExternalAuditLogger(l *zap.Logger) NewExternalAuditOption {
	return func(e *ExternalAudit) { e.logger = l }
}

func WithExternalAuditDelay(d time.Duration) NewExternalAuditOption {
	return func(e *ExternalAudit) {
		if d > 0 {
			e.auditDelay = d
		}
	}
}

func NewExternalAudit(
	db audit.DB,
	s *discord.Session,
	buf *MessageBuffer,
	opts ...NewExternalAuditOption,
) *ExternalAudit {
	e := &ExternalAudit{
		db:         db,
		session:    s,
		buffer:     buf,
		auditDelay: defaultAuditFetchDelay,
		logger:     zap.NewNop(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func SnowflakeTime(snowflake string) (time.Time, bool) {
	id, err := strconv.ParseInt(snowflake, 10, 64)
	if err != nil || id <= 0 {
		return time.Time{}, false
	}
	millis := (id >> 22) + discordEpochMillis
	return time.UnixMilli(millis), true
}

var (
	snowflakeRegex = regexp.MustCompile(`\b(\d{17,20})\b`)
	mentionRegex   = regexp.MustCompile(`<@!?(\d{17,20})>`)
	byNameRegex    = regexp.MustCompile(`(?i)\bby\s+@?([A-Za-z0-9_.]{2,32})`)
	namePipeRegex  = regexp.MustCompile(`(?i)([A-Za-z0-9_.]{2,32})\s*\|`)
)

type reasonExtract struct {
	ID   string
	Name string
}

func parseActorFromReason(reason string) (reasonExtract, bool) {
	if reason == "" {
		return reasonExtract{}, false
	}
	if m := mentionRegex.FindStringSubmatch(reason); m != nil {
		return reasonExtract{ID: m[1]}, true
	}
	if m := snowflakeRegex.FindStringSubmatch(reason); m != nil {
		return reasonExtract{ID: m[1]}, true
	}
	if m := byNameRegex.FindStringSubmatch(reason); m != nil {
		return reasonExtract{Name: m[1]}, true
	}
	if m := namePipeRegex.FindStringSubmatch(reason); m != nil {
		return reasonExtract{Name: m[1]}, true
	}
	return reasonExtract{}, false
}

func (e *ExternalAudit) fetchAuditLog(
	ctx context.Context,
	guildID, targetID string,
	actionType discordgo.AuditLogAction,
	eventTime time.Time,
) (*discordgo.AuditLogEntry, error) {
	if e.session == nil {
		return nil, errors.New("external_audit: no discord session")
	}

	// Initial settle delay so Discord has a chance to write the audit log
	// entry before our first read. Subsequent retries use auditRetryInterval.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(e.auditDelay):
	}

	for {
		entry, err := e.fetchAndMatchAuditEntry(ctx, guildID, targetID, actionType, eventTime)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			return entry, nil
		}

		// No match yet. Wait auditRetryInterval and try again, honouring
		// ctx cancellation so we don't outlive the resolveAndUpdate budget.
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(auditRetryInterval):
		}
	}
}

// fetchAndMatchAuditEntry does a single GuildAuditLog read and returns the
// first entry whose TargetID matches and whose snowflake-timestamp is within
// 10s of eventTime. Returns (nil, nil) when no entry matches.
func (e *ExternalAudit) fetchAndMatchAuditEntry(
	ctx context.Context,
	guildID, targetID string,
	actionType discordgo.AuditLogAction,
	eventTime time.Time,
) (*discordgo.AuditLogEntry, error) {
	res, err := e.session.GuildAuditLog(
		guildID, "", "", int(actionType), 5,
	)
	if err != nil {
		return nil, fmt.Errorf("external_audit: audit log fetch: %w", err)
	}
	if res == nil {
		return nil, nil
	}

	for _, entry := range res.AuditLogEntries {
		if entry == nil {
			continue
		}
		if entry.TargetID != targetID {
			continue
		}
		ts, ok := SnowflakeTime(entry.ID)
		if !ok {
			continue
		}
		if ts.Sub(eventTime).Abs() > 10*time.Second {
			continue
		}
		return entry, nil
	}

	return nil, nil
}

func (e *ExternalAudit) resolveActor(
	guildID, targetID, executor, reason string,
	eventTime time.Time,
) ResolvedActor {
	if executor == "" {
		return ResolvedActor{
			ActorID: "unknown",
			Source:  audit.SourceUnknown,
		}
	}

	if !e.executorIsBot(executor) {
		return ResolvedActor{
			ActorID: executor,
			Source:  audit.SourceNative,
		}
	}

	result := ResolvedActor{
		ActorIsBot: true,
		Source:     audit.SourceExternal,
	}

	if e.buffer != nil {
		if msg := e.buffer.ScanForBotResponse(
			"", executor, 5*time.Second, eventTime,
		); msg != nil {
			if user := interactionUser(msg); user != "" && user != targetID {
				result.ActorID = user
				return result
			}
			if msg.ReferencedMessageID != "" {
				author, err := e.fetchReferencedAuthor(
					msg.ChannelID, msg.ReferencedMessageID,
				)
				if err == nil && author != "" && author != targetID {
					result.ActorID = author
					return result
				}
			}
		}
	}

	if reason != "" {
		if extracted, ok := parseActorFromReason(reason); ok {
			if extracted.ID != "" {
				result.ActorID = extracted.ID
				return result
			}
			if extracted.Name != "" {
				if id := e.resolveUsernameViaCache(guildID, extracted.Name); id != "" {
					result.ActorID = id
					return result
				}
				result.ActorName = extracted.Name
				return result
			}
		}
	}

	if e.buffer != nil {
		if msg := e.buffer.ScanForAction(
			"", targetID, banKindKeywords(),
			5*time.Second, eventTime,
		); msg != nil && msg.AuthorID != targetID {
			result.ActorID = msg.AuthorID
			return result
		}
	}

	return result
}

func interactionUser(m *bufferedMessage) string {
	if m == nil {
		return ""
	}
	if m.InteractionMetadata != nil && m.InteractionMetadata.User != nil {
		return m.InteractionMetadata.User.ID
	}
	if m.Interaction != nil && m.Interaction.User != nil {
		return m.Interaction.User.ID
	}
	return ""
}

func (e *ExternalAudit) fetchReferencedAuthor(
	channelID, messageID string,
) (string, error) {
	if e.session == nil || channelID == "" || messageID == "" {
		return "", errors.New("external_audit: missing inputs")
	}
	msg, err := e.session.ChannelMessage(channelID, messageID)
	if err != nil {
		return "", err
	}
	if msg == nil || msg.Author == nil {
		return "", errors.New("external_audit: message or author missing")
	}
	return msg.Author.ID, nil
}

func (e *ExternalAudit) executorIsBot(executorID string) bool {
	if e.session == nil || executorID == "" {
		return false
	}
	for _, g := range e.session.State.Guilds {
		if g == nil {
			continue
		}
		if m, err := e.session.State.Member(g.ID, executorID); err == nil && m != nil {
			if m.User != nil {
				return m.User.Bot
			}
		}
	}
	return false
}

func (e *ExternalAudit) resolveUsernameViaCache(guildID, name string) string {
	if e.session == nil || guildID == "" || name == "" {
		return ""
	}
	guild, err := e.session.State.Guild(guildID)
	if err != nil || guild == nil {
		return ""
	}
	var match string
	count := 0
	for _, m := range guild.Members {
		if m == nil || m.User == nil {
			continue
		}
		if strings.EqualFold(m.User.Username, name) ||
			strings.EqualFold(m.User.GlobalName, name) ||
			strings.EqualFold(m.Nick, name) {
			count++
			if count > 1 {
				return ""
			}
			match = m.User.ID
		}
	}
	if count != 1 {
		return ""
	}
	return match
}

func banKindKeywords() []string {
	return []string{
		"ban", "kick", "timeout", "mute", "role", "nick", "deafen",
		"pin", "unpin", "purge", "delete", "disconnect",
	}
}

func (e *ExternalAudit) enqueueAuditRow(
	ctx context.Context,
	action audit.ExternalAction,
) (int64, error) {
	if action.GuildID == "" || action.TargetID == "" || action.Intent == "" {
		return 0, errors.New("external_audit: guild/target/intent required")
	}

	dedupIntent := action.Intent
	if action.TargetType == "" {
		action.TargetType = "user"
	}
	if action.ExecutedAt.IsZero() {
		action.ExecutedAt = time.Now().UTC()
	}

	if action.Source == audit.SourceBot || action.Source == "" {
		if dedupIntent == "ban" || dedupIntent == "unban" ||
			dedupIntent == "kick" || dedupIntent == "timeout" ||
			dedupIntent == "untimeout" {
			recent, err := audit.RecentBotAction(
				ctx, e.db,
				action.GuildID, action.TargetID, dedupIntent,
				10*time.Second,
			)
			if err != nil {
				e.logger.Warn("external_audit: dedup query failed; proceeding",
					zap.String("intent", action.Intent),
					zap.String("guild_id", action.GuildID),
					zap.Error(err),
				)
			} else if recent {
				e.logger.Debug("external_audit: skipped (recent bot action)",
					zap.String("intent", action.Intent),
					zap.String("guild_id", action.GuildID),
					zap.String("target_id", action.TargetID),
				)
				return 0, nil
			}
		}
	}

	action.ActorID = "unknown"
	rowID, err := audit.InsertExternal(ctx, e.db, action)
	if err != nil {
		return 0, fmt.Errorf("external_audit: insert: %w", err)
	}

	e.logger.Info("external_audit: row inserted; scheduling resolution",
		zap.String("intent", action.Intent),
		zap.String("guild_id", action.GuildID),
		zap.String("target_id", action.TargetID),
		zap.Int64("row_id", rowID),
	)

	go e.resolveAndUpdate(rowID, action, time.Now().UTC())

	return rowID, nil
}

func (e *ExternalAudit) resolveAndUpdate(
	rowID int64,
	action audit.ExternalAction,
	eventTime time.Time,
) {
	// Polling budget: auditDelay (initial settle) + 10s of retries at
	// auditRetryInterval. The prior auditDelay+5s budget was too tight for
	// Discord's audit log replication lag on native UI actions.
	bgCtx, cancel := context.WithTimeout(
		context.Background(),
		e.auditDelay+10*time.Second,
	)
	defer cancel()

	auditType := actionToLogAction(action.Intent)
	if auditType == 0 {
		return
	}

	entry, err := e.fetchAuditLog(
		bgCtx, action.GuildID, action.TargetID, auditType, eventTime,
	)
	if err != nil {
		e.logger.Warn("external_audit: fetch failed; leaving row unknown",
			zap.String("intent", action.Intent),
			zap.String("guild_id", action.GuildID),
			zap.Error(err),
		)
		return
	}
	if entry == nil {
		// After exhausting the polling budget (~10s + auditDelay). The row
		// stays at its insert-time actor_id="unknown". Logged at Info
		// because silent unknown-actor rows are operationally interesting.
		e.logger.Info("external_audit: no audit entry after retries; leaving row unknown",
			zap.String("intent", action.Intent),
			zap.String("guild_id", action.GuildID),
			zap.String("target_id", action.TargetID),
			zap.Duration("budget", e.auditDelay+10*time.Second),
		)
		return
	}

	resolved := e.resolveActor(
		action.GuildID, action.TargetID, entry.UserID, entry.Reason, eventTime,
	)

	if err := audit.UpdateActor(bgCtx, e.db, audit.ActorUpdate{
		RowID:      rowID,
		ActorID:    resolved.ActorID,
		ActorIsBot: resolved.ActorIsBot,
		ActorName:  resolved.ActorName,
		Reason:     entry.Reason,
	}); err != nil {
		e.logger.Error("external_audit: update actor failed",
			zap.String("intent", action.Intent),
			zap.Int64("row_id", rowID),
			zap.Error(err),
		)
		return
	}
	e.logger.Info("external_audit: actor resolved",
		zap.String("intent", action.Intent),
		zap.String("guild_id", action.GuildID),
		zap.String("target_id", action.TargetID),
		zap.String("actor_id", resolved.ActorID),
		zap.String("actor_name", resolved.ActorName),
		zap.Bool("actor_is_bot", resolved.ActorIsBot),
		zap.String("source", resolved.Source),
	)
}

func actionToLogAction(intent string) discordgo.AuditLogAction {
	switch intent {
	case "ban":
		return discordgo.AuditLogActionMemberBanAdd
	case "unban":
		return discordgo.AuditLogActionMemberBanRemove
	case "kick":
		return discordgo.AuditLogActionMemberKick
	case "timeout", "untimeout",
		"add_role", "remove_role", "set_nickname", "reset_nickname":
		return discordgo.AuditLogActionMemberUpdate
	case "role_add", "role_remove":
		return discordgo.AuditLogActionMemberRoleUpdate
	case "mute", "unmute", "deafen", "undeafen",
		"voice_disconnect":
		return discordgo.AuditLogActionMemberUpdate
	case "delete_message":
		return discordgo.AuditLogActionMessageDelete
	case "purge_messages":
		return discordgo.AuditLogActionMessageBulkDelete
	case "channel_create":
		return discordgo.AuditLogActionChannelCreate
	case "channel_update":
		return discordgo.AuditLogActionChannelUpdate
	case "channel_delete":
		return discordgo.AuditLogActionChannelDelete
	case "role_create":
		return discordgo.AuditLogActionRoleCreate
	case "role_update":
		return discordgo.AuditLogActionRoleUpdate
	case "role_delete":
		return discordgo.AuditLogActionRoleDelete
	case "guild_update":
		return discordgo.AuditLogActionGuildUpdate
	default:
		return 0
	}
}

func (e *ExternalAudit) OnGuildBanAdd(
	_ *discordgo.Session,
	m *discordgo.GuildBanAdd,
) {
	if m == nil || m.GuildID == "" || m.User == nil {
		return
	}
	if m.User.ID == e.botUserID() {
		return
	}
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    m.GuildID,
			ChannelID:  "",
			TargetID:   m.User.ID,
			TargetType: "user",
			Intent:     "ban",
			ExecutedAt: time.Now().UTC(),
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: ban insert failed",
			zap.String("guild_id", m.GuildID),
			zap.String("target_id", m.User.ID),
			zap.Error(err),
		)
	}
}

func (e *ExternalAudit) OnGuildBanRemove(
	_ *discordgo.Session,
	m *discordgo.GuildBanRemove,
) {
	if m == nil || m.GuildID == "" || m.User == nil {
		return
	}
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    m.GuildID,
			ChannelID:  "",
			TargetID:   m.User.ID,
			TargetType: "user",
			Intent:     "unban",
			ExecutedAt: time.Now().UTC(),
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: unban insert failed",
			zap.String("guild_id", m.GuildID),
			zap.String("target_id", m.User.ID),
			zap.Error(err),
		)
	}
}

func (e *ExternalAudit) OnGuildMemberRemove(
	_ *discordgo.Session,
	m *discordgo.GuildMemberRemove,
) {
	if m == nil || m.GuildID == "" {
		return
	}
	userID := ""
	if m.Member != nil && m.Member.User != nil {
		userID = m.Member.User.ID
	}
	if userID == "" {
		return
	}

	eventTime := time.Now().UTC()
	bg, cancel := context.WithTimeout(
		context.Background(),
		e.auditDelay+2*time.Second,
	)
	defer cancel()

	entry, err := e.fetchAuditLog(
		bg,
		m.GuildID,
		userID,
		discordgo.AuditLogActionMemberKick,
		eventTime,
	)
	if err != nil {
		e.logger.Warn("external_audit: kick disambiguation fetch failed",
			zap.String("guild_id", m.GuildID),
			zap.String("user_id", userID),
			zap.Error(err),
		)
		return
	}
	if entry == nil {
		e.logger.Debug("external_audit: voluntary leave; no row inserted",
			zap.String("guild_id", m.GuildID),
			zap.String("user_id", userID),
		)
		return
	}

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    m.GuildID,
			ChannelID:  "",
			TargetID:   userID,
			TargetType: "user",
			Intent:     "kick",
			ExecutedAt: eventTime,
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: kick insert failed",
			zap.String("guild_id", m.GuildID),
			zap.String("target_id", userID),
			zap.Error(err),
		)
	}
}

func (e *ExternalAudit) botUserID() string {
	if e.session == nil || e.session.State == nil {
		return ""
	}
	u := e.session.State.User
	if u == nil {
		return ""
	}
	return u.ID
}

func (e *ExternalAudit) OnGuildMemberUpdateAudit(
	_ *discordgo.Session,
	m *discordgo.GuildMemberUpdate,
) {
	if m == nil || m.GuildID == "" || m.Member == nil || m.Member.User == nil {
		return
	}
	userID := m.Member.User.ID
	if userID == "" || userID == e.botUserID() {
		return
	}

	before := m.BeforeUpdate
	after := m.Member
	eventTime := time.Now().UTC()

	if diff := diffTimeout(before, after); diff != "" {
		params := map[string]any{"timeout_state": diff}
		e.queueDiff(m.GuildID, userID, diff, params, eventTime)
	}

	added, removed := diffRoles(before, after)
	if len(added) > 0 {
		params := map[string]any{"added_role_ids": added}
		e.queueDiff(m.GuildID, userID, "add_role", params, eventTime)
	}
	if len(removed) > 0 {
		params := map[string]any{"removed_role_ids": removed}
		e.queueDiff(m.GuildID, userID, "remove_role", params, eventTime)
	}

	if before != nil && before.Nick != after.Nick {
		params := map[string]any{
			"old_nick": before.Nick,
			"new_nick": after.Nick,
		}
		e.queueDiff(m.GuildID, userID, "set_nickname", params, eventTime)
	}
}

func (e *ExternalAudit) queueDiff(
	guildID, userID, intent string,
	params map[string]any,
	eventTime time.Time,
) {
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    guildID,
			ChannelID:  "",
			TargetID:   userID,
			TargetType: "user",
			Intent:     intent,
			Parameters: params,
			ExecutedAt: eventTime,
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: GuildMemberUpdate diff insert failed",
			zap.String("guild_id", guildID),
			zap.String("target_id", userID),
			zap.String("intent", intent),
			zap.Error(err),
		)
	}
}

func diffTimeout(before, after *discordgo.Member) string {
	beforeActive := before != nil &&
		before.CommunicationDisabledUntil != nil &&
		!before.CommunicationDisabledUntil.IsZero() &&
		before.CommunicationDisabledUntil.After(time.Now().UTC())

	afterActive := after != nil &&
		after.CommunicationDisabledUntil != nil &&
		!after.CommunicationDisabledUntil.IsZero() &&
		after.CommunicationDisabledUntil.After(time.Now().UTC())

	switch {
	case !beforeActive && afterActive:
		return "timeout"
	case beforeActive && !afterActive:
		return "untimeout"
	}
	return ""
}

func diffRoles(before, after *discordgo.Member) (added, removed []string) {
	beforeSet := make(map[string]struct{}, len(safeRoles(before)))
	for _, r := range safeRoles(before) {
		beforeSet[r] = struct{}{}
	}
	afterSet := make(map[string]struct{}, len(safeRoles(after)))
	for _, r := range safeRoles(after) {
		afterSet[r] = struct{}{}
	}
	for r := range afterSet {
		if _, ok := beforeSet[r]; !ok {
			added = append(added, r)
		}
	}
	for r := range beforeSet {
		if _, ok := afterSet[r]; !ok {
			removed = append(removed, r)
		}
	}
	return added, removed
}

func safeRoles(m *discordgo.Member) []string {
	if m == nil {
		return nil
	}
	return m.Roles
}

func (e *ExternalAudit) OnVoiceStateUpdate(
	_ *discordgo.Session,
	m *discordgo.VoiceStateUpdate,
) {
	if m == nil || m.GuildID == "" || m.UserID == "" || m.VoiceState == nil {
		return
	}
	if m.UserID == e.botUserID() {
		return
	}

	before, ok := e.voiceState(m.GuildID, m.UserID)
	if !ok || before == nil {
		return
	}

	after := m.VoiceState
	eventTime := time.Now().UTC()

	if before.Mute != after.Mute {
		intent := "unmute"
		if after.Mute {
			intent = "mute"
		}
		e.queueDiff(m.GuildID, m.UserID, intent, nil, eventTime)
	}
	if before.Deaf != after.Deaf {
		intent := "undeafen"
		if after.Deaf {
			intent = "deafen"
		}
		e.queueDiff(m.GuildID, m.UserID, intent, nil, eventTime)
	}
	if before.ChannelID != "" && after.ChannelID == "" {
		e.queueDiff(m.GuildID, m.UserID, "voice_disconnect", nil, eventTime)
	}
}

func (e *ExternalAudit) voiceState(guildID, userID string) (*discordgo.VoiceState, bool) {
	if e.session == nil {
		return nil, false
	}
	vs, err := e.session.State.VoiceState(guildID, userID)
	if err != nil || vs == nil {
		return nil, false
	}
	return vs, true
}

func (e *ExternalAudit) OnMessageDeleteAudit(s *discordgo.Session, m *discordgo.MessageDelete) {
	if m == nil || m.Message == nil || m.GuildID == "" || m.ChannelID == "" {
		return
	}
	msgID := m.ID
	if msgID == "" {
		return
	}

	// Author resolution: prefer the embedded Message (a snapshot from the
	// state cache taken before discordgo removed the entry), then fall back
	// to a live state lookup for the rare case where the snapshot lacks the
	// author.
	authorID := ""
	if m.Message.Author != nil {
		authorID = m.Message.Author.ID
	}
	if authorID == "" && s != nil {
		if msg, err := s.State.Message(m.ChannelID, msgID); err == nil && msg != nil && msg.Author != nil {
			authorID = msg.Author.ID
		}
	}

	// Skip the bot's own message deletions. The bot routinely deletes its
	// own placeholder/reply messages via deletePlaceholderAndReply; those
	// are UI affordances, not moderation actions. Recording them as
	// external audit rows produced fake "native delete_message" entries
	// that then failed to resolve (Discord doesn't write audit-log entries
	// for a bot deleting its own messages), leaving actor_id="unknown" and
	// spamming the "no audit entry after retries" log line.
	if authorID == e.botUserID() {
		return
	}

	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	params := map[string]any{
		"channel_id": m.ChannelID,
	}
	if authorID != "" {
		params["author_id"] = authorID
	}

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    m.GuildID,
			ChannelID:  m.ChannelID,
			TargetID:   msgID,
			TargetType: "message",
			Intent:     "delete_message",
			Parameters: params,
			ExecutedAt: time.Now().UTC(),
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: message-delete insert failed",
			zap.String("guild_id", m.GuildID),
			zap.String("message_id", msgID),
			zap.Error(err),
		)
	}
}

func (e *ExternalAudit) OnMessageDeleteBulkAudit(
	_ *discordgo.Session,
	m *discordgo.MessageDeleteBulk,
) {
	if m == nil || m.GuildID == "" || m.ChannelID == "" || len(m.Messages) == 0 {
		return
	}
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	params := map[string]any{
		"channel_id":    m.ChannelID,
		"message_count": len(m.Messages),
	}
	targetID := m.Messages[0]

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    m.GuildID,
			ChannelID:  m.ChannelID,
			TargetID:   targetID,
			TargetType: "message",
			Intent:     "purge_messages",
			Parameters: params,
			ExecutedAt: time.Now().UTC(),
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: bulk-delete insert failed",
			zap.String("guild_id", m.GuildID),
			zap.String("channel_id", m.ChannelID),
			zap.Int("count", len(m.Messages)),
			zap.Error(err),
		)
	}
}

func (e *ExternalAudit) OnGuildChannelCreate(_ *discordgo.Session, m *discordgo.ChannelCreate) {
	if !e.recordGuildMeta(m.GuildID, "", "channel_create", nil) {
		return
	}
}

func (e *ExternalAudit) OnGuildChannelUpdate(_ *discordgo.Session, m *discordgo.ChannelUpdate) {
	if !e.recordGuildMeta(m.GuildID, m.Channel.ID, "channel_update", nil) {
		return
	}
}

func (e *ExternalAudit) OnGuildChannelDelete(_ *discordgo.Session, m *discordgo.ChannelDelete) {
	if !e.recordGuildMeta(m.GuildID, m.Channel.ID, "channel_delete", nil) {
		return
	}
}

func (e *ExternalAudit) OnGuildRoleCreate(_ *discordgo.Session, m *discordgo.GuildRoleCreate) {
	if m == nil || m.Role == nil {
		return
	}
	if !e.recordGuildMeta(m.GuildID, m.Role.ID, "role_create", nil) {
		return
	}
}

func (e *ExternalAudit) OnGuildRoleUpdate(_ *discordgo.Session, m *discordgo.GuildRoleUpdate) {
	if m == nil || m.Role == nil {
		return
	}
	if !e.recordGuildMeta(m.GuildID, m.Role.ID, "role_update", nil) {
		return
	}
}

func (e *ExternalAudit) OnGuildRoleDelete(_ *discordgo.Session, m *discordgo.GuildRoleDelete) {
	if !e.recordGuildMeta(m.GuildID, m.RoleID, "role_delete", nil) {
		return
	}
}

func (e *ExternalAudit) OnGuildUpdate(_ *discordgo.Session, m *discordgo.GuildUpdate) {
	if m == nil || m.ID == "" {
		return
	}
	if !e.recordGuildMeta(m.ID, m.ID, "guild_update", nil) {
		return
	}
}

func (e *ExternalAudit) recordGuildMeta(
	guildID, targetID, intent string,
	params map[string]any,
) bool {
	if guildID == "" {
		return false
	}
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	action := audit.ExternalAction{
		ModAction: audit.ModAction{
			GuildID:    guildID,
			ChannelID:  "",
			TargetID:   targetID,
			TargetType: "guild_meta",
			Intent:     intent,
			Parameters: params,
			ExecutedAt: time.Now().UTC(),
		},
		Source: audit.SourceNative,
	}
	if _, err := e.enqueueAuditRow(bg, action); err != nil {
		e.logger.Error("external_audit: tier-3 insert failed",
			zap.String("guild_id", guildID),
			zap.String("intent", intent),
			zap.Error(err),
		)
		return false
	}
	return true
}

func (e *ExternalAudit) OnMessageCreateBuffered(
	_ *discordgo.Session,
	m *discordgo.MessageCreate,
) {
	if e.buffer == nil || m == nil {
		return
	}
	e.buffer.Push(m)
}
