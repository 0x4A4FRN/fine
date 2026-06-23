package executor

import (
	"context"

	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/replies"
)

type PingExecutor struct {
	replies replies.Renderer
	logger  *zap.Logger
}

func NewPingExecutor(
	replies replies.Renderer,
	logger *zap.Logger,
) *PingExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PingExecutor{replies: replies, logger: logger}
}

func (e *PingExecutor) Execute(_ context.Context, _ Action) error {
	e.logger.Info("executor: ping: executing")

	text := e.replies.Get("ping", "text", nil)

	e.logger.Info("executor: ping: produced reply",
		zap.Int("text_len", len(text)),
	)
	return &TextResult{Text: text}
}

var _ Executor = (*PingExecutor)(nil)
