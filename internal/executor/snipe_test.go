package executor

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/storage"
)

func newTestSnipeExecutor() *SnipeExecutor {
	return &SnipeExecutor{pages: make(map[string]*snipePage), logger: zap.NewNop()}
}

func makeTestSnaps(n int) []storage.Snapshot {
	now := time.Now()
	del := now
	snaps := make([]storage.Snapshot, n)
	for i := range snaps {
		snaps[i] = storage.Snapshot{
			ID:        int64(i + 1),
			MessageID: "msg-" + string(rune('A'+i)),
			Content:   "message " + string(rune('A'+i)),
			MessageTS: now.Add(-time.Duration(i) * time.Minute),
			DeletedAt: &del,
			AuthorID:  "111111111111111111",
		}
	}
	return snaps
}

func TestHandlePagination_BoundaryNext(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(3), currentIdx: 0, createdAt: time.Now()}
	snap, _, _ := e.HandlePagination(context.Background(), "bot-msg-1", "next")
	if snap != nil {
		t.Fatal("expected nil at next boundary (index 0)")
	}
}

func TestHandlePagination_BoundaryPrev(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(3), currentIdx: 2, createdAt: time.Now()}
	snap, _, _ := e.HandlePagination(context.Background(), "bot-msg-1", "prev")
	if snap != nil {
		t.Fatal("expected nil at prev boundary (last index)")
	}
}

func TestHandlePagination_NavigateForward(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(5), currentIdx: 0, createdAt: time.Now()}
	snap, _, _ := e.HandlePagination(context.Background(), "bot-msg-1", "prev")
	if snap == nil || snap.ID != 2 {
		t.Fatalf("expected snap ID 2 at index 1, got %+v", snap)
	}
	snap, _, _ = e.HandlePagination(context.Background(), "bot-msg-1", "prev")
	if snap == nil || snap.ID != 3 {
		t.Fatalf("expected snap ID 3 at index 2, got %+v", snap)
	}
}

func TestHandlePagination_NavigateBackward(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(5), currentIdx: 4, createdAt: time.Now()}
	snap, _, _ := e.HandlePagination(context.Background(), "bot-msg-1", "next")
	if snap == nil || snap.ID != 4 {
		t.Fatalf("expected snap ID 4 at index 3, got %+v", snap)
	}
}

func TestHandlePagination_ExpiredState(t *testing.T) {
	e := newTestSnipeExecutor()
	snap, _, _ := e.HandlePagination(context.Background(), "nonexistent", "prev")
	if snap != nil {
		t.Fatal("expected nil for expired/missing page state")
	}
}

func TestHandlePagination_InvalidDirection(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(3), currentIdx: 1, createdAt: time.Now()}
	snap, _, _ := e.HandlePagination(context.Background(), "bot-msg-1", "sideways")
	if snap != nil {
		t.Fatal("expected nil for invalid direction")
	}
}

func TestHandlePagination_ConcurrentClicks(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(100), currentIdx: 0, createdAt: time.Now()}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.HandlePagination(context.Background(), "bot-msg-1", "prev")
		}()
	}
	wg.Wait()
	e.pagesMu.Lock()
	idx := e.pages["bot-msg-1"].currentIdx
	e.pagesMu.Unlock()
	if idx != 50 {
		t.Fatalf("expected currentIdx=50, got %d", idx)
	}
}

func TestDeletePage(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(3), currentIdx: 0, createdAt: time.Now()}
	e.DeletePage("bot-msg-1")
	e.pagesMu.Lock()
	_, exists := e.pages["bot-msg-1"]
	e.pagesMu.Unlock()
	if exists {
		t.Fatal("expected page to be deleted")
	}
}

func TestDeletePage_Nonexistent(t *testing.T) {
	e := newTestSnipeExecutor()
	e.DeletePage("nonexistent")
}

func TestSourceMsgID(t *testing.T) {
	e := newTestSnipeExecutor()
	e.pages["bot-msg-1"] = &snipePage{snaps: makeTestSnaps(3), currentIdx: 0, createdAt: time.Now(), sourceMsgID: "invoke-msg-123"}
	got := e.SourceMsgID("bot-msg-1")
	if got != "invoke-msg-123" {
		t.Fatalf("expected 'invoke-msg-123', got %q", got)
	}
}

func TestSourceMsgID_Expired(t *testing.T) {
	e := newTestSnipeExecutor()
	got := e.SourceMsgID("nonexistent")
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}
