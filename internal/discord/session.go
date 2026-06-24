package discord

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

type Session struct {
	*discordgo.Session
}

func NewSession(token string) (*Session, error) {
	if token == "" {
		return nil, fmt.Errorf("discord: token must not be empty")
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("discord: creating session: %w", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	return &Session{Session: dg}, nil
}

func (s *Session) DeleteMessage(channelID, messageID string) error {
	if err := s.Session.ChannelMessageDelete(channelID, messageID); err != nil {
		return fmt.Errorf("discord: deleting message %q in channel %q: %w",
			messageID, channelID, err)
	}
	return nil
}

func (s *Session) GuildBanCreate(
	guildID, userID, reason string,
	deleteMessageDays int,
) error {
	if err := s.Session.GuildBanCreateWithReason(
		guildID, userID, reason, deleteMessageDays,
	); err != nil {
		return fmt.Errorf("discord: banning user %q in guild %q: %w",
			userID, guildID, err)
	}
	return nil
}

func (s *Session) GuildBanDelete(guildID, userID string) error {
	if err := s.Session.GuildBanDelete(guildID, userID); err != nil {
		return fmt.Errorf("discord: unbanning user %q in guild %q: %w",
			userID, guildID, err)
	}
	return nil
}

func (s *Session) GuildMemberDelete(guildID, userID string) error {
	if err := s.Session.GuildMemberDelete(guildID, userID); err != nil {
		return fmt.Errorf("discord: kicking user %q from guild %q: %w",
			userID, guildID, err)
	}
	return nil
}

func (s *Session) GuildMemberEdit(
	guildID, userID string,
	data *discordgo.GuildMemberParams,
) error {
	if _, err := s.Session.GuildMemberEdit(guildID, userID, data); err != nil {
		return fmt.Errorf("discord: editing member %q in guild %q: %w",
			userID, guildID, err)
	}
	return nil
}

func (s *Session) GuildMemberRoleAdd(guildID, userID, roleID string) error {
	if err := s.Session.GuildMemberRoleAdd(guildID, userID, roleID); err != nil {
		return fmt.Errorf("discord: adding role %q to user %q in guild %q: %w",
			roleID, userID, guildID, err)
	}
	return nil
}

func (s *Session) GuildMemberRoleRemove(guildID, userID, roleID string) error {
	if err := s.Session.GuildMemberRoleRemove(guildID, userID, roleID); err != nil {
		return fmt.Errorf("discord: removing role %q from user %q in guild %q: %w",
			roleID, userID, guildID, err)
	}
	return nil
}

func (s *Session) ChannelMessagePin(channelID, messageID string) error {
	if err := s.Session.ChannelMessagePin(channelID, messageID); err != nil {
		return fmt.Errorf("discord: pinning message %q in channel %q: %w",
			messageID, channelID, err)
	}
	return nil
}

func (s *Session) ChannelMessageUnpin(channelID, messageID string) error {
	if err := s.Session.ChannelMessageUnpin(channelID, messageID); err != nil {
		return fmt.Errorf("discord: unpinning message %q in channel %q: %w",
			messageID, channelID, err)
	}
	return nil
}

func (s *Session) ChannelMessages(
	channelID string,
	limit int,
	beforeID, afterID, aroundID string,
) ([]*discordgo.Message, error) {
	msgs, err := s.Session.ChannelMessages(
		channelID, limit, beforeID, afterID, aroundID,
	)
	if err != nil {
		return nil, fmt.Errorf("discord: fetching messages from channel %q: %w",
			channelID, err)
	}
	return msgs, nil
}

func (s *Session) ChannelMessagesBulkDelete(
	channelID string,
	messageIDs []string,
) error {
	if err := s.Session.ChannelMessagesBulkDelete(channelID, messageIDs); err != nil {
		return fmt.Errorf("discord: bulk deleting %d messages in channel %q: %w",
			len(messageIDs), channelID, err)
	}
	return nil
}

func (s *Session) GuildCount() int {
	s.Session.State.RLock()
	defer s.Session.State.RUnlock()
	return len(s.Session.State.Guilds)
}

func (s *Session) TotalMemberCount() int {
	s.Session.State.RLock()
	defer s.Session.State.RUnlock()
	total := 0
	for _, g := range s.Session.State.Guilds {
		total += g.MemberCount
	}
	return total
}

func (s *Session) GuildMemberVoiceState(
	guildID, userID string,
	_ ...discordgo.RequestOption,
) (*discordgo.VoiceState, error) {
	vs, err := s.Session.State.VoiceState(guildID, userID)
	if err != nil {
		return nil, fmt.Errorf(
			"discord: fetching voice state for user %q in guild %q: %w",
			userID, guildID, err,
		)
	}
	return vs, nil
}
