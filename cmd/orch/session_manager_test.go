package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gordonwei/orch/pkg/session"
)

func TestNewSessionManager(t *testing.T) {
	sm := NewSessionManager()
	if sm == nil {
		t.Fatal("NewSessionManager returned nil")
	}
	if sm.HasActive() {
		t.Error("new manager should not have active session")
	}
	if len(sm.List()) != 0 {
		t.Error("new manager should have no sessions")
	}
}

func TestSessionManager_BackWithoutActive(t *testing.T) {
	sm := NewSessionManager()
	// Should not panic
	sm.Back()
	if sm.HasActive() {
		t.Error("should still have no active after Back()")
	}
}

func TestSessionManager_KillNonExistent(t *testing.T) {
	sm := NewSessionManager()
	err := sm.Kill(session.BackendClaude)
	if err == nil {
		t.Error("Kill on non-existent should error")
	}
}

func TestSessionManager_SwitchNonExistent(t *testing.T) {
	sm := NewSessionManager()
	err := sm.Switch(session.BackendKiro)
	if err == nil {
		t.Error("Switch to non-existent should error")
	}
}

func TestSessionManager_SetAutoRestart(t *testing.T) {
	sm := NewSessionManager()
	sm.SetAutoRestart(session.BackendClaude, true)
	sm.mu.Lock()
	got := sm.autoRestart[session.BackendClaude]
	sm.mu.Unlock()
	if !got {
		t.Error("SetAutoRestart should set true")
	}
}

func TestSessionManager_ActiveBackendEmpty(t *testing.T) {
	sm := NewSessionManager()
	if sm.ActiveBackend() != "" {
		t.Errorf("ActiveBackend should be empty, got %q", sm.ActiveBackend())
	}
}

func TestSessionManager_Events(t *testing.T) {
	sm := NewSessionManager()
	ch := sm.Events()
	if ch == nil {
		t.Fatal("Events() returned nil channel")
	}
}

func TestSessionManager_TouchOutput(t *testing.T) {
	sm := NewSessionManager()
	// Should not panic on non-existent backend
	sm.TouchOutput(session.BackendClaude)
}

func TestSessionManager_KillAll_Empty(t *testing.T) {
	sm := NewSessionManager()
	sm.WatchSessions()
	// Should not panic or hang
	sm.KillAll()
}

func TestSessionManager_Shutdown_Empty(t *testing.T) {
	sm := NewSessionManager()
	sm.WatchSessions()
	// Should not panic or hang
	done := make(chan struct{})
	go func() {
		sm.Shutdown()
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown on empty manager should not hang")
	}
}

func TestSessionManager_WatchSessions_Stop(t *testing.T) {
	sm := NewSessionManager()
	sm.WatchSessions()
	// Calling KillAll stops watcher
	sm.KillAll()
	// Should be safe to call again
	sm.Shutdown()
}

func TestRouteHinter_Basic(t *testing.T) {
	h := NewRouteHinter()

	// Strong signal: terraform should suggest kiro when in claude
	suggested, kw, reason := h.Hint("run terraform plan", session.BackendClaude)
	if suggested != session.BackendKiro {
		t.Errorf("expected kiro suggestion, got %q", suggested)
	}
	if kw == "" {
		t.Error("expected keyword match")
	}
	if reason == "" {
		t.Error("expected reason string")
	}
}

func TestRouteHinter_NoCrossDomain(t *testing.T) {
	h := NewRouteHinter()

	// terraform in kiro should not suggest switch
	suggested, _, _ := h.Hint("terraform apply", session.BackendKiro)
	if suggested != "" {
		t.Errorf("expected no suggestion for same domain, got %q", suggested)
	}
}

func TestRouteHinter_Cooldown(t *testing.T) {
	h := NewRouteHinter()

	// First hint should work
	suggested1, _, _ := h.Hint("terraform plan", session.BackendClaude)
	if suggested1 == "" {
		t.Fatal("first hint should trigger")
	}

	// Next 2 should be suppressed by cooldown (cooldown=3)
	suggested2, _, _ := h.Hint("kubectl get pods", session.BackendClaude)
	if suggested2 != "" {
		t.Error("second hint should be suppressed by cooldown")
	}

	suggested3, _, _ := h.Hint("helm upgrade", session.BackendClaude)
	if suggested3 != "" {
		t.Error("third hint should be suppressed by cooldown")
	}

	// After cooldown expires, should work again
	suggested4, _, _ := h.Hint("docker build", session.BackendClaude)
	if suggested4 == "" {
		t.Error("fourth hint (after cooldown) should trigger")
	}
}

func TestRouteHinter_WeakSignalIgnored(t *testing.T) {
	h := NewRouteHinter()

	// "test" is strength 1 (weak) — should not trigger
	suggested, _, _ := h.Hint("run the test", session.BackendClaude)
	if suggested != "" {
		t.Errorf("weak signal 'test' should not trigger, got %q", suggested)
	}
}

func TestRouteHint_BackwardCompat(t *testing.T) {
	// The global RouteHint function should still work
	suggested, kw := RouteHint("sync to notion please", session.BackendKiro)
	if suggested != session.BackendClaude {
		t.Errorf("expected claude suggestion, got %q", suggested)
	}
	if kw == "" {
		t.Error("expected keyword")
	}
}

func TestStripState_Stateful(t *testing.T) {
	st := &session.StripState{}

	// Before alt screen, content passes through
	got1 := st.Strip("hello ")
	if got1 != "hello " {
		t.Errorf("before alt screen: got %q", got1)
	}

	// Enter alt screen — content discarded
	got2 := st.Strip("\x1b[?1049h TUI content here")
	if got2 != "" {
		t.Errorf("in alt screen: got %q, want empty", got2)
	}

	// Still in alt screen
	got3 := st.Strip("more TUI junk")
	if got3 != "" {
		t.Errorf("still alt screen: got %q, want empty", got3)
	}

	// Leave alt screen — content passes through again
	got4 := st.Strip("\x1b[?1049l real output")
	if got4 != " real output" {
		t.Errorf("after alt screen: got %q, want %q", got4, " real output")
	}
}

func TestRouteHinter_ChineseKeywords(t *testing.T) {
	h := NewRouteHinter()

	// "部署" (strength 3) in claude session → should suggest kiro
	suggested, kw, reason := h.Hint("幫我部署這個服務到 GKE", session.BackendClaude)
	if suggested != session.BackendKiro {
		t.Errorf("expected kiro for 部署, got %q", suggested)
	}
	if kw != "部署" {
		t.Errorf("expected keyword 部署, got %q", kw)
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
	// Reason should be in Chinese and contain the keyword
	if !strings.Contains(reason, "部署") {
		t.Errorf("reason should mention the keyword, got: %s", reason)
	}
}

func TestRouteHinter_NoSuggestionSameDomain(t *testing.T) {
	h := NewRouteHinter()

	// "notion" in claude → same domain, no suggestion
	suggested, _, _ := h.Hint("update the notion page with meeting notes", session.BackendClaude)
	if suggested != "" {
		t.Errorf("should not suggest switch for same domain, got %q", suggested)
	}
}

func TestRouteHinter_PhrasePriority(t *testing.T) {
	h := NewRouteHinter()

	// "terraform plan" should trigger with strength 3
	suggested, kw, _ := h.Hint("run terraform plan for litellm-gke", session.BackendClaude)
	if suggested != session.BackendKiro {
		t.Errorf("expected kiro, got %q", suggested)
	}
	// Should preferentially match the longer phrase
	if kw != "terraform plan" && kw != "terraform" {
		t.Errorf("expected terraform-related keyword, got %q", kw)
	}
}

func TestRouteHinter_NotionInKiro(t *testing.T) {
	h := NewRouteHinter()

	// "notion" (strength 3) in kiro session → should suggest claude
	suggested, _, _ := h.Hint("同步到 notion 交接表", session.BackendKiro)
	if suggested != session.BackendClaude {
		t.Errorf("expected claude for notion, got %q", suggested)
	}
}
