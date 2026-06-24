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

	if len(m.Attachments) > 0 {
		attachMeta := make([]storage.AttachmentMetadata, 0, len(m.Attachments))
		for _, att := range m.Attachments {
			meta := storage.AttachmentMetadata{
				Filename:    att.Filename,
				ContentType: att.ContentType,
				Size:        int64(att.Size),
			}

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

	if resp.ContentLength > 25*1024*1024 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("download: file too large (%d bytes)", resp.ContentLength)
	}

	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, resp.Body, 25*1024*1024); err != nil && err != io.EOF {
		return fmt.Errorf("download: %w", err)
	}

	return h.storageUploader.Upload(context.Background(), s3Key, &buf, contentType)
}

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
