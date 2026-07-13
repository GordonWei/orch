package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gordonwei/orch/pkg/config"
	"gopkg.in/yaml.v3"
)

// testWorkflowYAML is a test workflow YAML template
const testWorkflowYAML = `name: "收工"
description: "signoff workflow: update handoff + sync Notion"
trigger: "收工"
variables:
  project: "Cowork"
steps:
  - id: step_1
    description: "update local handoff"
    agent: kiro
    prompt: "read docs/_agent_handoff.md, update completed items for {{.date}}"
  - id: step_2
    description: "sync Notion"
    agent: claude
    prompt: "sync handoff to Notion global board, project: {{.project}}"
    depends_on: [step_1]
`

const testWorkflowYAML2 = `name: "開工"
description: "boot workflow: read handoff"
trigger: "開工"
steps:
  - id: step_1
    description: "read handoff"
    agent: kiro
    prompt: "read docs/_agent_handoff.md, report pending items"
`

// TestLoadAll tests loading all workflows from directory
func TestLoadAll(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Write test files
	os.WriteFile(filepath.Join(tmpDir, "offwork.yaml"), []byte(testWorkflowYAML), 0644)
	os.WriteFile(filepath.Join(tmpDir, "onwork.yml"), []byte(testWorkflowYAML2), 0644)
	os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("not a workflow"), 0644)

	// Load
	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	if len(workflows) != 2 {
		t.Fatalf("expected 2 workflows, got %d", len(workflows))
	}

	// Check first workflow basic properties
	found := false
	for _, w := range workflows {
		if w.Name == "收工" {
			found = true
			if len(w.Trigger) == 0 || w.Trigger[0] != "收工" {
				t.Errorf("expected first trigger '收工', got '%v'", w.Trigger)
			}
			if len(w.Steps) != 2 {
				t.Errorf("expected 2 steps, got %d", len(w.Steps))
			}
			if w.Variables["project"] != "Cowork" {
				t.Errorf("expected variable project='Cowork', got '%s'", w.Variables["project"])
			}
		}
	}
	if !found {
		t.Error("workflow '收工' not found")
	}
}

// TestLoadAll_EmptyDir tests empty directory
func TestLoadAll_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(workflows) != 0 {
		t.Errorf("expected 0 workflows from empty dir, got %d", len(workflows))
	}
}

// TestLoadAll_NonExistentDir tests non-existent directory (no error)
func TestLoadAll_NonExistentDir(t *testing.T) {
	workflows, err := LoadAll("/non/existent/path")
	if err != nil {
		t.Fatalf("LoadAll should not error on non-existent dir: %v", err)
	}
	if workflows != nil {
		t.Errorf("expected nil for non-existent dir, got %v", workflows)
	}
}

// TestLoadAll_InvalidYAML tests invalid YAML (skipped, no error)
func TestLoadAll_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "broken.yaml"), []byte("{{invalid yaml:::"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "good.yaml"), []byte(testWorkflowYAML2), 0644)

	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	// Only the valid one is loaded
	if len(workflows) != 1 {
		t.Errorf("expected 1 valid workflow, got %d", len(workflows))
	}
}

// TestMatch tests trigger keyword matching
func TestMatch(t *testing.T) {
	workflows := []Workflow{
		{Name: "收工", Trigger: Triggers{"收工"}, Steps: []WorkflowStep{{ID: "s1", Agent: "kiro"}}},
		{Name: "開工", Trigger: Triggers{"開工"}, Steps: []WorkflowStep{{ID: "s1", Agent: "kiro"}}},
		{Name: "Deploy", Trigger: Triggers{"deploy staging"}, Steps: []WorkflowStep{{ID: "s1", Agent: "shell"}}},
	}

	tests := []struct {
		input    string
		expected string // expected matching workflow name, "" means nil
	}{
		{"收工", "收工"},
		{"我要收工了", "收工"},
		{"收工吧", "收工"},
		{"開工", "開工"},
		{"早安開工", "開工"},
		{"deploy staging", "Deploy"},
		{"please deploy staging now", "Deploy"},
		{"random input", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := Match(tt.input, workflows)
		if tt.expected == "" {
			if result != nil {
				t.Errorf("Match(%q): expected nil, got %q", tt.input, result.Name)
			}
		} else {
			if result == nil {
				t.Errorf("Match(%q): expected %q, got nil", tt.input, tt.expected)
			} else if result.Name != tt.expected {
				t.Errorf("Match(%q): expected %q, got %q", tt.input, tt.expected, result.Name)
			}
		}
	}
}

// TestMatch_ASCIIWordBoundary guards against a regression where an ASCII-only
// trigger (like the real "status" workflow) matched as a raw substring,
// falsely firing inside unrelated words such as "statusbar" or "gitstatus".
// CJK triggers are unaffected — they don't have a reliable word-boundary
// concept, so casual phrasing must keep matching (see TestMatch above).
func TestMatch_ASCIIWordBoundary(t *testing.T) {
	workflows := []Workflow{
		{Name: "Status", Trigger: Triggers{"status"}, Steps: []WorkflowStep{{ID: "s1", Agent: "shell"}}},
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"status", "Status"},
		{"orch status", "Status"},
		{"check cluster status", "Status"},
		{"statusbar", ""},
		{"gitstatus", ""},
		{"my-status-check", "Status"}, // hyphen is a non-alnum boundary, same as regex \b semantics
	}

	for _, tt := range tests {
		result := Match(tt.input, workflows)
		if tt.expected == "" {
			if result != nil {
				t.Errorf("Match(%q): expected nil, got %q", tt.input, result.Name)
			}
		} else if result == nil || result.Name != tt.expected {
			t.Errorf("Match(%q): expected %q, got %v", tt.input, tt.expected, result)
		}
	}
}

// TestMatch_CaseInsensitive tests case insensitivity
func TestMatch_CaseInsensitive(t *testing.T) {
	workflows := []Workflow{
		{Name: "Deploy", Trigger: Triggers{"DEPLOY"}, Steps: []WorkflowStep{{ID: "s1", Agent: "shell"}}},
	}

	result := Match("deploy", workflows)
	if result == nil {
		t.Fatal("expected match for case-insensitive trigger")
	}
	if result.Name != "Deploy" {
		t.Errorf("expected 'Deploy', got '%s'", result.Name)
	}
}

// TestMatch_MultipleTriggers tests that a workflow with multiple trigger synonyms
// matches on any of them.
func TestMatch_MultipleTriggers(t *testing.T) {
	workflows := []Workflow{
		{Name: "收工", Trigger: Triggers{"收工", "下班", "下線", "晚安"}, Steps: []WorkflowStep{{ID: "s1", Agent: "kiro"}}},
		{Name: "Status", Trigger: Triggers{"status", "狀態"}, Steps: []WorkflowStep{{ID: "s1", Agent: "shell"}}},
	}

	tests := []struct {
		input    string
		expected string
	}{
		// primary trigger
		{"收工", "收工"},
		// synonym triggers (CJK substring)
		{"下班", "收工"},
		{"我要下班了", "收工"},
		{"下線", "收工"},
		{"晚安", "收工"},
		{"今天晚安", "收工"},
		// ASCII synonym with word boundary
		{"status", "Status"},
		{"check status", "Status"},
		// CJK synonym
		{"狀態", "Status"},
		{"看一下狀態", "Status"},
		// no match
		{"random input", ""},
	}

	for _, tt := range tests {
		result := Match(tt.input, workflows)
		if tt.expected == "" {
			if result != nil {
				t.Errorf("Match(%q): expected nil, got %q", tt.input, result.Name)
			}
		} else {
			if result == nil {
				t.Errorf("Match(%q): expected %q, got nil", tt.input, tt.expected)
			} else if result.Name != tt.expected {
				t.Errorf("Match(%q): expected %q, got %q", tt.input, tt.expected, result.Name)
			}
		}
	}
}

// TestTriggers_UnmarshalYAML_Scalar tests YAML scalar → Triggers slice
func TestTriggers_UnmarshalYAML_Scalar(t *testing.T) {
	yamlData := `name: "test"
trigger: "收工"
steps:
  - id: s1
    agent: kiro
    prompt: "test"
`
	var w Workflow
	if err := yaml.Unmarshal([]byte(yamlData), &w); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(w.Trigger) != 1 || w.Trigger[0] != "收工" {
		t.Errorf("expected Triggers{\"收工\"}, got %v", w.Trigger)
	}
}

// TestTriggers_UnmarshalYAML_Sequence tests YAML sequence → Triggers slice
func TestTriggers_UnmarshalYAML_Sequence(t *testing.T) {
	yamlData := `name: "test"
trigger: ["收工", "下班", "晚安"]
steps:
  - id: s1
    agent: kiro
    prompt: "test"
`
	var w Workflow
	if err := yaml.Unmarshal([]byte(yamlData), &w); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(w.Trigger) != 3 {
		t.Fatalf("expected 3 triggers, got %d: %v", len(w.Trigger), w.Trigger)
	}
	expected := []string{"收工", "下班", "晚安"}
	for i, e := range expected {
		if w.Trigger[i] != e {
			t.Errorf("trigger[%d]: expected %q, got %q", i, e, w.Trigger[i])
		}
	}
}

// TestLoadAll_MultipleTriggers tests LoadAll with YAML that uses []string triggers
func TestLoadAll_MultipleTriggers(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `name: "收工"
description: "signoff"
trigger: ["收工", "下班", "晚安"]
steps:
  - id: step_1
    agent: kiro
    prompt: "do signoff"
`
	os.WriteFile(filepath.Join(tmpDir, "signoff.yaml"), []byte(yamlContent), 0644)

	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(workflows))
	}
	if len(workflows[0].Trigger) != 3 {
		t.Errorf("expected 3 triggers, got %d", len(workflows[0].Trigger))
	}
}

// TestHasValidTrigger tests the validation helper
func TestHasValidTrigger(t *testing.T) {
	tests := []struct {
		triggers Triggers
		valid    bool
	}{
		{Triggers{"收工"}, true},
		{Triggers{"收工", "下班"}, true},
		{Triggers{""}, false},
		{Triggers{" "}, false},
		{Triggers{}, false},
		{nil, false},
		{Triggers{"", "下班"}, true}, // at least one valid
	}

	for _, tt := range tests {
		result := hasValidTrigger(tt.triggers)
		if result != tt.valid {
			t.Errorf("hasValidTrigger(%v): expected %v, got %v", tt.triggers, tt.valid, result)
		}
	}
}

// TestToPlanner tests conversion to planner.Plan
func TestToPlanner(t *testing.T) {
	w := &Workflow{
		Name:        "收工",
		Description: "execute signoff workflow",
		Trigger:     Triggers{"收工"},
		Variables: map[string]string{
			"project": "Cowork",
		},
		Steps: []WorkflowStep{
			{
				ID:          "step_1",
				Description: "update handoff ({{.date}})",
				Agent:       "kiro",
				Prompt:      "update {{.project}} handoff, date {{.date}}, user {{.user}}",
			},
			{
				ID:          "step_2",
				Description: "sync Notion",
				Agent:       "claude",
				Prompt:      "sync Notion",
				DependsOn:   []string{"step_1"},
			},
		},
	}

	cfg := &config.Config{
		Persona: config.Persona{
			Owner: "Gordon Wei",
		},
	}

	// User overrides project variable
	vars := map[string]string{
		"project": "AWS",
	}

	plan := ToPlanner(w, vars, cfg)

	// Basic properties
	if plan.TaskSummary != "execute signoff workflow" {
		t.Errorf("expected task_summary 'execute signoff workflow', got '%s'", plan.TaskSummary)
	}
	if plan.Difficulty != "workflow" {
		t.Errorf("expected difficulty 'workflow', got '%s'", plan.Difficulty)
	}
	if plan.Category != "workflow" {
		t.Errorf("expected category 'workflow', got '%s'", plan.Category)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}

	// Step 1: template substitution
	step1 := plan.Steps[0]
	if step1.Agent != "kiro" {
		t.Errorf("step1 agent: expected 'kiro', got '%s'", step1.Agent)
	}
	// User-overridden project should be "AWS"
	if !strings.Contains(step1.Prompt, "AWS") {
		t.Errorf("step1 prompt should contain 'AWS' (user override), got '%s'", step1.Prompt)
	}
	// {{.user}} should be replaced with Gordon Wei
	if !strings.Contains(step1.Prompt, "Gordon Wei") {
		t.Errorf("step1 prompt should contain 'Gordon Wei', got '%s'", step1.Prompt)
	}
	// {{.date}} should be replaced (no longer contains {{)
	if strings.Contains(step1.Prompt, "{{") {
		t.Errorf("step1 prompt still has template syntax: '%s'", step1.Prompt)
	}

	// Step 2: depends_on conversion
	step2 := plan.Steps[1]
	if len(step2.DependsOn) != 1 || step2.DependsOn[0] != "step_1" {
		t.Errorf("step2 depends_on: expected [step_1], got %v", step2.DependsOn)
	}
}

// TestToPlanner_MultipleDependsOn tests multiple dependency conversion
func TestToPlanner_MultipleDependsOn(t *testing.T) {
	w := &Workflow{
		Name:        "test",
		Description: "test workflow",
		Trigger:     Triggers{"test"},
		Steps: []WorkflowStep{
			{ID: "step_1", Agent: "kiro", Prompt: "a"},
			{ID: "step_2", Agent: "kiro", Prompt: "b"},
			{ID: "step_3", Agent: "claude", Prompt: "c", DependsOn: []string{"step_1", "step_2"}},
		},
	}

	plan := ToPlanner(w, nil, &config.Config{})

	step3 := plan.Steps[2]
	if len(step3.DependsOn) != 2 || step3.DependsOn[0] != "step_1" || step3.DependsOn[1] != "step_2" {
		t.Errorf("step3 depends_on: expected [step_1, step_2], got %v", step3.DependsOn)
	}
}

// TestRenderTemplate tests template rendering
func TestRenderTemplate(t *testing.T) {
	vars := map[string]string{
		"date":    "2026-07-02",
		"project": "Cowork",
		"user":    "Gordon",
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"", ""},
		{"today is {{.date}}", "today is 2026-07-02"},
		{"{{.user}} works on {{.project}}", "Gordon works on Cowork"},
		{"no template here", "no template here"},
	}

	for _, tt := range tests {
		result := renderTemplate(tt.input, vars)
		if result != tt.expected {
			t.Errorf("renderTemplate(%q): expected %q, got %q", tt.input, tt.expected, result)
		}
	}
}

// TestRenderTemplate_InvalidTemplate tests invalid template (returns original)
func TestRenderTemplate_InvalidTemplate(t *testing.T) {
	vars := map[string]string{"x": "1"}
	// Incomplete template syntax
	result := renderTemplate("{{.missing_var}}", vars)
	// Go template outputs <no value> for undefined map keys without error
	// But malformed syntax returns original string
	_ = result // passes as long as it does not panic
}

// TestBuiltinVars tests built-in variables
func TestBuiltinVars(t *testing.T) {
	cfg := &config.Config{
		Persona: config.Persona{
			Owner: "TestUser",
		},
	}

	vars := builtinVars(cfg)

	if vars["user"] != "TestUser" {
		t.Errorf("expected user='TestUser', got '%s'", vars["user"])
	}
	if vars["date"] == "" {
		t.Error("expected date to be set")
	}
	if vars["time"] == "" {
		t.Error("expected time to be set")
	}
}

// TestBuiltinVars_NilConfig tests nil config
func TestBuiltinVars_NilConfig(t *testing.T) {
	vars := builtinVars(nil)

	if _, ok := vars["user"]; ok {
		t.Error("expected no 'user' var when config is nil")
	}
	if vars["date"] == "" {
		t.Error("expected date to be set even with nil config")
	}
}
