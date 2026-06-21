package logging

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func ParseLevel(s string) (zapcore.Level, error) {
	switch s {
	case "":
		return zapcore.InfoLevel, nil
	}
	var lvl zapcore.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return zapcore.InfoLevel,
			fmt.Errorf("logging: invalid level %q (using info): %w", s, err)
	}
	return lvl, nil
}

func New(level zapcore.Level) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(level)
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg.Build()
}

func NewDevelopment(level zapcore.Level) (*zap.Logger, error) {
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(level)
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg.Build()
}

// NewDevelopmentWithLogDir creates a development logger that also tee-writes
// JSON-encoded logs to a file in dir. If dir is empty, behaves identically to
// NewDevelopment. If broadcaster is non-nil, log lines are also fanned out
// to SSE subscribers for live log streaming.
func NewDevelopmentWithLogDir(level zapcore.Level, dir string, broadcaster *LogBroadcaster) (*zap.Logger, error) {
	if dir == "" && broadcaster == nil {
		return NewDevelopment(level)
	}

	var cores []zapcore.Core

	// Always include the development (console) logger.
	devLogger, err := NewDevelopment(level)
	if err != nil {
		return nil, err
	}
	cores = append(cores, devLogger.Core())

	// Optional file output (JSON).
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logging: creating log dir %q: %w", dir, err)
		}
		logPath := filepath.Join(dir, "fine.log")
		fileEncoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
		cores = append(cores, zapcore.NewCore(
			fileEncoder,
			zapcore.AddSync(&syncFileWriter{path: logPath}),
			level,
		))
	}

	// Optional SSE broadcaster (JSON, same as file).
	if broadcaster != nil {
		broadcastCfg := zap.NewProductionEncoderConfig()
		broadcastCfg.TimeKey = "ts"
		broadcastCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		broadcastEncoder := zapcore.NewJSONEncoder(broadcastCfg)
		cores = append(cores, zapcore.NewCore(
			broadcastEncoder,
			zapcore.AddSync(broadcaster),
			level,
		))
	}

	return zap.New(zapcore.NewTee(cores...)), nil
}

// syncFileWriter is a minimal write-syncer that opens a file for appending
// and writes to it. It does not buffer.
type syncFileWriter struct {
	path string
	f    *os.File
}

func (w *syncFileWriter) Write(p []byte) (int, error) {
	if w.f == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		w.f = f
	}
	return w.f.Write(p)
}

func (w *syncFileWriter) Sync() error {
	if w.f == nil {
		return nil
	}
	return w.f.Sync()
}
