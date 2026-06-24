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

			ph.mu.Lock()
			ph.mu.Unlock() //nolint:staticcheck
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

			if ctx.Err() != nil {
				ph.mu.Unlock()
				return
			}
			variant := h.replies.Get("handler", "on_receive", nil)
			if variant == lastVariant {

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

func (h *Handler) deletePlaceholderAndReply(
	ph *placeholder,
	channelID, sourceMsgID, text string,
) string {
	if h.messageAPI == nil {
		return ""
	}

	if ph != nil && ph.msgID != "" {
		ph.mu.Lock()
		_ = h.messageAPI.ChannelMessageDelete(channelID, ph.msgID)
		ph.mu.Unlock()
		h.logger.Info("handler: placeholder deleted",
			zap.String("channel_id", channelID),
			zap.String("message_id", ph.msgID),
		)
	}

	newID, _ := h.sendReply(channelID, text, sourceMsgID)
	h.logger.Info("handler: fresh reply sent after placeholder deletion",
		zap.String("channel_id", channelID),
		zap.String("message_id", newID),
		zap.String("reply_to", sourceMsgID),
		zap.String("final_preview", truncateContent(text, 200)),
	)
	return newID
}

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
