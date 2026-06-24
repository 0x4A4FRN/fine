package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/bot"
	"github.com/0x4A4FRN/fine/internal/cache"
	"github.com/0x4A4FRN/fine/internal/config"
	"github.com/0x4A4FRN/fine/internal/conversation"
	"github.com/0x4A4FRN/fine/internal/db"
	"github.com/0x4A4FRN/fine/internal/discord"
	"github.com/0x4A4FRN/fine/internal/executor"
	"github.com/0x4A4FRN/fine/internal/llm"
	"github.com/0x4A4FRN/fine/internal/logging"
	"github.com/0x4A4FRN/fine/internal/replies"
	"github.com/0x4A4FRN/fine/internal/storage"
	"github.com/0x4A4FRN/fine/internal/sweep"
	"github.com/0x4A4FRN/fine/internal/web"
)

var (
	Version      = "dev"
	Commit       = "xxxxxxx"
	BuildDate    = "unknown"
	GoVersionStr = "dev"
)

type discordAdapter struct {
	*discord.Session
}

var _ executor.DiscordAPI = (*discordAdapter)(nil)
var _ bot.DiscordSessionAPI = (*discordAdapter)(nil)
var _ bot.DiscordMessageAPI = (*discordAdapter)(nil)

func (d *discordAdapter) BotUserID() string {
	if d.Session == nil {
		return ""
	}
	u := d.Session.State.User
	if u == nil {
		return ""
	}
	return u.ID
}

func main() {

	bootstrapLogger, _ := logging.NewDevelopment(zapcore.InfoLevel)
	defer func() {
		_ = bootstrapLogger.Sync()
	}()

	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger.Fatal("config load failed", zap.Error(err))
	}

	lvl, _ := logging.ParseLevel(cfg.LogLevel)

	var logBroadcaster *logging.LogBroadcaster
	if cfg.LogStreamSecret != "" {
		logBroadcaster = logging.NewLogBroadcaster()
	}

	logger, err := logging.NewDevelopmentWithLogDir(lvl, cfg.LogDir, logBroadcaster)
	if err != nil {
		bootstrapLogger.Fatal("logger build failed",
			zap.String("log_level", cfg.LogLevel),
			zap.String("log_dir", cfg.LogDir),
			zap.Error(err),
		)
	}
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info("main: startup",
		zap.String("log_level", cfg.LogLevel),
		zap.String("llm_model", cfg.LLMModel),
		zap.String("replies_path", cfg.RepliesPath),
		zap.String("version", Version),
		zap.String("commit", Commit),
		zap.String("build_date", BuildDate),
		zap.String("go_version", GoVersionStr),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal("db pool init failed", zap.Error(err))
	}
	defer pool.Close()
	logger.Info("db pool initialized")

	session, err := discord.NewSession(cfg.BotToken)
	if err != nil {
		logger.Fatal("discord session failed", zap.Error(err))
	}
	defer session.Close()
	logger.Info("discord session created",
		zap.Uint64("intents", uint64(session.Identify.Intents)),
	)

	llmClient := llm.NewOpenAIClient(
		llm.WithBaseURL(cfg.LLMBaseURL),
		llm.WithAPIKeys(cfg.LLMAPIKeys, cfg.LLMKeyRotateEvery),
		llm.WithModel(cfg.LLMModel),
		llm.WithLogger(logger),
		llm.WithTimeout(cfg.LLMTimeout),
		llm.WithMaxRetries(cfg.LLMMaxRetries),
	)
	logger.Info("llm client initialized",
		zap.String("base_url", cfg.LLMBaseURL),
		zap.String("model", cfg.LLMModel),
		zap.Int("api_key_count", len(cfg.LLMAPIKeys)),
		zap.Int("key_rotate_every", cfg.LLMKeyRotateEvery),
	)

	convStore := conversation.NewStore(
		pool,
		time.Duration(cfg.ConversationWindowMinutes)*time.Minute,
		cfg.ConversationHistoryMaxMessages,
	)

	cacheStore := cache.NewStore(pool)

	replyRenderer, err := replies.Load(cfg.RepliesPath)
	if err != nil {
		logger.Fatal("replies load failed",
			zap.String("path", cfg.RepliesPath),
			zap.Error(err),
		)
	}
	logger.Info("replies loaded", zap.String("path", cfg.RepliesPath))

	startedAt := time.Now()
	discordAPI := &discordAdapter{Session: session}

	settingsSnapshot := executor.NewGuildSettingsSnapshot()
	loaded, err := pool.LoadAllGuildSettings(ctx)
	if err != nil {
		logger.Fatal("db: load guild settings failed", zap.Error(err))
	}
	for _, gs := range loaded {
		settingsSnapshot.Set(executor.GuildSettings{
			GuildID:      gs.GuildID,
			SudoMode:     gs.SudoMode,
			VerboseError: gs.VerboseError,
			UpdatedBy:    gs.UpdatedBy,
		})
	}
	logger.Info("main: guild settings hydrated",
		zap.Int("guild_count", len(loaded)),
	)

	var s3Uploader storage.Uploader
	if cfg.S3Bucket != "" && cfg.S3Endpoint != "" {
		s3Cfg := storage.S3Config{
			Endpoint:  cfg.S3Endpoint,
			Bucket:    cfg.S3Bucket,
			Region:    cfg.S3Region,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
		}
		uploader, err := storage.NewS3Uploader(ctx, s3Cfg)
		if err != nil {
			logger.Fatal("main: S3 uploader init failed", zap.Error(err))
		}
		s3Uploader = uploader
		logger.Info("main: S3 uploader initialized",
			zap.String("endpoint", cfg.S3Endpoint),
			zap.String("bucket", cfg.S3Bucket),
		)
	} else {
		logger.Info("main: S3 not configured; snipe will work for text-only messages")
	}

	snapshotStore := storage.NewStore(pool)

	router := executor.NewRouter(
		discordAPI,
		pool,
		replyRenderer,
		startedAt,
		executor.WithLogger(logger),
		executor.WithGuildSettings(
			settingsSnapshot, &guildSettingsDBAdapter{inner: pool},
		),
		executor.WithBuildInfo(executor.BuildInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: BuildDate,
			GoVersion: GoVersionStr,
		}),
		executor.WithSnipeExecutor(snapshotStore, s3Uploader),
	)
	router.StartBackgroundWorkers()

	handler := bot.NewHandler(
		llmClient,
		bot.WithSystemPrompt(llm.DefaultSystemPrompt()),
		bot.WithDiscord(session),
		bot.WithExecutor(router),
		bot.WithWindowDB(pool),
		bot.WithConversationStore(convStore),
		bot.WithCacheStore(cacheStore),
		bot.WithAuditDB(pool),
		bot.WithReplies(replyRenderer),
		bot.WithGuildSettings(settingsSnapshot),
		bot.WithLogger(logger),
		bot.WithCacheHitThreshold(cfg.CacheHitConfidenceMin),
		bot.WithConfirmWindowDuration(time.Duration(cfg.ConfirmWindowSeconds)*time.Second),
		bot.WithStorageStore(snapshotStore),
		bot.WithStorageUploader(s3Uploader),
		bot.WithSnipePaginationFn(router.SnipePagination),
		bot.WithSnipeSourceMsgIDFn(router.SnipeSourceMsgID),
		bot.WithSnipeDeletePageFn(router.SnipeDeletePage),
		bot.WithPreCheckPermissionFn(router.PreCheckPermission),
		bot.WithPreCheckActionPermissionFn(router.PreCheckActionPermission),
		bot.WithPurgeScanFn(router.PurgeScan),
	)

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		handler.SetBotID(r.User.ID)
		logger.Info("main: logged in (Ready event)",
			zap.String("bot_id", r.User.ID),
			zap.String("username", r.User.Username),
			zap.String("discriminator", r.User.Discriminator),
			zap.Int("guild_count", len(r.Guilds)),
		)
	})

	session.AddHandler(handler.HandleMessageCreateSnapshot)
	session.AddHandler(handler.HandleMessageCreate)
	session.AddHandler(handler.HandleMessageDelete)
	session.AddHandler(handler.HandleMessageDeleteBulk)
	session.AddHandler(handler.HandleInteractionCreate)
	session.AddHandler(handler.OnGuildMemberUpdate)

	if err := session.Open(); err != nil {
		logger.Fatal("discord gateway open failed", zap.Error(err))
	}
	logger.Info("discord gateway opened")

	go sweep.Start(ctx, pool, session, replyRenderer, logger)

	go handler.StartTimeoutExpirySweep(ctx, 30*time.Second)

	go web.Serve(ctx, ":8080", logger, logBroadcaster, cfg.LogStreamSecret)

	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				retentionSweep(ctx, pool, snapshotStore, cfg, logger)
			}
		}
	}()

	logger.Info("main: bot is running. Press Ctrl-C to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("main: shutting down...")

	router.Stop()
}

func retentionSweep(ctx context.Context, pool *db.Pool, snapshotStore *storage.Store, cfg *config.Config, logger *zap.Logger) {
	sweeps := []struct {
		label string
		fn    func() error
	}{
		{
			"conversation_messages",
			func() error {
				_, err := pool.Exec(ctx, "DELETE FROM conversation_messages WHERE created_at < NOW() - $1 * INTERVAL '1 day'", cfg.ConversationRetentionDays)
				return err
			},
		},
		{
			"conversations",
			func() error {
				_, err := pool.Exec(ctx, "DELETE FROM conversations WHERE last_active_at < NOW() - $1 * INTERVAL '1 day'", cfg.ConversationRetentionDays)
				return err
			},
		},
		{
			"mod_actions",
			func() error {
				_, err := pool.Exec(ctx, "DELETE FROM mod_actions WHERE executed_at < NOW() - $1 * INTERVAL '1 day'", cfg.AuditRetentionDays)
				return err
			},
		},
	}
	for _, s := range sweeps {
		if err := s.fn(); err != nil {
			logger.Error("main: retention sweep failed", zap.String("table", s.label), zap.Error(err))
		}
	}

	deleted, err := snapshotStore.SweepRetention(ctx, cfg.SnipeRetentionDays)
	if err != nil {
		logger.Error("main: snipe retention", zap.Error(err))
	} else if deleted > 0 {
		logger.Info("main: snipe retention sweep", zap.Int64("deleted", deleted))
	}
}

var _ audit.DB = (*db.Pool)(nil)

type guildSettingsDBAdapter struct{ inner *db.Pool }

func (a guildSettingsDBAdapter) UpsertGuildSettings(
	ctx context.Context, gs executor.GuildSettings,
) error {
	return a.inner.UpsertGuildSettings(ctx, db.GuildSettings{
		GuildID:      gs.GuildID,
		SudoMode:     gs.SudoMode,
		VerboseError: gs.VerboseError,
		UpdatedBy:    gs.UpdatedBy,
	})
}

var _ executor.GuildSettingsDB = (*guildSettingsDBAdapter)(nil)
