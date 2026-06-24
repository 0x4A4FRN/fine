package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/0x4A4FRN/fine/internal/llm"
)

// ── stubExecutor ───────────────────────────────────────────────────────────

type stubExecutor struct {
	calls     []Action
	returnErr error
}

func (s *stubExecutor) Execute(_ context.Context, action Action) error {
	s.calls = append(s.calls, action)
	return s.returnErr
}

// newTestRouter creates a Router with only the fields needed for routing
// tests — no Discord API, no DB, no real executors. The logger is a nop so
// output doesn't pollute test runs.
func newTestRouter(executors map[string]Executor) *Router {
	return &Router{
		executors: executors,
		logger:    zap.NewNop(),
	}
}

// ── MultiError ─────────────────────────────────────────────────────────────

func TestMultiError_HasFailures_NoFailed(t *testing.T) {
	me := &MultiError{Succeeded: []string{"ban"}}
	if me.HasFailures() {
		t.Fatal("expected HasFailures()=false when Failed is empty")
	}
}

func TestMultiError_HasFailures_WithFailed(t *testing.T) {
	me := &MultiError{
		Failed: []failedAction{{Intent: "ban", Err: errors.New("denied")}},
	}
	if !me.HasFailures() {
		t.Fatal("expected HasFailures()=true when Failed is non-empty")
	}
}

func TestMultiError_Error_Empty(t *testing.T) {
	me := &MultiError{}
	if got := me.Error(); got != "no actions executed" {
		t.Fatalf("unexpected: %q", got)
	}
}

func TestMultiError_Error_AllSucceeded(t *testing.T) {
	me := &MultiError{Succeeded: []string{"ban", "kick"}}
	got := me.Error()
	if !strings.Contains(got, "Executed") {
		t.Fatalf("expected 'Executed' prefix, got %q", got)
	}
	if !strings.Contains(got, "ban") || !strings.Contains(got, "kick") {
		t.Fatalf("expected both intents in output, got %q", got)
	}
}

func TestMultiError_Error_AllFailed(t *testing.T) {
	me := &MultiError{
		Failed: []failedAction{
			{Intent: "ban", Err: errors.New("permission denied")},
			{Intent: "kick", Err: errors.New("not a member")},
		},
	}
	got := me.Error()
	if !strings.Contains(got, "Failed") {
		t.Fatalf("expected 'Failed' prefix, got %q", got)
	}
	if !strings.Contains(got, "ban") || !strings.Contains(got, "kick") {
		t.Fatalf("expected both intents in output, got %q", got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("expected error text in output, got %q", got)
	}
}

func TestMultiError_Error_Mixed(t *testing.T) {
	me := &MultiError{
		Succeeded: []string{"kick"},
		Failed:    []failedAction{{Intent: "ban", Err: errors.New("hierarchy")}},
	}
	got := me.Error()
	if !strings.Contains(got, "Executed") {
		t.Fatalf("expected 'Executed' in output, got %q", got)
	}
	if !strings.Contains(got, "kick") {
		t.Fatalf("expected 'kick' in output, got %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Fatalf("expected 'failed' in output, got %q", got)
	}
	if !strings.Contains(got, "ban") {
		t.Fatalf("expected 'ban' in output, got %q", got)
	}
}

// ── Route ──────────────────────────────────────────────────────────────────

func TestRoute_EmptyIntent_ReturnsNil(t *testing.T) {
	r := newTestRouter(nil)
	err := r.Route(context.Background(), Action{Intent: ""})
	if err != nil {
		t.Fatalf("expected nil for empty intent, got %v", err)
	}
}

func TestRoute_UnknownIntent_ReturnsError(t *testing.T) {
	r := newTestRouter(nil)
	err := r.Route(context.Background(), Action{Intent: "obliterate"})
	if err == nil {
		t.Fatal("expected error for unknown intent")
	}
	if !strings.Contains(err.Error(), "obliterate") {
		t.Fatalf("expected intent name in error, got: %v", err)
	}
}

func TestRoute_KnownIntent_CallsExecutor(t *testing.T) {
	stub := &stubExecutor{}
	r := newTestRouter(map[string]Executor{"ban": stub})

	action := Action{
		Intent:  "ban",
		GuildID: "guild-1",
		ActorID: "actor-1",
	}
	if err := r.Route(context.Background(), action); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 call to executor, got %d", len(stub.calls))
	}
	if stub.calls[0].Intent != "ban" {
		t.Errorf("expected intent 'ban', got %q", stub.calls[0].Intent)
	}
}

func TestRoute_KnownIntent_ExecutorErrorPropagated(t *testing.T) {
	sentinel := errors.New("permission denied")
	stub := &stubExecutor{returnErr: sentinel}
	r := newTestRouter(map[string]Executor{"ban": stub})

	err := r.Route(context.Background(), Action{Intent: "ban"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}

// ── ExecuteResponse ────────────────────────────────────────────────────────

func TestExecuteResponse_NonModeration_IsNoOp(t *testing.T) {
	stub := &stubExecutor{}
	r := newTestRouter(map[string]Executor{"ban": stub})

	resp := &llm.LLMResponse{
		Intent:       "ban",
		IsModeration: false,
	}
	err := r.ExecuteResponse(context.Background(), resp, ActionMeta{})
	if err != nil {
		t.Fatalf("expected nil for non-moderation response, got %v", err)
	}
	if len(stub.calls) != 0 {
		t.Fatal("expected no executor calls for non-moderation response")
	}
}

func TestExecuteResponse_SingleAction_RoutesCorrectly(t *testing.T) {
	stub := &stubExecutor{}
	r := newTestRouter(map[string]Executor{"kick": stub})

	resp := &llm.LLMResponse{
		Intent:       "kick",
		IsModeration: true,
		Targets:      []llm.Target{{Type: "user", ID: "111111111111111111"}},
	}
	meta := ActionMeta{GuildID: "g1", ActorID: "a1"}
	if err := r.ExecuteResponse(context.Background(), resp, meta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 executor call, got %d", len(stub.calls))
	}
	if stub.calls[0].GuildID != "g1" {
		t.Errorf("expected GuildID 'g1', got %q", stub.calls[0].GuildID)
	}
}

func TestExecuteResponse_MultiAction_AllSucceed_ReturnsNil(t *testing.T) {
	banStub := &stubExecutor{}
	kickStub := &stubExecutor{}
	r := newTestRouter(map[string]Executor{
		"ban":  banStub,
		"kick": kickStub,
	})

	resp := &llm.LLMResponse{
		Intent:       "ban",
		IsModeration: true,
		Actions: []llm.Action{
			{Intent: "ban", Targets: []llm.Target{{Type: "user", ID: "111111111111111111"}}},
			{Intent: "kick", Targets: []llm.Target{{Type: "user", ID: "222222222222222222"}}},
		},
	}
	if err := r.ExecuteResponse(context.Background(), resp, ActionMeta{}); err != nil {
		t.Fatalf("expected nil when all actions succeed, got: %v", err)
	}
	if len(banStub.calls) != 1 {
		t.Errorf("expected 1 ban call, got %d", len(banStub.calls))
	}
	if len(kickStub.calls) != 1 {
		t.Errorf("expected 1 kick call, got %d", len(kickStub.calls))
	}
}

func TestExecuteResponse_MultiAction_PartialFailure_ReturnsMultiError(t *testing.T) {
	sentinel := errors.New("hierarchy violation")
	banStub := &stubExecutor{returnErr: sentinel}
	kickStub := &stubExecutor{}
	r := newTestRouter(map[string]Executor{
		"ban":  banStub,
		"kick": kickStub,
	})

	resp := &llm.LLMResponse{
		Intent:       "ban",
		IsModeration: true,
		Actions: []llm.Action{
			{Intent: "ban", Targets: []llm.Target{{Type: "user", ID: "111111111111111111"}}},
			{Intent: "kick", Targets: []llm.Target{{Type: "user", ID: "222222222222222222"}}},
		},
	}
	err := r.ExecuteResponse(context.Background(), resp, ActionMeta{})
	if err == nil {
		t.Fatal("expected error for partial failure")
	}
	var me *MultiError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MultiError, got %T: %v", err, err)
	}
	if !me.HasFailures() {
		t.Fatal("expected HasFailures()=true")
	}
	if len(me.Succeeded) != 1 {
		t.Errorf("expected 1 succeeded, got %d", len(me.Succeeded))
	}
	if len(me.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(me.Failed))
	}
}

func TestExecuteResponse_MultiAction_AllFail_ReturnsMultiError(t *testing.T) {
	sentinel := errors.New("denied")
	r := newTestRouter(map[string]Executor{
		"ban":  &stubExecutor{returnErr: sentinel},
		"kick": &stubExecutor{returnErr: sentinel},
	})

	resp := &llm.LLMResponse{
		Intent:       "ban",
		IsModeration: true,
		Actions: []llm.Action{
			{Intent: "ban"},
			{Intent: "kick"},
		},
	}
	err := r.ExecuteResponse(context.Background(), resp, ActionMeta{})
	var me *MultiError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MultiError, got %T", err)
	}
	if len(me.Succeeded) != 0 {
		t.Errorf("expected 0 succeeded, got %d", len(me.Succeeded))
	}
	if len(me.Failed) != 2 {
		t.Errorf("expected 2 failed, got %d", len(me.Failed))
	}
}

// ── Snipe nil-guard methods ────────────────────────────────────────────────
// The snipeExecutor field is assigned in registerExecutors(). These tests
// verify that every Snipe* method degrades gracefully when the field is nil
// (i.e. when WithSnipeExecutor was never called).

func TestSnipePagination_NilExecutor_ReturnsNil(t *testing.T) {
	r := &Router{logger: zap.NewNop()} // snipeExecutor intentionally nil
	snap, text, components := r.SnipePagination(context.Background(), "msg", "next")
	if snap != nil {
		t.Errorf("expected nil snapshot, got %+v", snap)
	}
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}
	if components != nil {
		t.Errorf("expected nil components, got %+v", components)
	}
}

func TestSnipeSourceMsgID_NilExecutor_ReturnsEmpty(t *testing.T) {
	r := &Router{logger: zap.NewNop()}
	if got := r.SnipeSourceMsgID("msg-id"); got != "" {
		t.Fatalf("expected '' for nil snipeExecutor, got %q", got)
	}
}

func TestSnipeDeletePage_NilExecutor_NoPanic(t *testing.T) {
	r := &Router{logger: zap.NewNop()}
	// Must not panic.
	r.SnipeDeletePage("msg-id")
}

// ── actionFromMeta ─────────────────────────────────────────────────────────

func TestActionFromMeta_CopiesAllFields(t *testing.T) {
	meta := ActionMeta{
		GuildID:            "g1",
		ChannelID:          "c1",
		ActorID:            "a1",
		SourceMsgID:        "s1",
		BotMessageID:       "b1",
		UserReplyMessageID: "u1",
		Sudo:               true,
	}
	action := actionFromMeta(meta)
	if action.GuildID != meta.GuildID ||
		action.ChannelID != meta.ChannelID ||
		action.ActorID != meta.ActorID ||
		action.SourceMsgID != meta.SourceMsgID ||
		action.BotMessageID != meta.BotMessageID ||
		action.UserReplyMessageID != meta.UserReplyMessageID ||
		action.Sudo != meta.Sudo {
		t.Fatalf("actionFromMeta did not copy all fields correctly: %+v", action)
	}
}
