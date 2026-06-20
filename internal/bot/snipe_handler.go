package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/storage"
)

// HandleMessageCreateSnapshot is a lightweight handler registered
// separately from HandleMessageCreate. It stores a snapshot of every
// non-Fine-bot message (including other bots) for the snipe feature.
// It runs independently and does not interfere with the main handler.
func (h *Handler) HandleMessageCreateSnapshot(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == h.BotID() {
		return
	}
	if h.storageStore == nil {
		return
	}

	snap := storage.Snapshot{
		GuildID:    m.GuildID,
		ChannelID:  m.ChannelID,
		MessageID:  m.ID,
		AuthorID:   m.Author.ID,
		AuthorName: m.Author.Username,
		AuthorBot:  m.Author.Bot,
		Content:    m.Content,
		MessageTS:  m.Timestamp,
	}

	// Build attachment metadata and optionally download to S3.
	if len(m.Attachments) > 0 {
		attachMeta := make([]storage.AttachmentMetadata, 0, len(m.Attachments))
		for _, att := range m.Attachments {
			meta := storage.AttachmentMetadata{
				Filename:    att.Filename,
				ContentType: att.ContentType,
				Size:        int64(att.Size),
			}

			// Download and upload to S3 if configured.
			if h.storageUploader != nil && att.URL != "" {
				s3Key := fmt.Sprintf("snipe:%s/%s", m.ID, att.Filename)
				err := h.downloadAndUpload(att.URL, s3Key, att.ContentType)
				if err != nil {
					h.logger.Warn("handler: snapshot: attachment upload failed",
						zap.String("message_id", m.ID),
						zap.String("filename", att.Filename),
						zap.String("s3_key", s3Key),
						zap.Error(err),
					)
					// Keep metadata without S3 key — the snipe display
					// will show "unavailable" for this attachment.
				} else {
					meta.S3Key = s3Key
				}
			}

			attachMeta = append(attachMeta, meta)
		}
		snap.Attachments = attachMeta
	}

	if err := h.storageStore.InsertSnapshot(context.Background(), snap); err != nil {
		h.logger.Error("handler: snapshot: insert failed",
			zap.String("message_id", m.ID),
			zap.Error(err),
		)
		return
	}

	h.logger.Debug("handler: snapshot: stored",
		zap.String("message_id", m.ID),
		zap.String("channel_id", m.ChannelID),
		zap.Int("attachments", len(snap.Attachments)),
	)
}

// HandleMessageDelete marks a message as deleted in the snapshot store
// when Discord delivers a MESSAGE_DELETE event.
func (h *Handler) HandleMessageDelete(_ *discordgo.Session, m *discordgo.MessageDelete) {
	if h.storageStore == nil {
		return
	}

	if err := h.storageStore.MarkDeleted(context.Background(), m.ID); err != nil {
		h.logger.Error("handler: message delete: mark failed",
			zap.String("message_id", m.ID),
			zap.Error(err),
		)
		return
	}

	h.logger.Debug("handler: message delete: marked",
		zap.String("message_id", m.ID),
	)
}

// HandleMessageDeleteBulk marks multiple messages as deleted when
// Discord delivers a MESSAGE_DELETE_BULK event.
func (h *Handler) HandleMessageDeleteBulk(_ *discordgo.Session, m *discordgo.MessageDeleteBulk) {
	if h.storageStore == nil {
		return
	}

	if len(m.Messages) == 0 {
		return
	}

	if err := h.storageStore.MarkBulkDeleted(context.Background(), m.Messages); err != nil {
		h.logger.Error("handler: bulk delete: mark failed",
			zap.Int("count", len(m.Messages)),
			zap.Error(err),
		)
		return
	}

	h.logger.Debug("handler: bulk delete: marked",
		zap.Int("count", len(m.Messages)),
	)
}

// downloadAndUpload downloads a file from a URL and uploads it to S3
// via the configured uploader.
func (h *Handler) downloadAndUpload(url, s3Key, contentType string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// Cap at 25MB.
	if resp.ContentLength > 25*1024*1024 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("download: file too large (%d bytes)", resp.ContentLength)
	}

	// Buffer the entire body so the S3 client sends a known Content-Length.
	// Streaming (chunked) uploads are rejected by some S3-compatible
	// providers (Backblaze B2, etc.).
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, resp.Body, 25*1024*1024); err != nil && err != io.EOF {
		return fmt.Errorf("download: %w", err)
	}

	return h.storageUploader.Upload(context.Background(), s3Key, &buf, contentType)
}

// parseSnipeCustomID extracts the prefix and bot message ID from a snipe
// button custom ID string. The custom ID format is "snipe_<prev|next|delete>_<botMessageID>"
// where botMessageID is the Discord snowflake of the bot's snipe message.
// Returns ("", "", false) if the custom ID is not a snipe button.
func parseSnipeCustomID(customID string) (prefix, botMsgID string, ok bool) {
	parts := strings.SplitN(customID, "_", 3)
	if len(parts) != 3 || parts[0] != "snipe" {
		return "", "", false
	}
	if parts[2] == "" {
		return "", "", false
	}
	return "snipe_" + parts[1], parts[2], true
}

// parseConfirmCustomID extracts the prefix and window ID from a confirm
// button custom ID string. Returns ("", 0, false) if the custom ID is
// not a confirmation button.
func parseConfirmCustomID(customID string) (prefix string, windowID int64, ok bool) {
	parts := strings.SplitN(customID, "_", 3)
	if len(parts) != 3 || parts[0] != "confirm" {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[0] + "_" + parts[1], id, true
}
