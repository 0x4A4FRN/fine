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
// NewDevelopment.
func NewDevelopmentWithLogDir(level zapcore.Level, dir string) (*zap.Logger, error) {
	if dir == "" {
		return NewDevelopment(level)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logging: creating log dir %q: %w", dir, err)
	}

	logPath := filepath.Join(dir, "fine.log")
	fileEncoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	fileCore := zapcore.NewTee(
		zapcore.NewCore(
			fileEncoder,
			zapcore.AddSync(&syncFileWriter{path: logPath}),
			level,
		),
	)

	devLogger, err := NewDevelopment(level)
	if err != nil {
		return nil, err
	}

	return zap.New(
		zapcore.NewTee(devLogger.Core(), fileCore),
	), nil
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
