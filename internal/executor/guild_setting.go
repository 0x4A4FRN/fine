package executor

import "sync"

// GuildSettings is the in-memory view of one row in the guild_settings table.
// Mirrors db.GuildSettings so the storage layer and process-local cache stay
// decoupled; main.go copies fields at hydration and on each toggle.
type GuildSettings struct {
	GuildID      string
	SudoMode     bool
	VerboseError bool
	UpdatedBy    string
}

// GuildSettingsSnapshot is the process-local cache of guild settings, hydrated
// once at startup from the DB and updated in place whenever a SettingExecutor
// flips a flag. Subsequent handler requests consult this snapshot instead of
// hitting the DB on every message.
type GuildSettingsSnapshot struct {
	mu   sync.RWMutex
	data map[string]GuildSettings
}

func NewGuildSettingsSnapshot() *GuildSettingsSnapshot {
	return &GuildSettingsSnapshot{data: make(map[string]GuildSettings)}
}

// Set replaces the entry for guildID.
func (s *GuildSettingsSnapshot) Set(gs GuildSettings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[gs.GuildID] = gs
}

// Get returns the entry for guildID, or zero-value (all flags false) when the
// guild has no row yet. Default-zero is intentional: an unconfigured guild
// behaves the same way it always did — no sudo, no verbose.
func (s *GuildSettingsSnapshot) Get(guildID string) GuildSettings {
	if s == nil || guildID == "" {
		return GuildSettings{GuildID: guildID}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[guildID]
}

// UpdateSetting atomically reads, modifies, and writes back a single setting
// for the given guild under the snapshot's mutex, eliminating the
// read-modify-write race that existed when setting.go assembled a new
// GuildSettings struct from multiple unlocked snapshot reads.
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
