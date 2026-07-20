package hooks

import (
	"context"
	"testing"
	"time"
)

func TestNewRunnerNil(t *testing.T) {
	r := NewRunner(nil)
	if r != nil {
		t.Fatal("expected nil Runner for empty config")
	}
	// nil Runner methods should be safe
	if r.HasHooks(PreExecute) {
		t.Fatal("nil Runner should not have hooks")
	}
	results, err := r.Run(context.Background(), HookEvent{Trigger: PreExecute})
	if err != nil {
		t.Fatalf("nil Runner.Run should not error: %v", err)
	}
	if results != nil {
		t.Fatal("nil Runner.Run should return nil results")
	}
}

func TestNewRunnerEmpty(t *testing.T) {
	r := NewRunner(HooksConfig{})
	if r != nil {
		t.Fatal("expected nil Runner for empty HooksConfig")
	}
}

func TestHasHooks(t *testing.T) {
	cfg := HooksConfig{
		"pre_execute": {
			{Name: "test", Command: "echo hello"},
		},
	}
	r := NewRunner(cfg)
	if !r.HasHooks(PreExecute) {
		t.Fatal("expected HasHooks=true for pre_execute")
	}
	if r.HasHooks(PostExecute) {
		t.Fatal("expected HasHooks=false for post_execute")
	}
}

func TestRunSuccess(t *testing.T) {
	cfg := HooksConfig{
		"pre_execute": {
			{Name: "echo-test", Command: "echo hello"},
		},
	}
	r := NewRunner(cfg)

	results, err := r.Run(context.Background(), HookEvent{
		Trigger: PreExecute,
		Input:   "test task",
		Agent:   "kiro",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", results[0].ExitCode)
	}
	if results[0].Stdout != "hello" {
		t.Fatalf("expected stdout 'hello', got %q", results[0].Stdout)
	}
	if results[0].Blocked {
		t.Fatal("should not be blocked")
	}
}

func TestRunStdinJSON(t *testing.T) {
	// Hook reads STDIN and echoes the trigger field
	cfg := HooksConfig{
		"pre_route": {
			{Name: "read-stdin", Command: `cat | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['trigger'])"`},
		},
	}
	r := NewRunner(cfg)

	results, err := r.Run(context.Background(), HookEvent{
		Trigger: PreRoute,
		Input:   "hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Stdout != "pre_route" {
		t.Fatalf("expected stdout 'pre_route', got %q", results[0].Stdout)
	}
}

func TestRunBlockOnFailure(t *testing.T) {
	cfg := HooksConfig{
		"pre_execute": {
			{Name: "blocker", Command: "echo 'reason: dangerous' >&2; exit 2", BlockOnFailure: true},
			{Name: "should-not-run", Command: "echo 'unreachable'"},
		},
	}
	r := NewRunner(cfg)

	results, err := r.Run(context.Background(), HookEvent{
		Trigger: PreExecute,
		Agent:   "shell",
		Command: "rm -rf /",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (second hook skipped), got %d", len(results))
	}
	if !results[0].Blocked {
		t.Fatal("expected blocked=true")
	}
	if results[0].ExitCode != 2 {
		t.Fatalf("expected exit 2, got %d", results[0].ExitCode)
	}
	if results[0].Stderr != "reason: dangerous" {
		t.Fatalf("expected stderr reason, got %q", results[0].Stderr)
	}
	if !Blocked(results) {
		t.Fatal("Blocked() should return true")
	}
	reason := BlockReason(results)
	if reason != "reason: dangerous" {
		t.Fatalf("expected block reason, got %q", reason)
	}
}

func TestRunExit2WithoutBlockOnFailure(t *testing.T) {
	// exit 2 without BlockOnFailure should NOT block
	cfg := HooksConfig{
		"pre_execute": {
			{Name: "warn-only", Command: "exit 2", BlockOnFailure: false},
			{Name: "still-runs", Command: "echo ok"},
		},
	}
	r := NewRunner(cfg)

	results, err := r.Run(context.Background(), HookEvent{Trigger: PreExecute})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Blocked {
		t.Fatal("should not be blocked without BlockOnFailure")
	}
	if results[1].Stdout != "ok" {
		t.Fatalf("second hook should have run, got stdout %q", results[1].Stdout)
	}
}

func TestRunTimeout(t *testing.T) {
	cfg := HooksConfig{
		"post_execute": {
			{Name: "slow", Command: "sleep 10", Timeout: 1},
		},
	}
	r := NewRunner(cfg)

	start := time.Now()
	results, err := r.Run(context.Background(), HookEvent{Trigger: PostExecute})
	took := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err == nil {
		t.Fatal("expected timeout error")
	}
	if results[0].ExitCode != -1 {
		t.Fatalf("expected exit -1 for timeout, got %d", results[0].ExitCode)
	}
	// Should not wait full 10 seconds
	if took > 3*time.Second {
		t.Fatalf("timeout took too long: %v", took)
	}
}

func TestRunMultipleHooks(t *testing.T) {
	cfg := HooksConfig{
		"post_execute": {
			{Name: "first", Command: "echo one"},
			{Name: "second", Command: "echo two"},
			{Name: "third", Command: "echo three"},
		},
	}
	r := NewRunner(cfg)

	results, err := r.Run(context.Background(), HookEvent{Trigger: PostExecute})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	expected := []string{"one", "two", "three"}
	for i, exp := range expected {
		if results[i].Stdout != exp {
			t.Fatalf("result[%d] stdout=%q, want %q", i, results[i].Stdout, exp)
		}
	}
}

func TestRunNonExistentCommand(t *testing.T) {
	cfg := HooksConfig{
		"pre_route": {
			{Name: "bad-cmd", Command: "/nonexistent/binary/xyz123"},
		},
	}
	r := NewRunner(cfg)

	results, err := r.Run(context.Background(), HookEvent{Trigger: PreRoute})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// bash -c with nonexistent binary: exit 127
	if results[0].ExitCode == 0 {
		t.Fatal("expected non-zero exit for bad command")
	}
}

func TestCombinedStdout(t *testing.T) {
	results := []HookResult{
		{ExitCode: 0, Stdout: "line1"},
		{ExitCode: 1, Stdout: "ignored"},
		{ExitCode: 0, Stdout: ""},
		{ExitCode: 0, Stdout: "line2"},
	}
	got := CombinedStdout(results)
	if got != "line1\nline2" {
		t.Fatalf("expected 'line1\\nline2', got %q", got)
	}
}

func TestDefaultTimeout(t *testing.T) {
	cfg := HooksConfig{
		"on_session_start": {
			{Name: "no-timeout-set", Command: "echo ok"},
		},
	}
	r := NewRunner(cfg)
	// Check that default timeout was applied
	defs := r.hooks[OnSessionStart]
	if len(defs) != 1 {
		t.Fatal("expected 1 hook def")
	}
	if defs[0].Timeout != 10 {
		t.Fatalf("expected default timeout=10, got %d", defs[0].Timeout)
	}
}

func TestContextCancellation(t *testing.T) {
	cfg := HooksConfig{
		"pre_execute": {
			{Name: "long", Command: "sleep 10", Timeout: 30},
		},
	}
	r := NewRunner(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 500ms
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	results, err := r.Run(ctx, HookEvent{Trigger: PreExecute})
	took := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Should finish quickly due to context cancellation
	if took > 3*time.Second {
		t.Fatalf("context cancellation took too long: %v", took)
	}
}

func TestAllTriggers(t *testing.T) {
	triggers := AllTriggers()
	if len(triggers) != 5 {
		t.Fatalf("expected 5 triggers, got %d", len(triggers))
	}
	expected := map[Trigger]bool{
		PreRoute: true, PreExecute: true, PostExecute: true,
		OnSessionStart: true, OnSessionEnd: true,
	}
	for _, tr := range triggers {
		if !expected[tr] {
			t.Fatalf("unexpected trigger: %s", tr)
		}
	}
}
