package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

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

func NewDevelopment(level zapcore.Level) (*zap.Logger, error) {
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(level)
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg.Build()
}

func NewDevelopmentWithLogDir(level zapcore.Level, dir string, broadcaster *LogBroadcaster) (*zap.Logger, error) {
	if dir == "" && broadcaster == nil {
		return NewDevelopment(level)
	}

	var cores []zapcore.Core

	devLogger, err := NewDevelopment(level)
	if err != nil {
		return nil, err
	}
	cores = append(cores, devLogger.Core())

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

type syncFileWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func (w *syncFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	return w.f.Sync()
}
