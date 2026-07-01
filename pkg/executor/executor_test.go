package executor

import (
	"testing"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/planner"
)

func testConfig() *config.Config {
	return &config.Config{
		Workspace: config.Workspace{Root: "."},
	}
}

func TestExecuteShellStep(t *testing.T) {
	e := New(testConfig())

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
		t.Fatalf("expected success, got failure")
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

func TestExecuteMultiStep(t *testing.T) {
	e := New(testConfig())

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
				DependsOn:   "step_1",
			},
		},
	}

	result := e.Execute(plan)

	if !result.Success {
		t.Fatalf("expected success")
	}
	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(result.Steps))
	}
}

func TestExecuteWithVerify(t *testing.T) {
	e := New(testConfig())

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

func TestExecuteFailedVerify(t *testing.T) {
	e := New(testConfig())
	e.maxRetries = 1 // 減少等待時間

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

func TestExecuteUnknownAgent(t *testing.T) {
	e := New(testConfig())

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

func TestTruncate(t *testing.T) {
	short := "hello"
	if truncate(short, 100) != "hello" {
		t.Error("short string should not be truncated")
	}

	long := "abcdefghij"
	result := truncate(long, 5)
	if len(result) <= 5 {
		// truncate adds "... (truncated)" suffix
	}
}
