package executor

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/audit"
	"github.com/0x4A4FRN/fine/internal/replies"
	"github.com/0x4A4FRN/fine/internal/storage"
)

type StatusExecutor struct {
	discord   BotInfoAPI
	pool      audit.DB
	uploader  storage.Uploader
	replies   replies.Renderer
	startedAt time.Time
	logger    *zap.Logger
}

func NewStatusExecutor(
	discord BotInfoAPI,
	pool audit.DB,
	uploader storage.Uploader,
	replies replies.Renderer,
	startedAt time.Time,
	logger *zap.Logger,
) *StatusExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &StatusExecutor{
		discord:   discord,
		pool:      pool,
		uploader:  uploader,
		replies:   replies,
		startedAt: startedAt,
		logger:    logger,
	}
}

func (e *StatusExecutor) Execute(ctx context.Context, _ Action) error {
	e.logger.Info("executor: status: executing")

	gatewayLatency := e.discord.HeartbeatLatency().Round(time.Millisecond)
	uptime := time.Since(e.startedAt).Round(time.Second)

	dbLatency := time.Duration(0)
	dbErr := error(errors.New("db: not configured"))
	if e.pool != nil {
		dbLatency, dbErr = measureDBLatency(ctx, e.pool)
	}

	s3Latency := time.Duration(0)
	s3Err := error(errors.New("s3: not configured"))
	if e.uploader != nil {
		s3Latency, s3Err = e.uploader.Ping(ctx)
	}

	vars := map[string]string{
		"gateway_latency": gatewayLatency.String(),
		"db_latency":      dbLatency.Round(time.Millisecond).String(),
		"s3_latency":      s3Latency.Round(time.Millisecond).String(),
		"uptime":          uptime.String(),
	}
	if dbErr != nil {
		vars["db_status"] = "error"
		vars["db_error"] = dbErr.Error()
	} else {
		vars["db_status"] = "ok"
	}
	if s3Err != nil {
		vars["s3_status"] = "error"
		vars["s3_error"] = s3Err.Error()
	} else {
		vars["s3_status"] = "ok"
	}

	body := e.replies.Get("status", "text", vars)
	footer := e.replies.Get("status", "footer", nil)

	e.logger.Info("executor: status: produced reply",
		zap.String("gateway_latency", vars["gateway_latency"]),
		zap.String("db_latency", vars["db_latency"]),
		zap.String("db_status", vars["db_status"]),
		zap.String("s3_latency", vars["s3_latency"]),
		zap.String("s3_status", vars["s3_status"]),
		zap.String("uptime", vars["uptime"]),
	)
	return &TextResult{Text: body + "\n" + footer}
}

const statusDBPingSQL = "SELECT 1"

func measureDBLatency(ctx context.Context, pool audit.DB) (time.Duration, error) {
	start := time.Now()
	var dummy int
	if err := pool.QueryRow(ctx, statusDBPingSQL).Scan(&dummy); err != nil {
		return time.Since(start), errors.Join(errors.New("status: db ping"), err)
	}
	return time.Since(start), nil
}

var _ Executor = (*StatusExecutor)(nil)
