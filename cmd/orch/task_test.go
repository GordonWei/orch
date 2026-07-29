package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/planner"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever was written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

// ══════════════════════════════════════════════════════════════════════════
// emitDryRunPlan: --json + --dry-run must still emit structured JSON
// ══════════════════════════════════════════════════════════════════════════

func testDryRunPlan() *planner.Plan {
	return &planner.Plan{
		TaskSummary: "dry run test task",
		Category:    "query",
		Difficulty:  "simple",
		Steps: []planner.Step{
			{ID: "s1", Agent: "shell", Description: "echo hi", Command: "echo hi"},
		},
	}
}

func TestEmitDryRunPlan_JSONMode(t *testing.T) {
	plan := testDryRunPlan()

	out := captureStdout(t, func() {
		emitDryRunPlan(plan, true)
	})

	var got JSONOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json --dry-run did not produce valid JSON: %v\noutput: %s", err, out)
	}
	if !got.Success {
		t.Errorf("Success = false, want true for a dry-run preview")
	}
	if got.Plan == nil || got.Plan.TaskSummary != plan.TaskSummary {
		t.Errorf("Plan not carried through: got %+v", got.Plan)
	}
	if len(got.Steps) != 0 {
		t.Errorf("Steps = %v, want empty (dry-run never executes)", got.Steps)
	}
}

func TestEmitDryRunPlan_TextMode(t *testing.T) {
	plan := testDryRunPlan()

	out := captureStdout(t, func() {
		emitDryRunPlan(plan, false)
	})

	if !strings.Contains(out, "Execution Plan (dry-run)") {
		t.Errorf("text mode output missing human-readable dry-run header, got: %s", out)
	}
	var probe map[string]any
	if json.Unmarshal([]byte(out), &probe) == nil {
		t.Errorf("text mode output parsed as JSON, want plain text: %s", out)
	}
}

// ══════════════════════════════════════════════════════════════════════════
// mergeReplanResult: the outer e.Execute(plan) sentinel must never clobber
// the real outcome the SetRePlanFunc closure already recorded.
// ══════════════════════════════════════════════════════════════════════════

func TestMergeReplanResult(t *testing.T) {
	closureRecorded := executor.Result{Success: true, Steps: []executor.StepResult{{StepID: "s1", Output: "replanned ok"}}}

	tests := []struct {
		name   string
		result executor.Result
		next   executor.Result
		want   executor.Result
	}{
		{
			name:   "no re-plan: next result wins as usual",
			result: executor.Result{},
			next:   executor.Result{Success: true, RePlanCount: 0},
			want:   executor.Result{Success: true, RePlanCount: 0},
		},
		{
			name:   "re-plan sentinel: keep the closure-recorded result instead",
			result: closureRecorded,
			next:   executor.Result{Success: false, Err: nil, RePlanCount: 1},
			want:   closureRecorded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeReplanResult(tt.result, tt.next)
			if got.Success != tt.want.Success {
				t.Errorf("Success = %v, want %v", got.Success, tt.want.Success)
			}
			if len(got.Steps) != len(tt.want.Steps) {
				t.Errorf("Steps = %v, want %v", got.Steps, tt.want.Steps)
			}
		})
	}
}

// TestReplanEndToEnd exercises the exact wiring pattern runTask uses (real
// executor, no mocks): a step configured with on_failure=re-plan fails, the
// SetRePlanFunc closure builds a fresh plan, updates the outer `plan` var,
// executes it, and the caller combines results via mergeReplanResult exactly
// like runTask does. Before the fix, `result = e.Execute(plan)` unconditionally
// overwrote the closure's successful outcome with executor's own
// "re-plan triggered" sentinel, so a re-plan that *succeeded* was still
// reported as a failure.
func TestReplanEndToEnd(t *testing.T) {
	cfg := &config.Config{Workspace: config.Workspace{Root: "."}}
	e := executor.New(cfg, nil)

	failingPlan := &planner.Plan{
		TaskSummary: "original plan (will fail)",
		Steps: []planner.Step{
			{ID: "a", Agent: "shell", Command: "exit 1", OnFailure: "re-plan"},
		},
	}
	replannedPlan := &planner.Plan{
		TaskSummary: "replanned plan (succeeds)",
		Steps: []planner.Step{
			{ID: "b", Agent: "shell", Command: "echo replanned_ok"},
		},
	}

	plan := failingPlan
	var result executor.Result

	e.SetRePlanFunc(func(failedContext string) error {
		plan = replannedPlan
		result = e.Execute(replannedPlan)
		return nil
	})

	result = mergeReplanResult(result, e.Execute(plan))

	if !result.Success {
		t.Fatalf("expected the replanned execution to be reported as success, got Success=false Err=%v", result.Err)
	}
	if len(result.Steps) != 1 || result.Steps[0].StepID != "b" {
		t.Fatalf("result.Steps = %+v, want the replanned step [b], not the original failed step [a]", result.Steps)
	}
	if plan != replannedPlan {
		t.Fatalf("outer plan variable was not updated to the replanned plan")
	}
}
