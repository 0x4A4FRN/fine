package executor

import (
	"context"

	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/replies"
)

type HelpExecutor struct {
	replies *replies.Replies
	logger  *zap.Logger
}

func NewHelpExecutor(
	replies *replies.Replies,
	logger *zap.Logger,
) *HelpExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HelpExecutor{replies: replies, logger: logger}
}

func (e *HelpExecutor) Execute(_ context.Context, _ Action) error {
	e.logger.Info("executor: help: executing")

	text := e.replies.Get("help", "text", nil)

	e.logger.Info("executor: help: produced reply",
		zap.Int("text_len", len(text)),
	)
	return &TextResult{Text: text}
}

var _ Executor = (*HelpExecutor)(nil)
