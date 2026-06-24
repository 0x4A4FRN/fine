DROP INDEX IF EXISTS idx_mod_actions_recent_bot;
DROP INDEX IF EXISTS idx_mod_actions_source;

ALTER TABLE mod_actions
  DROP COLUMN IF EXISTS actor_name,
  DROP COLUMN IF EXISTS actor_is_bot,
  DROP COLUMN IF EXISTS source;
