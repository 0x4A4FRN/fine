package web

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"
)

func Serve(ctx context.Context, addr string, logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		logger.Info("web: health server shutting down")
	}()

	logger.Info("web: health server listening",
		zap.String("addr", addr),
	)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("web: server error", zap.Error(err))
	}
}
