package logging

import (
	"sync"
)

const historySize = 1000

type LogBroadcaster struct {
	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
	history     [][]byte
	head        int
	count       int
}

func NewLogBroadcaster() *LogBroadcaster {
	return &LogBroadcaster{
		subscribers: make(map[chan []byte]struct{}),
		history:     make([][]byte, historySize),
	}
}

func (b *LogBroadcaster) oldestToNewest() [][]byte {
	if b.count == 0 {
		return nil
	}
	out := make([][]byte, b.count)
	for i := 0; i < b.count; i++ {
		out[i] = b.history[(b.head+i)%historySize]
	}
	return out
}

func (b *LogBroadcaster) Subscribe(maxLines int) <-chan []byte {
	ch := make(chan []byte, 256+historySize)

	b.mu.Lock()
	if maxLines != 0 {
		hist := b.oldestToNewest()
		if maxLines > 0 && len(hist) > maxLines {
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

func (b *LogBroadcaster) Write(p []byte) (int, error) {
	line := make([]byte, len(p))
	copy(line, p)

	b.mu.Lock()

	if b.count < historySize {
		b.history[b.count] = line
		b.count++
	} else {
		b.history[b.head] = line
		b.head = (b.head + 1) % historySize
	}

	for ch := range b.subscribers {
		select {
		case ch <- line:
		default:
		}
	}
	b.mu.Unlock()
	return len(p), nil
}

func (b *LogBroadcaster) Sync() error { return nil }
