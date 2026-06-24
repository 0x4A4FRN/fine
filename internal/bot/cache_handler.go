package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/0x4A4FRN/fine/internal/cache"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/safety"
	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

func (h *Handler) handleCacheCheck(
	ctx context.Context,
	m *discordgo.MessageCreate,
	cleaned string,
) bool {
	userIDs, roleIDs := extractMentionIDs(m.Mentions)
	template := cache.BuildTemplate(cleaned, userIDs, roleIDs)

	entry, err := h.cacheStore.Get(ctx, m.GuildID, template)
	if err != nil {
		h.logger.Error("handler: cache lookup", zap.Error(err))
		return false
	}

	if entry == nil || entry.Confidence < h.cacheHitThreshold {
		h.logger.Debug("handler: cache miss or low confidence",
			zap.Bool("present", entry != nil),
			func() zap.Field {
				if entry != nil {
					return zap.Float64("confidence", entry.Confidence)
				}
				return zap.Skip()
			}(),
		)
		return false
	}

	h.logger.Info("handler: cache hit",
		zap.String("intent", entry.Intent),
		zap.Float64("confidence", entry.Confidence),
	)

	if safety.IsNegation(cleaned) {
		h.logger.Info("handler: negation gate override on cache hit",
			zap.String("intent", entry.Intent),
		)
		h.sendReply(m.ChannelID, h.negationReplyText(), m.ID)
		return true
	}

	resp, err := buildResponseFromCache(entry, m.Mentions)
	if err != nil {
		h.logger.Error("handler: building cached response", zap.Error(err))
		return false
	}

	if resp.Intent == "" {
		h.logger.Warn("handler: cache entry with empty intent; treating as miss",
			zap.String("template", "censored"),
			zap.Float64("confidence", entry.Confidence),
		)
		return false
	}

	if err := llm.ValidateLLMResponse(resp, h.logger); err != nil {
		h.logger.Warn("handler: cache hit response failed validation; falling through to LLM",
			zap.Error(err),
		)
		return false
	}

	if h.store != nil {
		if err := h.store.WriteMessage(
			ctx,
			m.GuildID,
			m.ChannelID,
			m.Author.ID,
			"user",
			cleaned,
			m.ID,
		); err != nil {
			h.logger.Error("handler: writing user message (cache)", zap.Error(err))
		}
	}

	h.dispatchModerationResponse(ctx, m, cleaned, resp, nil)
	return true
}
func (h *Handler) maybeWriteCache(
	ctx context.Context,
	guildID, cleaned string,
	resp *llm.LLMResponse,
) {
	if h.cacheStore == nil {
		return
	}

	if !resp.IsModeration {
		return
	}
	if resp.Confidence < h.cacheHitThreshold {
		return
	}
	if resp.Intent == "audit_lookup" {
		return
	}
	if resp.Intent == "" {
		return
	}
	if len(resp.Actions) > 0 {
		return
	}

	userIDs, roleIDs := extractMentionIDsFromResponse(resp)
	template := cache.BuildTemplate(cleaned, userIDs, roleIDs)

	paramsJSON, err := cache.SerializeParameters(resp.Parameters)
	if err != nil {
		h.logger.Error("handler: serializing parameters for cache", zap.Error(err))
		return
	}

	entry := cache.CacheEntry{
		Intent:     resp.Intent,
		Confidence: resp.Confidence,
		Parameters: paramsJSON,
		ActionType: "single",
	}

	if err := h.cacheStore.Set(ctx, guildID, template, entry); err != nil {
		h.logger.Error("handler: writing cache", zap.Error(err))
	}
	h.logger.Debug("handler: cache entry written",
		zap.String("intent", resp.Intent),
	)
}
func buildResponseFromCache(
	entry *cache.CacheEntry,
	mentions []*discordgo.User,
) (*llm.LLMResponse, error) {
	var params llm.Parameters
	if entry.Parameters != "" {
		if err := json.Unmarshal([]byte(entry.Parameters), &params); err != nil {
			return nil, fmt.Errorf("unmarshaling cached parameters: %w", err)
		}
	}

	targets := make([]llm.Target, 0, len(mentions))
	for _, u := range mentions {
		targets = append(targets, llm.Target{
			ID:   u.ID,
			Type: "user",
		})
	}

	return &llm.LLMResponse{
		Intent:       entry.Intent,
		Confidence:   entry.Confidence,
		IsModeration: true,
		Targets:      targets,
		Parameters:   params,
	}, nil
}
func extractMentionIDs(
	mentions []*discordgo.User,
) (userIDs []string, roleIDs []string) {
	userIDs = make([]string, 0, len(mentions))
	for _, u := range mentions {
		userIDs = append(userIDs, u.ID)
	}
	roleIDs = []string{}
	return userIDs, roleIDs
}
func extractMentionIDsFromResponse(resp *llm.LLMResponse) (userIDs []string, roleIDs []string) {
	userIDs = []string{}
	roleIDs = []string{}
	for _, t := range resp.Targets {
		switch t.Type {
		case "user":
			userIDs = append(userIDs, t.ID)
		case "role":
			roleIDs = append(roleIDs, t.ID)
		}
	}
	return userIDs, roleIDs
}
