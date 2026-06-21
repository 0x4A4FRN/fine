package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// LogStreamer is the interface the /logs SSE handler needs from the
// logging broadcaster. Defined consumer-side so the web package doesn't
// depend on the logging package.
type LogStreamer interface {
	Subscribe(maxLines int) <-chan []byte
	Unsubscribe(ch <-chan []byte)
}

func Serve(ctx context.Context, addr string, logger *zap.Logger, logStreamer LogStreamer, logStreamSecret string) {
	if logger == nil {
		logger = zap.NewNop()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	if logStreamer != nil {
		mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
			handleLogStream(w, r, logStreamer, logStreamSecret, logger)
		})
	}

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
		zap.Bool("log_stream_enabled", logStreamer != nil),
	)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("web: server error", zap.Error(err))
	}
}

// handleLogStream streams log lines as Server-Sent Events. The client
// connects with:
//
//	curl --no-buffer -H "Authorization: Bearer <secret>" http://host:8080/logs
//
// If no secret is configured (empty string), the endpoint returns 503.
// If the secret doesn't match, the endpoint returns 401.
func handleLogStream(w http.ResponseWriter, r *http.Request, streamer LogStreamer, secret string, logger *zap.Logger) {
	if secret == "" {
		http.Error(w, "log streaming not configured (set LOG_STREAM_SECRET)", http.StatusServiceUnavailable)
		return
	}

	provided := r.Header.Get("Authorization")
	if provided != "Bearer "+secret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Parse ?lines=N query param. Default is 100 (recent history).
	// ?lines=0 disables history (live only). ?lines=-1 or ?lines=all
	// replays the full buffer (up to 1000 lines).
	maxLines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if l == "all" {
			maxLines = -1
		} else if n, err := strconv.Atoi(l); err == nil {
			maxLines = n
		}
	}

	ch := streamer.Subscribe(maxLines)
	defer streamer.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			// SSE format: "data: <line>\n\n". The line already has a
			// trailing newline from the encoder; trim it and let SSE
			// add its own framing.
			text := string(line)
			if len(text) > 0 && text[len(text)-1] == '\n' {
				text = text[:len(text)-1]
			}
			fmt.Fprintf(w, "data: %s\n\n", text)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
