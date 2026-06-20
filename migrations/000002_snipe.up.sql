-- Fine: Snipe feature — message snapshot archival
-- Stores a copy of every message for retrieval after deletion via the
-- snipe command. Attachments are stored as JSON metadata; binary files
-- go to S3 (separate layer).

CREATE TABLE message_snapshots (
    id              BIGSERIAL PRIMARY KEY,
    guild_id        TEXT NOT NULL,
    channel_id      TEXT NOT NULL,
    message_id      TEXT NOT NULL,
    author_id       TEXT NOT NULL,
    author_name     TEXT NOT NULL,
    author_bot      BOOLEAN NOT NULL DEFAULT FALSE,
    content         TEXT NOT NULL DEFAULT '',
    attachments     JSON,
    embeds          JSON,
    message_ts      TIMESTAMPTZ NOT NULL,
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_snapshots_channel_deleted
    ON message_snapshots(channel_id, deleted_at DESC)
    WHERE deleted_at IS NOT NULL;

CREATE INDEX idx_snapshots_message_id
    ON message_snapshots(message_id);

CREATE INDEX idx_snapshots_retention
    ON message_snapshots(created_at);
