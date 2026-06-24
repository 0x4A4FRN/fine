package executor

import (
	"context"
	"runtime"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/replies"
)

type InfoExecutor struct {
	discord   BotInfoAPI
	replies   replies.Renderer
	startedAt time.Time
	buildInfo BuildInfo
	logger    *zap.Logger
}

func NewInfoExecutor(
	discord BotInfoAPI,
	replies replies.Renderer,
	startedAt time.Time,
	buildInfo BuildInfo,
	logger *zap.Logger,
) *InfoExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &InfoExecutor{
		discord:   discord,
		replies:   replies,
		startedAt: startedAt,
		buildInfo: buildInfo,
		logger:    logger,
	}
}

func (e *InfoExecutor) Execute(ctx context.Context, _ Action) error {
	e.logger.Info("executor: info: executing")

	uptime := time.Since(e.startedAt).Round(time.Second)
	guilds := e.discord.GuildCount()
	users := e.discord.TotalMemberCount()

	version := e.buildInfo.Version
	if version == "" {
		version = "dev"
	}
	commit := e.buildInfo.Commit
	if commit == "" {
		commit = "unknown"
	}
	goVer := e.buildInfo.GoVersion
	if goVer == "" {
		goVer = runtime.Version()
	}
	buildDate := e.buildInfo.BuildDate
	if buildDate == "" {
		buildDate = "unknown"
	}

	vars := map[string]string{
		"guilds":     strconv.Itoa(guilds),
		"users":      strconv.Itoa(users),
		"uptime":     uptime.String(),
		"version":    version,
		"commit":     commit,
		"go_version": goVer,
		"build_date": buildDate,
	}

	text := e.replies.Get("info", "text", vars)
	e.logger.Info("executor: info: produced reply",
		zap.Int("guilds", guilds),
		zap.Int("users", users),
		zap.String("uptime", uptime.String()),
		zap.String("version", version),
		zap.Int("text_len", len(text)),
	)
	return &TextResult{Text: text}
}

var _ Executor = (*InfoExecutor)(nil)
