package logging

import (
	"sync"
)

// historySize is the maximum number of log lines kept in the ring buffer
// for replay to new subscribers. 1000 lines is roughly 500KB–1MB depending
// on field density, which is negligible memory for a long-running bot.
const historySize = 1000

// LogBroadcaster fans out log lines to all active SSE subscribers.
// It implements zapcore.WriteSyncer so it can be composed into a zap
// core's output alongside stdout/file writers.
//
// Each subscriber gets a buffered channel. If a subscriber's buffer fills
// (slow consumer), lines are dropped for that subscriber only — the
// broadcaster never blocks the logger.
//
// A ring buffer of the last `historySize` lines is kept in memory so new
// subscribers can receive recent history before switching to live streaming.
type LogBroadcaster struct {
	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
	history     [][]byte // ring buffer of recent lines
}

func NewLogBroadcaster() *LogBroadcaster {
	return &LogBroadcaster{
		subscribers: make(map[chan []byte]struct{}),
		history:     make([][]byte, 0, historySize),
	}
}

// Subscribe returns a channel that receives log line bytes as they're
// written. Each line includes the trailing newline from the encoder.
// The caller MUST call Unsubscribe when done to avoid leaking the channel.
//
// maxLines controls how many history lines to replay before live streaming:
//   - maxLines > 0: replay the last maxLines lines (or fewer if history
//     has fewer), oldest first
//   - maxLines == 0: no history replay, live only
//   - maxLines < 0: replay all available history (up to historySize)
func (b *LogBroadcaster) Subscribe(maxLines int) <-chan []byte {
	ch := make(chan []byte, 256+historySize)

	b.mu.Lock()
	if maxLines != 0 {
		hist := b.history
		if maxLines > 0 && len(hist) > maxLines {
			// Only send the last maxLines entries.
			hist = hist[len(hist)-maxLines:]
		}
		for _, line := range hist {
			ch <- line
		}
	}
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	return ch
}

// Unsubscribe removes a subscriber and closes its channel. Safe to call
// multiple times.
func (b *LogBroadcaster) Unsubscribe(ch <-chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.subscribers {
		if c == ch {
			delete(b.subscribers, c)
			close(c)
			return
		}
	}
}

// Write implements zapcore.WriteSyncer. It fans out the bytes to all
// subscribers and appends to the history ring buffer. Non-blocking — if a
// subscriber's buffer is full, the line is dropped for that subscriber.
func (b *LogBroadcaster) Write(p []byte) (int, error) {
	// Copy the line so subscribers and history don't share the same
	// backing array (zap reuses buffers).
	line := make([]byte, len(p))
	copy(line, p)

	b.mu.Lock()
	// Append to history ring buffer.
	if len(b.history) < historySize {
		b.history = append(b.history, line)
	} else {
		// Ring buffer full — overwrite oldest. This rotates the slice
		// in place: shift left and append at end. The capacity is already
		// historySize so append won't reallocate.
		b.history = append(b.history[1:], line)
	}
	// Fan out to live subscribers.
	for ch := range b.subscribers {
		select {
		case ch <- line:
		default:
			// Buffer full — drop the line for this subscriber.
		}
	}
	b.mu.Unlock()
	return len(p), nil
}

// Sync implements zapcore.WriteSyncer (no-op).
func (b *LogBroadcaster) Sync() error { return nil }
