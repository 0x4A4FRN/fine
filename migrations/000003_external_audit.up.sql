ALTER TABLE mod_actions
  ADD COLUMN IF NOT EXISTS source        TEXT    NOT NULL DEFAULT 'bot'
    CHECK (source IN ('bot', 'native', 'external', 'unknown')),
  ADD COLUMN IF NOT EXISTS actor_is_bot  BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS actor_name    TEXT    NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_mod_actions_source
  ON mod_actions(guild_id, source, executed_at DESC);

CREATE INDEX IF NOT EXISTS idx_mod_actions_recent_bot
  ON mod_actions(guild_id, target_id, intent, executed_at DESC);
