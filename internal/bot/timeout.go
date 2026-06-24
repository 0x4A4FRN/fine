package bot

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/executor"
	"github.com/0x4A4FRN/fine/internal/llm"
)

type timeoutTrackEntry struct {
	GuildID       string
	UserID        string
	ChannelID     string
	BotMessageID  string
	ExpiresAtUnix int64
	LiftedAt      time.Time
	Settled       bool
}
type timeoutTracker struct {
	mu sync.RWMutex
	m  map[string]timeoutTrackEntry
}

func newTimeoutTracker() *timeoutTracker {
	return &timeoutTracker{m: map[string]timeoutTrackEntry{}}
}
func (t *timeoutTracker) Track(
	guildID, userID, channelID, botMessageID string,
	expiresAtUnix int64,
) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[guildID+":"+userID] = timeoutTrackEntry{
		GuildID:       guildID,
		UserID:        userID,
		ChannelID:     channelID,
		BotMessageID:  botMessageID,
		ExpiresAtUnix: expiresAtUnix,
	}
}
func (t *timeoutTracker) MarkLifted(guildID, userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := guildID + ":" + userID
	e, ok := t.m[k]
	if !ok {
		return
	}
	e.LiftedAt = time.Now().UTC()
	t.m[k] = e
}
func (t *timeoutTracker) Forget(guildID, userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, guildID+":"+userID)
}
func (t *timeoutTracker) Lookup(guildID, userID string) (timeoutTrackEntry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.m[guildID+":"+userID]
	return e, ok
}
func (t *timeoutTracker) TrySettle(guildID, userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := guildID + ":" + userID
	e, ok := t.m[k]
	if !ok {
		return false
	}
	if e.Settled {
		return false
	}
	e.Settled = true
	t.m[k] = e
	return true
}
func (t *timeoutTracker) Snapshot() []timeoutTrackEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]timeoutTrackEntry, 0, len(t.m))
	for _, e := range t.m {
		out = append(out, e)
	}
	return out
}
func (h *Handler) recordTimeoutTransition(
	resp *llm.LLMResponse,
	meta executor.ActionMeta,
	botMessageID string,
) {
	if h.timeoutTracker == nil || resp == nil || resp.Targets == nil {
		return
	}
	if resp.Intent != "timeout" && resp.Intent != "untimeout" {
		return
	}
	userID := ""
	for _, t := range resp.Targets {
		if t.Type == "user" {
			userID = t.ID
			break
		}
	}
	if userID == "" || meta.GuildID == "" {
		return
	}
	switch resp.Intent {
	case "timeout":
		h.timeoutTracker.Forget(meta.GuildID, userID)
		if resp.Parameters.DurationSeconds != nil {
			until := time.Now().UTC().Add(
				time.Duration(*resp.Parameters.DurationSeconds) * time.Second,
			)
			h.timeoutTracker.Track(
				meta.GuildID, userID, meta.ChannelID,
				botMessageID, until.Unix(),
			)
		}
	case "untimeout":
		// MarkLifted is left as defensive state; the actual edit-and-forget
		// happens synchronously in handleTimeoutLift (called from
		// executeResponseWithMeta). LiftedAt is harmless when set this
		// way but lets future code paths inspect lift vs natural expiry.
		h.timeoutTracker.MarkLifted(meta.GuildID, userID)
	}
}
func (h *Handler) handleTimeoutLift(resp *llm.LLMResponse, meta executor.ActionMeta) {
	if h.timeoutTracker == nil || h.messageAPI == nil || h.replies == nil {
		return
	}
	userID := ""
	for _, t := range resp.Targets {
		if t.Type == "user" {
			userID = t.ID
			break
		}
	}
	if userID == "" || meta.GuildID == "" {
		return
	}
	entry, ok := h.timeoutTracker.Lookup(meta.GuildID, userID)
	if !ok {
		h.logger.Warn("handler: untimeout lift; tracker entry absent (already swept?)",
			zap.String("guild_id", meta.GuildID),
			zap.String("user_id", userID),
		)
		return
	}
	text := h.replies.Get("timeout_reply", "lifted", map[string]string{
		"user_name": "<@" + userID + ">",
	})
	if msgID := h.editOrSend(entry.ChannelID, entry.BotMessageID, text); msgID != "" {
		h.logger.Info("handler: untimeout: message settled",
			zap.String("channel_id", entry.ChannelID),
			zap.String("message_id", msgID),
			zap.String("user_id", userID),
		)
	}
	h.timeoutTracker.Forget(meta.GuildID, userID)
}
func (h *Handler) StartTimeoutExpirySweep(ctx context.Context, interval time.Duration) {
	if h.timeoutTracker == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.sweepExpiredTimeouts()
		}
	}
}
func (h *Handler) sweepExpiredTimeouts() {
	if h.timeoutTracker == nil || h.messageAPI == nil || h.replies == nil || h.auditDB == nil {
		return
	}
	now := time.Now().UTC().Unix()
	for _, e := range h.timeoutTracker.Snapshot() {
		if e.ExpiresAtUnix <= 0 || now < e.ExpiresAtUnix {
			continue
		}
		if e.GuildID == "" || e.UserID == "" {
			continue
		}
		h.settleExpiredEntry(e.GuildID, e.UserID, "natural expiry")
	}
}
func (h *Handler) settleExpiredEntry(guildID, userID, reason string) {
	if h.timeoutTracker == nil || h.messageAPI == nil || h.replies == nil {
		return
	}
	if !h.timeoutTracker.TrySettle(guildID, userID) {
		h.logger.Debug("handler: timeout expiry; already settled elsewhere; skipping duplicate",
			zap.String("guild_id", guildID),
			zap.String("user_id", userID),
		)
		return
	}
	entry, ok := h.timeoutTracker.Lookup(guildID, userID)
	if !ok {
		return
	}

	// Pick template based on whether the original lift barely happened.
	// In normal operation the synchronous untimeout path already settled
	// and Forgot the entry; this branch is reached only if sync-lift was
	// bypassed (e.g. messageAPI was nil).
	useLiftedTemplate := !entry.LiftedAt.IsZero() &&
		time.Since(entry.LiftedAt) < 5*time.Second
	var templateKey string
	var vars map[string]string
	if useLiftedTemplate {
		templateKey = "lifted"
		vars = map[string]string{"user_name": "<@" + userID + ">"}
	} else {
		templateKey = "ended"
		vars = map[string]string{
			"user_name":     "<@" + userID + ">",
			"end_timestamp": strconv.FormatInt(time.Now().UTC().Unix(), 10),
		}
	}
	editText := h.replies.Get("timeout_reply", templateKey, vars)

	if msgID := h.editOrSend(entry.ChannelID, entry.BotMessageID, editText); msgID != "" {
		h.logger.Info("handler: timeout expiry-sweep: settled",
			zap.String("channel_id", entry.ChannelID),
			zap.String("message_id", msgID),
			zap.Bool("lifted_template", useLiftedTemplate),
		)
	}

	if h.auditDB != nil && !useLiftedTemplate {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := audit.WriteAction(ctx, h.auditDB, audit.ModAction{
			GuildID:    guildID,
			ChannelID:  entry.ChannelID,
			ActorID:    "",
			TargetID:   userID,
			TargetType: "user",
			Intent:     "timeout_ended",
			Reason:     reason,
			ExecutedAt: time.Now().UTC(),
		}); err != nil {
			h.logger.Warn("handler: timeout-end audit write failed",
				zap.String("user_id", userID),
				zap.Error(err),
			)
		}
	}

	h.timeoutTracker.Forget(guildID, userID)
}
func (h *Handler) OnGuildMemberUpdate(_ *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	if h.timeoutTracker == nil || h.messageAPI == nil || h.replies == nil {
		return
	}
	if m.Member == nil || m.Member.User == nil {
		return
	}
	userID := m.Member.User.ID
	if userID == "" || m.GuildID == "" {
		return
	}

	_, ok := h.timeoutTracker.Lookup(m.GuildID, userID)
	if !ok {
		return
	}

	// Discord's GUILD_MEMBER_UPDATE event payload is unreliable in this
	// environment: m.Member.CommunicationDisabledUntil can arrive stale
	// (still showing the timeout as future-dated) even when the timeout
	// has actually been cleared via the Discord UI. Trusting the event
	// payload leads to a false-positive "still timed out" and we miss
	// the M1 edit.
	//
	// Verify the actual current state with a REST call. The polling
	// sweeper is the source of truth for natural expiry, and this
	// listener now covers UI lifts (and any other way Discord clears
	// CommunicationDisabledUntil) reliably.
	if h.discord == nil {
		h.logger.Warn("handler: GUILD_MEMBER_UPDATE for tracked user; no discord session; cannot verify",
			zap.String("guild_id", m.GuildID),
			zap.String("user_id", userID),
		)
		return
	}
	currentMember, err := h.discord.GuildMember(m.GuildID, userID)
	if err != nil {
		h.logger.Warn("handler: GUILD_MEMBER_UPDATE; REST GuildMember lookup failed; deferring to polling sweep",
			zap.String("guild_id", m.GuildID),
			zap.String("user_id", userID),
			zap.Error(err),
		)
		return
	}
	stillTimedOut := currentMember.CommunicationDisabledUntil != nil &&
		!currentMember.CommunicationDisabledUntil.IsZero() &&
		currentMember.CommunicationDisabledUntil.After(time.Now().UTC())
	if stillTimedOut {
		h.logger.Debug("handler: GUILD_MEMBER_UPDATE; REST confirms timeout still active; deferring to polling sweep",
			zap.String("guild_id", m.GuildID),
			zap.String("user_id", userID),
		)
		return
	}

	// Delegate to the shared settle path. TrySettle inside
	// settleExpiredEntry atomically de-duplicates with the polling sweeper,
	// so the listener is now best-effort acceleration; the sweeper is the
	// authoritative source of truth for natural expiry.
	h.settleExpiredEntry(m.GuildID, userID, "member update: timeout cleared")
}
