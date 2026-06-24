package sweep

import (
	"context"
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

type mockRenderer struct {
	result string
}

func (m *mockRenderer) Get(_, _ string, _ any) string {
	return m.result
}
func (m *mockRenderer) Render(_ string, _ any) (string, error) {
	return m.result, nil
}
func (m *mockRenderer) Has(_, _ string) bool { return true }

type mockRows struct {
	data    [][4]any
	pos     int
	scanErr error
	rowsErr error
}

func (r *mockRows) Close() {}
func (r *mockRows) Err() error {
	return r.rowsErr
}
func (r *mockRows) Conn() *pgx.Conn                              { return nil }
func (r *mockRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *mockRows) Values() ([]any, error)                       { return nil, nil }
func (r *mockRows) RawValues() [][]byte                          { return nil }
func (r *mockRows) Next() bool {
	if r.pos >= len(r.data) {
		return false
	}
	r.pos++
	return true
}
func (r *mockRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.data[r.pos-1]
	*dest[0].(*int64) = row[0].(int64)
	*dest[1].(*string) = row[1].(string)
	*dest[2].(*string) = row[2].(string)
	*dest[3].(*string) = row[3].(string)
	return nil
}

type execCall struct {
	tag pgconn.CommandTag
	err error
}

type mockDB struct {
	rows      pgx.Rows
	queryErr  error
	execCalls []execCall
	execIdx   int
}

func (m *mockDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return m.rows, m.queryErr
}

func (m *mockDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	if m.execIdx >= len(m.execCalls) {

		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	c := m.execCalls[m.execIdx]
	m.execIdx++
	return c.tag, c.err
}

type mockEditor struct {
	calls   []struct{ channelID, msgID, content string }
	editErr error
}

func (e *mockEditor) ChannelMessageEdit(channelID, msgID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	e.calls = append(e.calls, struct{ channelID, msgID, content string }{channelID, msgID, content})
	return nil, e.editErr
}

func TestExtractOriginalConfirmText_Empty(t *testing.T) {
	if got := extractOriginalConfirmText(""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestExtractOriginalConfirmText_InvalidJSON(t *testing.T) {
	if got := extractOriginalConfirmText("not json {{{"); got != "" {
		t.Fatalf("expected empty for invalid JSON, got %q", got)
	}
}

func TestExtractOriginalConfirmText_MissingField(t *testing.T) {
	if got := extractOriginalConfirmText(`{"other":"value"}`); got != "" {
		t.Fatalf("expected empty for missing field, got %q", got)
	}
}

func TestExtractOriginalConfirmText_WithField(t *testing.T) {
	payload := `{"original_confirm_text":"~~Ban Alice?~~ Expired."}`
	want := "~~Ban Alice?~~ Expired."
	if got := extractOriginalConfirmText(payload); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildExpiredText_NilRenderer_ReturnsFallback(t *testing.T) {
	got := buildExpiredText(nil, `{"original_confirm_text":"Ban Alice?"}`)
	if got != fallbackExpiredText {
		t.Fatalf("expected fallback %q, got %q", fallbackExpiredText, got)
	}
}

func TestBuildExpiredText_EmptyPayload_ReturnsFallback(t *testing.T) {
	got := buildExpiredText(&mockRenderer{result: "rendered"}, "")
	if got != fallbackExpiredText {
		t.Fatalf("expected fallback for empty payload, got %q", got)
	}
}

func TestBuildExpiredText_ValidPayload_UsesRenderer(t *testing.T) {
	renderer := &mockRenderer{result: "~~Ban Alice?~~ Expired."}
	payload := `{"original_confirm_text":"Ban Alice?"}`
	got := buildExpiredText(renderer, payload)
	if got != "~~Ban Alice?~~ Expired." {
		t.Fatalf("expected renderer output, got %q", got)
	}
}

func makeWindowRows(windows ...expiredWindow) *mockRows {
	data := make([][4]any, len(windows))
	for i, w := range windows {
		data[i] = [4]any{w.ID, w.ChannelID, w.BotMessageID, w.Payload}
	}
	return &mockRows{data: data}
}

func TestSweepOnce_NoExpiredWindows_NoOp(t *testing.T) {
	db := &mockDB{rows: &mockRows{}}
	editor := &mockEditor{}
	SweepOnce(context.Background(), db, editor, nil, zap.NewNop())
	if len(editor.calls) != 0 {
		t.Fatalf("expected 0 edit calls for empty result, got %d", len(editor.calls))
	}
}

func TestSweepOnce_QueryError_SkipsProcessing(t *testing.T) {
	db := &mockDB{rows: nil, queryErr: errors.New("db down")}
	editor := &mockEditor{}

	SweepOnce(context.Background(), db, editor, nil, zap.NewNop())
	if len(editor.calls) != 0 {
		t.Fatalf("expected 0 edit calls after query error, got %d", len(editor.calls))
	}
}

func TestSweepOnce_HappyPath_EditsMessage(t *testing.T) {
	w := expiredWindow{
		ID:           42,
		ChannelID:    "chan-1",
		BotMessageID: "msg-1",
		Payload:      `{"original_confirm_text":"Ban Alice?"}`,
	}
	db := &mockDB{
		rows: makeWindowRows(w),
		execCalls: []execCall{
			{tag: pgconn.NewCommandTag("UPDATE 1")},
		},
	}
	editor := &mockEditor{}
	renderer := &mockRenderer{result: "~~Ban Alice?~~ Expired."}

	SweepOnce(context.Background(), db, editor, renderer, zap.NewNop())

	if len(editor.calls) != 1 {
		t.Fatalf("expected 1 edit call, got %d", len(editor.calls))
	}
	call := editor.calls[0]
	if call.channelID != "chan-1" || call.msgID != "msg-1" {
		t.Errorf("wrong channel/msg: %+v", call)
	}
	if call.content != "~~Ban Alice?~~ Expired." {
		t.Errorf("unexpected content: %q", call.content)
	}
}

func TestSweepOnce_AlreadyResolved_SkipsEdit(t *testing.T) {

	w := expiredWindow{ID: 7, ChannelID: "c", BotMessageID: "m", Payload: "{}"}
	db := &mockDB{
		rows: makeWindowRows(w),
		execCalls: []execCall{
			{tag: pgconn.NewCommandTag("UPDATE 0")},
		},
	}
	editor := &mockEditor{}
	SweepOnce(context.Background(), db, editor, nil, zap.NewNop())
	if len(editor.calls) != 0 {
		t.Fatalf("expected 0 edits when RowsAffected=0, got %d", len(editor.calls))
	}
}

func TestSweepOnce_ExecError_ContinuesToNextWindow(t *testing.T) {
	w1 := expiredWindow{ID: 1, ChannelID: "c", BotMessageID: "m1", Payload: "{}"}
	w2 := expiredWindow{ID: 2, ChannelID: "c", BotMessageID: "m2", Payload: "{}"}
	db := &mockDB{
		rows: makeWindowRows(w1, w2),
		execCalls: []execCall{
			{tag: pgconn.CommandTag{}, err: errors.New("lock timeout")},
			{tag: pgconn.NewCommandTag("UPDATE 1")},
		},
	}
	editor := &mockEditor{}
	SweepOnce(context.Background(), db, editor, nil, zap.NewNop())

	if len(editor.calls) != 1 {
		t.Fatalf("expected 1 edit (for w2), got %d", len(editor.calls))
	}
	if editor.calls[0].msgID != "m2" {
		t.Errorf("expected m2, got %q", editor.calls[0].msgID)
	}
}

func TestSweepOnce_EditorError_ContinuesToNextWindow(t *testing.T) {
	w1 := expiredWindow{ID: 1, ChannelID: "c", BotMessageID: "m1", Payload: "{}"}
	w2 := expiredWindow{ID: 2, ChannelID: "c", BotMessageID: "m2", Payload: "{}"}
	db := &mockDB{rows: makeWindowRows(w1, w2)}
	editor := &mockEditor{editErr: errors.New("discord 403")}

	SweepOnce(context.Background(), db, editor, nil, zap.NewNop())
	if len(editor.calls) != 2 {
		t.Fatalf("expected editor called twice (even on error), got %d", len(editor.calls))
	}
}

func TestSweepOnce_NilEditor_NoOp(t *testing.T) {
	w := expiredWindow{ID: 1, ChannelID: "c", BotMessageID: "m", Payload: "{}"}
	db := &mockDB{rows: makeWindowRows(w)}

	SweepOnce(context.Background(), db, nil, nil, zap.NewNop())

}

func TestSweepOnce_MultipleWindows_AllEdited(t *testing.T) {
	windows := []expiredWindow{
		{ID: 1, ChannelID: "c", BotMessageID: "m1", Payload: "{}"},
		{ID: 2, ChannelID: "c", BotMessageID: "m2", Payload: "{}"},
		{ID: 3, ChannelID: "c", BotMessageID: "m3", Payload: "{}"},
	}
	db := &mockDB{rows: makeWindowRows(windows...)}
	editor := &mockEditor{}
	SweepOnce(context.Background(), db, editor, nil, zap.NewNop())
	if len(editor.calls) != 3 {
		t.Fatalf("expected 3 edits, got %d", len(editor.calls))
	}
}
