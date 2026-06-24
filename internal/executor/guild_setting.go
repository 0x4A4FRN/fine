package executor

import "sync"

type GuildSettings struct {
	GuildID      string
	SudoMode     bool
	VerboseError bool
	UpdatedBy    string
}

type GuildSettingsSnapshot struct {
	mu   sync.RWMutex
	data map[string]GuildSettings
}

func NewGuildSettingsSnapshot() *GuildSettingsSnapshot {
	return &GuildSettingsSnapshot{data: make(map[string]GuildSettings)}
}

func (s *GuildSettingsSnapshot) Set(gs GuildSettings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[gs.GuildID] = gs
}

func (s *GuildSettingsSnapshot) Get(guildID string) GuildSettings {
	if s == nil || guildID == "" {
		return GuildSettings{GuildID: guildID}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[guildID]
}

func (s *GuildSettingsSnapshot) UpdateSetting(guildID, setting string, on bool, updatedBy string) GuildSettings {
	if s == nil {
		return GuildSettings{GuildID: guildID, UpdatedBy: updatedBy}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gs := s.data[guildID]
	gs.GuildID = guildID
	gs.UpdatedBy = updatedBy
	switch setting {
	case "sudo_mode":
		gs.SudoMode = on
	case "verbose_error":
		gs.VerboseError = on
	}
	s.data[guildID] = gs
	return gs
}
