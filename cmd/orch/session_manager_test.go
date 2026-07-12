package main

import (
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
