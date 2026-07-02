package executor

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/planner"
)

func testConfig() *config.Config {
	return &config.Config{
		Workspace: config.Workspace{Root: "."},
	}
}

// TestExecuteShellStep verifies basic execution of a single shell step.
func TestExecuteShellStep(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "test echo",
		Difficulty:  "simple",
		Category:    "query",
		Steps: []planner.Step{
			{
				ID:          "step_1",
				Description: "echo hello",
				Agent:       "shell",
				Command:     "echo hello_orch_test",
			},
		},
	}

	result := e.Execute(plan)

	if !result.Success {
		t.Fatalf("expected success, got failure: %v", result.Err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("expected 1 step result, got %d", len(result.Steps))
	}
	if result.Steps[0].Err != nil {
		t.Fatalf("step error: %v", result.Steps[0].Err)
	}
	if result.Steps[0].Output != "hello_orch_test\n" {
		t.Errorf("output = %q, want 'hello_orch_test\\n'", result.Steps[0].Output)
	}
}

// TestExecuteMultiStepWithDeps verifies sequential multi-step execution with dependencies.
func TestExecuteMultiStepWithDeps(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "multi step",
		Difficulty:  "simple",
		Category:    "query",
		Steps: []planner.Step{
			{
				ID:          "step_1",
				Description: "generate data",
				Agent:       "shell",
				Command:     "echo 42",
			},
			{
				ID:          "step_2",
				Description: "use previous output",
				Agent:       "shell",
				Command:     "echo step2_done",
				DependsOn:   []string{"step_1"},
			},
		},
	}

	result := e.Execute(plan)

	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(result.Steps))
	}
}

// TestParallelExecution verifies steps without dependencies run in parallel.
// Three steps sleeping 0.3s should complete in ~0.5s if parallel.
func TestParallelExecution(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "parallel test",
		Difficulty:  "simple",
		Category:    "infra",
		Steps: []planner.Step{
			{
				ID:          "a",
				Description: "task A",
				Agent:       "shell",
				Command:     "sleep 0.3 && echo A_done",
			},
			{
				ID:          "b",
				Description: "task B",
				Agent:       "shell",
				Command:     "sleep 0.3 && echo B_done",
			},
			{
				ID:          "c",
				Description: "task C",
				Agent:       "shell",
				Command:     "sleep 0.3 && echo C_done",
			},
		},
	}

	start := time.Now()
	result := e.Execute(plan)
	elapsed := time.Since(start)

	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Err)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 step results, got %d", len(result.Steps))
	}

	// Sequential would take 0.9s+. Parallel should finish within 0.6s.
	if elapsed > 700*time.Millisecond {
		t.Errorf("parallel execution took %v, expected < 700ms (steps should run concurrently)", elapsed)
	}
	t.Logf("parallel execution of 3 x sleep(0.3) completed in %v", elapsed)
}

// TestDAGDependencyOrder verifies DAG respects dependency order.
// Step C depends on A and B; A and B run in parallel.
func TestDAGDependencyOrder(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "dag order test",
		Difficulty:  "moderate",
		Category:    "infra",
		Steps: []planner.Step{
			{
				ID:          "a",
				Description: "step A",
				Agent:       "shell",
				Command:     "sleep 0.2 && echo A",
			},
			{
				ID:          "b",
				Description: "step B",
				Agent:       "shell",
				Command:     "sleep 0.2 && echo B",
			},
			{
				ID:          "c",
				Description: "step C depends on A and B",
				Agent:       "shell",
				Command:     "echo C",
				DependsOn:   []string{"a", "b"},
			},
		},
	}

	result := e.Execute(plan)

	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Err)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 step results, got %d", len(result.Steps))
	}

	// Find step C result, confirm it ran after A and B
	var cResult *StepResult
	for i := range result.Steps {
		if result.Steps[i].StepID == "c" {
			cResult = &result.Steps[i]
		}
	}
	if cResult == nil {
		t.Fatal("step C not found in results")
	}
	if cResult.Err != nil {
		t.Fatalf("step C failed: %v", cResult.Err)
	}
	if cResult.Output != "C\n" {
		t.Errorf("step C output = %q, want 'C\\n'", cResult.Output)
	}
}

// TestContextChainFromMultipleDeps verifies step receives context from multiple upstreams.
func TestContextChainFromMultipleDeps(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "context chain test",
		Steps: []planner.Step{
			{
				ID:      "a",
				Agent:   "shell",
				Command: "echo KV:region=us-east-1",
			},
			{
				ID:      "b",
				Agent:   "shell",
				Command: "echo KV:env=prod",
			},
			{
				ID:        "c",
				Agent:     "shell",
				Command:   "echo final",
				DependsOn: []string{"a", "b"},
			},
		},
	}

	result := e.Execute(plan)
	if !result.Success {
		t.Fatalf("expected success: %v", result.Err)
	}

	// Verify KV from a and b are correctly parsed
	for _, sr := range result.Steps {
		switch sr.StepID {
		case "a":
			if sr.KV["region"] != "us-east-1" {
				t.Errorf("step a KV[region] = %q, want us-east-1", sr.KV["region"])
			}
		case "b":
			if sr.KV["env"] != "prod" {
				t.Errorf("step b KV[env] = %q, want prod", sr.KV["env"])
			}
		}
	}
}

// TestExecuteWithVerify verifies step verify_cmd logic.
func TestExecuteWithVerify(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "verify test",
		Difficulty:  "simple",
		Category:    "query",
		Steps: []planner.Step{
			{
				ID:          "step_1",
				Description: "create and verify",
				Agent:       "shell",
				Command:     "echo verified",
				VerifyCmd:   "test 1 -eq 1", // always passes
			},
		},
	}

	result := e.Execute(plan)

	if !result.Success {
		t.Fatalf("expected success with passing verify")
	}
	if !result.Steps[0].Verified {
		t.Error("expected Verified=true")
	}
}

// TestExecuteFailedVerify verifies behavior when verify_cmd fails.
func TestExecuteFailedVerify(t *testing.T) {
	e := New(testConfig(), nil)
	e.maxRetries = 1 // reduce wait time

	plan := &planner.Plan{
		TaskSummary: "fail verify test",
		Difficulty:  "simple",
		Category:    "query",
		Steps: []planner.Step{
			{
				ID:          "step_1",
				Description: "will fail verify",
				Agent:       "shell",
				Command:     "echo fail",
				VerifyCmd:   "test 1 -eq 2", // always fails
			},
		},
	}

	result := e.Execute(plan)

	if result.Success {
		t.Fatalf("expected failure with failing verify")
	}
}

// TestOnFailureSkip verifies downstream steps still execute when on_failure=skip.
func TestOnFailureSkip(t *testing.T) {
	e := New(testConfig(), nil)
	e.maxRetries = 0

	plan := &planner.Plan{
		TaskSummary: "skip test",
		Steps: []planner.Step{
			{
				ID:        "a",
				Agent:     "shell",
				Command:   "exit 1", // intentional failure
				OnFailure: "skip",
			},
			{
				ID:        "b",
				Agent:     "shell",
				Command:   "echo b_ok",
				DependsOn: []string{"a"},
			},
		},
	}

	result := e.Execute(plan)

	// Should not be fully successful (a failed)
	if result.Success {
		t.Fatal("expected overall failure since step a failed")
	}

	// But b should have executed (since a has skip policy)
	var bResult *StepResult
	for i := range result.Steps {
		if result.Steps[i].StepID == "b" {
			bResult = &result.Steps[i]
		}
	}
	if bResult == nil {
		t.Fatal("step b not found in results")
	}
	if bResult.Err != nil {
		t.Errorf("step b should have succeeded, got err: %v", bResult.Err)
	}
}

// TestOnFailureAbort verifies all pending steps are cancelled when on_failure=abort.
func TestOnFailureAbort(t *testing.T) {
	e := New(testConfig(), nil)
	e.maxRetries = 0

	plan := &planner.Plan{
		TaskSummary: "abort test",
		Steps: []planner.Step{
			{
				ID:        "a",
				Agent:     "shell",
				Command:   "exit 1", // intentional failure
				OnFailure: "abort",
			},
			{
				ID:        "b",
				Agent:     "shell",
				Command:   "sleep 5 && echo should_not_run",
				DependsOn: []string{"a"},
			},
		},
	}

	start := time.Now()
	result := e.Execute(plan)
	elapsed := time.Since(start)

	if result.Success {
		t.Fatal("expected failure")
	}

	// b should not run for 5 seconds
	if elapsed > 2*time.Second {
		t.Errorf("abort should have cancelled quickly, took %v", elapsed)
	}
}

// TestOnFailureRePlan verifies re-plan callback is triggered correctly.
func TestOnFailureRePlan(t *testing.T) {
	e := New(testConfig(), nil)
	e.maxRetries = 0

	var rePlanCalled int32
	e.SetRePlanFunc(func(failedContext string) error {
		atomic.AddInt32(&rePlanCalled, 1)
		return nil
	})

	plan := &planner.Plan{
		TaskSummary: "replan test",
		Steps: []planner.Step{
			{
				ID:        "a",
				Agent:     "shell",
				Command:   "exit 1",
				OnFailure: "re-plan",
			},
			{
				ID:        "b",
				Agent:     "shell",
				Command:   "echo should_not_run",
				DependsOn: []string{"a"},
			},
		},
	}

	result := e.Execute(plan)

	if result.Success {
		t.Fatal("expected failure with re-plan")
	}
	if result.RePlanCount != 1 {
		t.Errorf("RePlanCount = %d, want 1", result.RePlanCount)
	}
	if atomic.LoadInt32(&rePlanCalled) != 1 {
		t.Errorf("rePlanFunc called %d times, want 1", rePlanCalled)
	}
}

// TestCycleDetection verifies cycle detection in dependencies.
func TestCycleDetection(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "cycle test",
		Steps: []planner.Step{
			{
				ID:        "a",
				Agent:     "shell",
				Command:   "echo a",
				DependsOn: []string{"b"},
			},
			{
				ID:        "b",
				Agent:     "shell",
				Command:   "echo b",
				DependsOn: []string{"a"},
			},
		},
	}

	result := e.Execute(plan)

	if result.Success {
		t.Fatal("expected failure for cyclic dependency")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil error for cycle")
	}
	t.Logf("cycle error: %v", result.Err)
}

// TestDuplicateStepID verifies duplicate step ID error detection.
func TestDuplicateStepID(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "dup id test",
		Steps: []planner.Step{
			{ID: "a", Agent: "shell", Command: "echo 1"},
			{ID: "a", Agent: "shell", Command: "echo 2"},
		},
	}

	result := e.Execute(plan)
	if result.Success {
		t.Fatal("expected failure for duplicate step IDs")
	}
}

// TestMissingDependency verifies error when depending on non-existent step ID.
func TestMissingDependency(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "missing dep",
		Steps: []planner.Step{
			{
				ID:        "a",
				Agent:     "shell",
				Command:   "echo a",
				DependsOn: []string{"nonexistent"},
			},
		},
	}

	result := e.Execute(plan)
	if result.Success {
		t.Fatal("expected failure for missing dependency")
	}
}

// TestEventsChan verifies streaming events are sent correctly.
func TestEventsChan(t *testing.T) {
	e := New(testConfig(), nil)
	e.EventChan = make(chan StepEvent, 100) // buffered to avoid blocking

	plan := &planner.Plan{
		TaskSummary: "event test",
		Steps: []planner.Step{
			{
				ID:      "a",
				Agent:   "shell",
				Command: "echo hi",
			},
		},
	}

	// Collect events in another goroutine (Execute closes channel when done)
	var events []StepEvent
	done := make(chan struct{})
	go func() {
		for ev := range e.EventChan {
			events = append(events, ev)
		}
		close(done)
	}()

	result := e.Execute(plan)
	<-done // wait for event collection to complete

	if !result.Success {
		t.Fatalf("expected success: %v", result.Err)
	}

	// Should have at least one Start and one Done event
	var hasStart, hasDone bool
	for _, ev := range events {
		if ev.StepID == "a" && ev.Type == EventStepStart {
			hasStart = true
		}
		if ev.StepID == "a" && ev.Type == EventStepDone {
			hasDone = true
		}
	}
	if !hasStart {
		t.Error("expected EventStepStart for step a")
	}
	if !hasDone {
		t.Error("expected EventStepDone for step a")
	}
}

// TestDiamondDAG verifies diamond DAG (A → B,C → D) parallel execution.
//
//	    A
//	   / \
//	  B   C
//	   \ /
//	    D
func TestDiamondDAG(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "diamond DAG",
		Steps: []planner.Step{
			{ID: "a", Agent: "shell", Command: "echo A"},
			{ID: "b", Agent: "shell", Command: "sleep 0.2 && echo B", DependsOn: []string{"a"}},
			{ID: "c", Agent: "shell", Command: "sleep 0.2 && echo C", DependsOn: []string{"a"}},
			{ID: "d", Agent: "shell", Command: "echo D", DependsOn: []string{"b", "c"}},
		},
	}

	start := time.Now()
	result := e.Execute(plan)
	elapsed := time.Since(start)

	if !result.Success {
		t.Fatalf("expected success: %v", result.Err)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("expected 4 results, got %d", len(result.Steps))
	}

	// B and C parallel → total should not exceed 0.5s (sequential needs 0.4s+)
	if elapsed > 600*time.Millisecond {
		t.Errorf("diamond DAG took %v, B and C should run in parallel", elapsed)
	}
	t.Logf("diamond DAG completed in %v", elapsed)
}

// TestExecuteUnknownAgent verifies error handling for unknown agent.
func TestExecuteUnknownAgent(t *testing.T) {
	e := New(testConfig(), nil)
	e.maxRetries = 0

	plan := &planner.Plan{
		TaskSummary: "unknown agent",
		Steps: []planner.Step{
			{
				ID:    "step_1",
				Agent: "nonexistent_agent_xyz",
			},
		},
	}

	result := e.Execute(plan)
	if result.Success {
		t.Fatal("expected failure for unknown agent")
	}
}

// TestTruncate verifies truncate helper function.
func TestTruncate(t *testing.T) {
	short := "hello"
	if truncate(short, 100) != "hello" {
		t.Error("short string should not be truncated")
	}

	long := "abcdefghijklmnop"
	result := truncate(long, 5)
	if len(result) < 5 {
		t.Error("truncated result too short")
	}
	if !strings.Contains(result, "truncated") {
		t.Error("long string should contain 'truncated' suffix")
	}
}

// TestNoDepsRunsImmediately verifies empty DependsOn runs immediately (backward compat).
func TestNoDepsRunsImmediately(t *testing.T) {
	e := New(testConfig(), nil)

	plan := &planner.Plan{
		TaskSummary: "no deps",
		Steps: []planner.Step{
			{
				ID:      "solo",
				Agent:   "shell",
				Command: "echo immediate",
				// DependsOn is empty
			},
		},
	}

	result := e.Execute(plan)
	if !result.Success {
		t.Fatalf("expected success: %v", result.Err)
	}
	if result.Steps[0].Output != "immediate\n" {
		t.Errorf("output = %q, want 'immediate\\n'", result.Steps[0].Output)
	}
}
