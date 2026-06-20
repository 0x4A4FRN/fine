package bot

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const placeholderRotationInterval = 2 * time.Second

type placeholder struct {
	mu        sync.Mutex
	msgID     string
	channelID string
	sourceID  string
	stopOnce  sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

func noopStop() {}
func (h *Handler) startPlaceholder(
	channelID string,
	sourceID string,
	parentCtx context.Context,
) (ph *placeholder, stop func()) {
	if h.messageAPI == nil || h.replies == nil {
		return nil, noopStop
	}

	initial := h.replies.Get("handler", "on_receive", nil)
	id, ok := h.sendReply(channelID, initial, sourceID)
	_ = ok
	if id == "" {
		h.logger.Warn("handler: placeholder initial send returned empty id; proceeding without placeholder",
			zap.String("variant", initial),
		)
		return nil, noopStop
	}
	h.logger.Info("handler: placeholder posted",
		zap.String("channel_id", channelID),
		zap.String("message_id", id),
		zap.String("variant", initial),
	)

	ph = &placeholder{
		msgID:     id,
		channelID: channelID,
		sourceID:  sourceID,
		done:      make(chan struct{}),
	}
	phCtx, phCancel := context.WithCancel(parentCtx)
	ph.cancel = phCancel

	go h.runPlaceholderRotation(phCtx, ph)

	stopFn := func() {
		ph.stopOnce.Do(func() {
			phCancel()
			// Acquire and immediately release the mutex so any tick
			// goroutine that already entered the critical section
			// finishes its (now no-op) edit. Combined with the
			// goroutine's ctx.Err() check under the same lock, this
			// guarantees that after stopFn returns, no further edit
			// can fire even if a tick is queued in ticker.C at this
			// exact moment.
			//
			// The empty Lock/Unlock is intentional: it is a
			// synchronization barrier, not a protected section.
			ph.mu.Lock()
			ph.mu.Unlock() //nolint:staticcheck // intentional empty critical section
			<-ph.done
		})
	}
	return ph, stopFn
}
func (h *Handler) runPlaceholderRotation(ctx context.Context, ph *placeholder) {
	defer close(ph.done)
	ticker := time.NewTicker(placeholderRotationInterval)
	defer ticker.Stop()
	lastVariant := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ph.mu.Lock()
			// Re-check cancellation under the lock. If the handler has
			// already called stop() after we entered this iteration, do
			// NOT overwrite the message the handler just finalized.
			if ctx.Err() != nil {
				ph.mu.Unlock()
				return
			}
			variant := h.replies.Get("handler", "on_receive", nil)
			if variant == lastVariant {
				// Avoid re-editing to the same variant; trivial visual
				// protection. Replies.Get is already random across
				// multiple variants so collisions are rare.
				ph.mu.Unlock()
				continue
			}
			_, err := h.messageAPI.ChannelMessageEdit(ph.channelID, ph.msgID, variant)
			ph.mu.Unlock()
			if err != nil {
				h.logger.Warn("handler: placeholder rotated edit failed; exiting rotation",
					zap.String("channel_id", ph.channelID),
					zap.String("message_id", ph.msgID),
					zap.Error(err),
				)
				return
			}
			lastVariant = variant
			h.logger.Debug("handler: placeholder rotated",
				zap.String("channel_id", ph.channelID),
				zap.String("message_id", ph.msgID),
				zap.String("variant", variant),
			)
		}
	}
}

// deletePlaceholderAndReply finalises a placeholder-driven flow by deleting
// the rotating "Thinking…" placeholder and sending a fresh reply (with a
// MessageReference to the invoking user message). Returns the new bot
// message id (empty on failure or when no message API is configured).
//
// This replaces the previous "edit placeholder in place" pattern. The
// delete-and-reply pattern avoids the orphan-placeholder bug where the
// user sees both the rotating "Thinking…" text and the final result,
// and gives a cleaner visual: the bot's reply is a real reply to the
// invoking message, not an edit of an unrelated placeholder.
func (h *Handler) deletePlaceholderAndReply(
	ph *placeholder,
	channelID, sourceMsgID, text string,
) string {
	if h.messageAPI == nil {
		return ""
	}
	// Delete the placeholder if it exists. The mutex serialises against
	// the rotation goroutine so we don't delete mid-edit.
	if ph != nil && ph.msgID != "" {
		ph.mu.Lock()
		_ = h.messageAPI.ChannelMessageDelete(channelID, ph.msgID)
		ph.mu.Unlock()
		h.logger.Info("handler: placeholder deleted",
			zap.String("channel_id", channelID),
			zap.String("message_id", ph.msgID),
		)
	}
	// Send a fresh reply to the invoking message.
	newID, _ := h.sendReply(channelID, text, sourceMsgID)
	h.logger.Info("handler: fresh reply sent after placeholder deletion",
		zap.String("channel_id", channelID),
		zap.String("message_id", newID),
		zap.String("reply_to", sourceMsgID),
		zap.String("final_preview", truncateContent(text, 200)),
	)
	return newID
}

// deletePlaceholderOnly deletes the rotating placeholder without sending a
// replacement reply. Used by flows where the executor sends its own message
// directly (e.g. snipe) and returns nil — the handler just needs to clean
// up the placeholder so the user doesn't see both "Thinking…" and the
// executor's message.
func (h *Handler) deletePlaceholderOnly(ph *placeholder) {
	if ph == nil || ph.msgID == "" || h.messageAPI == nil {
		return
	}
	ph.mu.Lock()
	_ = h.messageAPI.ChannelMessageDelete(ph.channelID, ph.msgID)
	ph.mu.Unlock()
	h.logger.Info("handler: placeholder deleted (no replacement reply)",
		zap.String("channel_id", ph.channelID),
		zap.String("message_id", ph.msgID),
	)
}
