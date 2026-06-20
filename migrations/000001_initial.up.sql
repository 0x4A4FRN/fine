-- Fine: Initial schema
-- Tables: conversations, conversation_messages, mod_actions, intent_cache, suggestion_windows

-- ── Conversations ──────────────────────────────────────────────────────────

CREATE TABLE conversations (
    id              BIGSERIAL PRIMARY KEY,
    guild_id        TEXT NOT NULL,
    channel_id      TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_conv_lookup
    ON conversations(guild_id, channel_id, user_id, last_active_at DESC);

-- ── Conversation messages ───────────────────────────────────────────────────

CREATE TABLE conversation_messages (
    id              BIGSERIAL PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content         TEXT NOT NULL,
    discord_msg_id  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_conv_msgs ON conversation_messages(conversation_id, id);

-- ── Mod actions (audit log) ─────────────────────────────────────────────────

CREATE TABLE mod_actions (
    id                  BIGSERIAL PRIMARY KEY,
    guild_id            TEXT NOT NULL,
    channel_id          TEXT NOT NULL,
    actor_id            TEXT NOT NULL,
    target_id           TEXT NOT NULL,
    target_type         TEXT NOT NULL,
    intent              TEXT NOT NULL,
    reason              TEXT,
    parameters          TEXT,                      -- JSON: {duration, messageCount, ...}
    source_message_id   TEXT,
    confirmed_at        TIMESTAMPTZ,               -- null for non-destructive
    executed_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_mod_actions_target ON mod_actions(guild_id, target_id, executed_at DESC);
CREATE INDEX idx_mod_actions_actor  ON mod_actions(guild_id, actor_id,  executed_at DESC);
CREATE INDEX idx_mod_actions_intent ON mod_actions(guild_id, intent,    executed_at DESC);

-- ── Intent cache ────────────────────────────────────────────────────────────

CREATE TABLE intent_cache (
    guild_id     TEXT NOT NULL,
    template     TEXT NOT NULL,
    intent       TEXT NOT NULL,
    confidence   REAL NOT NULL,
    parameters   TEXT,
    action_type  TEXT,
    hits         INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_hit_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (guild_id, template)
);

-- ── Suggestion windows (confirm flow) ───────────────────────────────────────

CREATE TABLE suggestion_windows (
    id              BIGSERIAL PRIMARY KEY,
    guild_id        TEXT NOT NULL,
    channel_id      TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('open', 'expired', 'cancelled', 'executed')),
    bot_message_id  TEXT NOT NULL,
    payload         TEXT NOT NULL,                 -- Full JSON LLM response
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_suggest_lookup  ON suggestion_windows(channel_id, user_id, status);
CREATE INDEX idx_suggest_bot_msg ON suggestion_windows(bot_message_id);

-- ── Guild settings ──────────────────────────────────────────────────────────
-- One row per guild. Hydrated into a process-local snapshot on startup; toggles
-- write through to the DB and refresh the same snapshot so subsequent requests
-- see the change without waiting for a restart.

CREATE TABLE guild_settings (
    guild_id      TEXT PRIMARY KEY,
    sudo_mode     BOOLEAN NOT NULL DEFAULT FALSE,
    verbose_error BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
