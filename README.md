# Fine — Discord Moderation Bot

Fine is a natural-language moderation bot for Discord. Users @mention the bot (or reply to one of its messages); Fine sends the cleaned text plus conversation history to an OpenAI-compatible LLM, asks the model to emit a strict JSON object matching its response schema, validates the result, and dispatches the chosen intent (ban, kick, timeout, purge, nickname, role, pin, etc.) to a per-intent executor. Destructive intents require a `yes`/`no` confirmation that expires after 60 seconds; the global `sudo_mode` guild setting bypasses confirmation per-guild. Every executed action is written to the `mod_actions` audit table.

The bot's personality lives entirely in YAML reply templates (`internal/replies/replies.yaml`) — Go code only renders and routes them, never decides word choice.

## Quick Start

### Prerequisites

- Go 1.26+
- PostgreSQL 14+
- A Discord bot token ([create one here](https://discord.com/developers/applications))
- An OpenAI-compatible LLM API key (OpenAI, OpenRouter, LM Studio, etc.)

### Setup

1. **Clone and build:**

   ```bash
   git clone https://github.com/0x4A4FRN/fine.git
   cd fine
   make build
   ```

2. **Create the database:**

   ```bash
   createdb fine
   psql fine -f migrations/000001_initial.up.sql
   ```

3. **Configure environment:**

   ```bash
   cp .env.example .env  # if available, or create manually
   ```

   Edit `.env` with your values (see [Configuration](#configuration) below).

4. **Run:**

   ```bash
   make run
   # or
   ./bin/fine
   ```

5. **Invite the bot to your server** with the following permissions:
   - Ban Members
   - Kick Members
   - Manage Messages
   - Manage Roles
   - Manage Nicknames
   - Moderate Members (timeout)
   - Deafen/Mute Members (voice)
   - Read Message History
   - Send Messages
   - Embed Links

   Required intents: Guilds, Guild Messages, Message Content.

## Build Commands

| Command | Purpose |
|---------|---------|
| `make build` | Release build with version stamping (`./bin/fine`) |
| `make dev` | Dev build stamped as `dev-<sha>` |
| `make run` | Dev build + run |
| `make clean` | Remove `./bin` |
| `go build ./cmd/fine` | Plain build (no version metadata) |

## Configuration

All configuration is via environment variables. No config files needed.

### Required

| Variable | Description |
|----------|-------------|
| `DISCORD_BOT_TOKEN` | Discord bot token from the Developer Portal |
| `DATABASE_URL` | PostgreSQL connection string (e.g. `postgres://user:pass@localhost:5432/fine`) |

### Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `LOG_DIR` | (empty) | If set, also writes JSON logs to `<dir>/fine.log` |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `LLM_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible API base URL |
| `LLM_API_KEY` | (empty) | API key(s). Accepts a single key or comma-separated list for round-robin rotation (e.g. `key1,key2,key3`) |
| `LLM_KEY_ROTATE_EVERY` | `25` | Number of requests before rotating to the next API key. Also rotates immediately on HTTP 429 |
| `LLM_MODEL` | `gpt-4o-mini` | Model name for chat completions |
| `LLM_TIMEOUT_MS` | `15000` | HTTP timeout for LLM requests (milliseconds) |
| `LLM_MAX_RETRIES` | `2` | Max retries on transient errors (5xx, network). Uses exponential backoff (1s, 2s, 4s) |
| `LLM_CONFIDENCE_THRESHOLD` | `0.7` | Minimum LLM confidence to act on a response |
| `CONVERSATION_WINDOW_MINUTES` | `30` | Conversation history window in minutes. Messages older than this are not included in LLM context |
| `CONVERSATION_HISTORY_MAX_MESSAGES` | `10` | Max number of messages sent to the LLM as conversation history |
| `CONVERSATION_RETENTION_DAYS` | `30` | Days before old conversation records are deleted from the DB |
| `CONFIRM_WINDOW_SECONDS` | `60` | Seconds before a destructive-intent confirmation prompt expires |
| `CACHE_HIT_CONFIDENCE_MIN` | `0.7` | Minimum confidence for intent cache hits to bypass the LLM |
| `AUDIT_RETENTION_DAYS` | `90` | Days before old audit records are deleted from the DB |
| `REPLIES_PATH` | `internal/replies/replies.yaml` | Path to the reply templates YAML file |
| `S3_ENDPOINT` | (empty) | Path-style S3-compatible endpoint for snipe attachments (Cloudflare R2, MinIO, Backblaze B2 all work). Empty disables attachment upload — text-only snipe still works |
| `S3_BUCKET` | (empty) | Bucket name. When empty, snipe attachments display as `(unavailable)` |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | (empty) | Static credentials (other providers will need their own pair) |
| `SNIPE_RETENTION_DAYS` | `7` | Days before old `message_snapshots` rows are deleted |

### Multi-API-Key Rotation

If you have a per-key token limit (e.g. 100k tokens/min per key), you can distribute load across multiple keys:

```bash
LLM_API_KEY=sk-key-aaa,sk-key-bbb,sk-key-ccc
LLM_KEY_ROTATE_EVERY=25
```

The bot rotates to the next key every 25 requests and immediately on HTTP 429 (rate limited). With a single key, rotation is disabled and behavior is identical to before.

**Sizing conversation history with token limits:**

Each LLM request has a fixed overhead of ~2,000 tokens (system prompt + JSON schema). Each history message pair (user + assistant) adds ~111 tokens. At 90k tokens/min per key:

| Keys | 10 req/min | 20 req/min | 30 req/min |
|------|-----------|-----------|-----------|
| 1    | 62 msgs   | 21 msgs   | 8 msgs    |
| 2    | 143 msgs  | 62 msgs   | 35 msgs   |
| 3    | 223 msgs  | 102 msgs  | 62 msgs   |

With 3 keys and ~20 req/min traffic, `CONVERSATION_HISTORY_MAX_MESSAGES=30` is a safe choice that stays well within budget.

## How It Works

```
User @mentions Fine
        │
        ▼
  ┌──────────────────┐
  │  bot.Handler      │  Mention detection, reply-chain, content cleaning
  └──────┬───────────┘
         │
         ▼
  ┌──────────────────┐
  │  Cache lookup     │  (guild_id, template) → cached intent? Skip LLM
  └──────┬───────────┘
         │ miss
         ▼
  ┌──────────────────┐
  │  LLM round-trip   │  System prompt + schema + history + user message
  └──────┬───────────┘
         │
         ▼
  ┌──────────────────┐
  │  Validate JSON    │  Schema, snowflake, target type, parameter checks
  └──────┬───────────┘
         │
         ▼
  ┌──────────────────┐
  │  executor.Router  │  Dispatch by intent → per-intent Executor
  └──────┬───────────┘
         │
         ▼
  ┌──────────────────┐
  │  Permission gate  │  Hierarchy, self-protection, snowflake, member existence
  └──────┬───────────┘
         │
    destructive?──── no ──→ Execute → Audit write → Reply
         │
         yes
         │
         ▼
  ┌──────────────────┐
  │  Confirmation     │  "Confirm: ban @user? Reply yes/no"
  │  window (60s)     │  Stored in suggestion_windows table
  └──────┬───────────┘
         │
    yes / no / expire
         │
    yes ──→ Execute → Audit write → Edit confirmation message to "Done"
    no  ──→ Cancelled
    expire → Sweep marks as expired
```

### Intents

Fine supports the following moderation intents:

| Intent | Description |
|--------|-------------|
| `ban` | Ban a user from the guild |
| `unban` | Unban a previously banned user |
| `kick` | Kick a user from the guild |
| `timeout` | Timeout a user (communication disabled) |
| `untimeout` | Remove a timeout early |
| `mute` | Mute a user in a voice channel |
| `unmute` | Unmute a user in a voice channel |
| `deafen` | Deafen a user in a voice channel |
| `undeafen` | Undeafen a user in a voice channel |
| `set_nickname` | Set a user's nickname (self-targeting allowed) |
| `reset_nickname` | Reset a user's nickname (self-targeting allowed) |
| `add_role` | Add a role to a user |
| `remove_role` | Remove a role from a user |
| `pin_message` | Pin a message |
| `unpin_message` | Unpin a message |
| `delete_message` | Delete a message |
| `purge_messages` | Bulk delete messages (up to 1000, bounded by Discord's 14-day bulk delete limit) |
| `snipe` | View recently deleted messages in the channel with pagination (Prev/Next/Delete buttons). Attachments are uploaded to S3 when configured. Requires Administrator, Manage Messages, or being the guild owner. |
| `toggle_setting` | Toggle a guild-wide bot setting (`sudo_mode` or `verbose_error`) |
| `audit_lookup` | Look up past moderation actions from the audit log |
| `ping` | Latency check |
| `help` | Help text |
| `info` | Bot info and stats |
| `status` | Gateway latency, uptime, DB latency |

### Guild Settings

Settings are toggled via natural language ("turn on sudo mode", "disable verbose error") and stored per-guild:

- **`sudo_mode`**: When on, destructive intents execute immediately without the yes/no confirmation step. Permission and hierarchy checks still apply.
- **`verbose_error`**: When on, error replies include a debug line with the underlying error message.

### Safety Features

- **Permission gate**: Every destructive intent checks that the actor has the required Discord permission (Ban Members, Kick Members, Moderate Members, etc.) before executing.
- **Hierarchy enforcement**: The bot and the actor must both outrank the target in the guild's role hierarchy.
- **Self-protection**: The bot refuses to act on itself, the actor, or the guild owner.
- **Snowflake validation**: All target IDs are validated against `^\d{17,20}$` before reaching the Discord API.
- **Negation override**: If the user says "don't", "never", "cancel", or "abort" in a destructive request, the bot aborts instead of executing.
- **Confirmation expiry**: Destructive confirmation prompts expire after 60 seconds (configurable). A background sweeper edits expired prompts to "Expired."
- **Audit trail**: Every executed action is recorded in `mod_actions` with actor, target, intent, reason, parameters, and timestamp. `sudo_mode` actions are marked with `"sudo": true` in the parameters JSON.

## Architecture

```
cmd/fine/main.go          Entry point — config, DI wiring, signal handling
internal/
  bot/                     Discord event handling (split across 12 files)
    handler.go             Handler struct, options, HandleMessageCreate, dispatch
    placeholder.go         "Thinking…" rotating placeholder UI
    timeout.go             Timeout lifecycle tracking & expiry sweeper
    cache_handler.go       Intent cache lookup and write-back
    confirm_handler.go     Yes/no confirmation flow
    confirm.go             Confirmation window DB operations
    replies_handler.go     Reply text rendering helpers
    dispatch.go            Executor dispatch bridge
    audit_handler.go       Audit lookup handling
    messages.go            Discord message I/O primitives
    intent.go              Intent classification helpers
    conversation_handler.go  Conversation persistence adapter
  executor/                Per-intent executors + router + permission gate
    router.go              Registry-map dispatch, DiscordAPI sub-interfaces
    permission.go          gate() — permission, hierarchy, self-protection
    helpers.go             Shared executor utilities
    guild_setting.go       GuildSettingsSnapshot (mutex-protected)
    ban.go, kick.go, ...   One file per intent
  llm/                     OpenAI-compatible HTTP client + JSON schema + validation
  config/                  Env-var → Config loader
  db/                      pgxpool wrapper + guild settings DB helpers
  discord/                 discordgo.Session wrapper
  conversation/            User-scoped conversation history store
  cache/                   Per-guild intent cache store
  audit/                   Append-only mod_actions writer + audit lookup
  sweep/                   Background sweeper for expired confirmation windows
  safety/                  Negation detection
  replies/                 YAML reply template loader
  logging/                 zap.Logger builder (with optional file logging)
  web/                     /healthz HTTP server
migrations/                SQL migrations
```

## Health Check

The bot exposes a simple HTTP endpoint at `:8080/healthz` that returns `OK` with a 200 status. This is a process liveness check with no authentication — do not extend it to handle sensitive state.

## Database

PostgreSQL is required. The schema is in `migrations/000001_initial.up.sql` and creates five tables:

- `conversations` / `conversation_messages` — conversation history for LLM context
- `mod_actions` — append-only audit log of all moderation actions
- `intent_cache` — per-guild (template → intent) cache to skip LLM round-trips
- `suggestion_windows` — confirmation prompt state for destructive intents
- `guild_settings` — per-guild `sudo_mode` and `verbose_error` flags

Run the migration:

```bash
psql fine -f migrations/000001_initial.up.sql
```

## License

See the repository for license information.
