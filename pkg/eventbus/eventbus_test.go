package eventbus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBusMatchesBasic(t *testing.T) {
	rules := []TriggerRule{
		{
			Name:  "notify-on-done",
			On:    "step.done",
			Agent: "kiro",
			Then: Action{
				Agent:         "claude",
				PromptTemplate: "sync result: {{.Output}}",
			},
		},
	}

	bus := New(rules)

	// Should match
	event := Event{
		Type:     "step.done",
		Agent:    "kiro",
		Category: "infra",
		Output:   "deployed successfully",
	}

	actions, err := bus.Process(event)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].Agent != "claude" {
		t.Errorf("expected agent=claude, got %q", actions[0].Agent)
	}
	if actions[0].Prompt != "sync result: deployed successfully" {
		t.Errorf("unexpected prompt: %q", actions[0].Prompt)
	}
}

func TestBusNoMatch(t *testing.T) {
	rules := []TriggerRule{
		{
			Name:  "only-kiro",
			On:    "step.done",
			Agent: "kiro",
			Then:  Action{Agent: "claude", PromptTemplate: "test"},
		},
	}

	bus := New(rules)

	// Should NOT match (wrong agent)
	event := Event{
		Type:  "step.done",
		Agent: "claude",
	}

	actions, err := bus.Process(event)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %d", len(actions))
	}
}

func TestBusConditionCategory(t *testing.T) {
	rules := []TriggerRule{
		{
			Name:      "infra-only",
			On:        "step.done",
			Condition: "category == infra",
			Then:      Action{Agent: "claude", PromptTemplate: "update handoff: {{.Output}}"},
		},
	}

	bus := New(rules)

	// Match: category == infra
	actions, _ := bus.Process(Event{Type: "step.done", Agent: "kiro", Category: "infra", Output: "done"})
	if len(actions) != 1 {
		t.Fatalf("expected 1 action for infra, got %d", len(actions))
	}

	// No match: category == meeting
	actions, _ = bus.Process(Event{Type: "step.done", Agent: "kiro", Category: "meeting", Output: "done"})
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions for meeting, got %d", len(actions))
	}
}

func TestBusConditionTagContains(t *testing.T) {
	rules := []TriggerRule{
		{
			Name:      "notion-tag",
			On:        "step.done",
			Condition: "tag contains notion",
			Then:      Action{Agent: "local", PromptTemplate: "summarize: {{.Output}}"},
		},
	}

	bus := New(rules)

	// Match
	actions, _ := bus.Process(Event{Type: "step.done", Agent: "claude", Tags: []string{"notion", "sync"}})
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}

	// No match
	actions, _ = bus.Process(Event{Type: "step.done", Agent: "claude", Tags: []string{"git", "push"}})
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %d", len(actions))
	}
}

func TestBusConditionNotEqual(t *testing.T) {
	rules := []TriggerRule{
		{
			Name:      "not-chat",
			On:        "step.done",
			Condition: "category != chat",
			Then:      Action{Agent: "claude", PromptTemplate: "{{.Output}}"},
		},
	}

	bus := New(rules)

	// Match: not chat
	actions, _ := bus.Process(Event{Type: "step.done", Category: "infra"})
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}

	// No match: is chat
	actions, _ = bus.Process(Event{Type: "step.done", Category: "chat"})
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %d", len(actions))
	}
}

func TestBusMultipleRulesMatch(t *testing.T) {
	rules := []TriggerRule{
		{Name: "rule1", On: "step.done", Then: Action{Agent: "claude", PromptTemplate: "r1"}},
		{Name: "rule2", On: "step.done", Then: Action{Agent: "local", PromptTemplate: "r2"}},
		{Name: "rule3", On: "step.failed", Then: Action{Agent: "claude", PromptTemplate: "r3"}},
	}

	bus := New(rules)

	actions, _ := bus.Process(Event{Type: "step.done"})
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
}

func TestBusEmptyRules(t *testing.T) {
	bus := New(nil)

	actions, err := bus.Process(Event{Type: "step.done"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %d", len(actions))
	}
	if bus.HasRules() {
		t.Error("HasRules should return false")
	}
}

func TestBusTemplateRendering(t *testing.T) {
	rules := []TriggerRule{
		{
			Name: "template-test",
			On:   "step.done",
			Then: Action{
				Agent:         "claude",
				PromptTemplate: "Agent {{.Agent}} finished {{.Category}} task. Output:\n{{.Output}}",
			},
		},
	}

	bus := New(rules)
	actions, err := bus.Process(Event{
		Type:     "step.done",
		Agent:    "kiro",
		Category: "deploy",
		Output:   "helm upgrade complete",
	})
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	expected := "Agent kiro finished deploy task. Output:\nhelm upgrade complete"
	if actions[0].Prompt != expected {
		t.Errorf("prompt mismatch:\ngot:  %q\nwant: %q", actions[0].Prompt, expected)
	}
}

func TestTruncateOutput(t *testing.T) {
	long := "abcdefghij" // 10 chars
	if got := TruncateOutput(long, 5); len(got) < 5 {
		t.Errorf("truncate failed")
	}
	if got := TruncateOutput(long, 0); got != long {
		t.Errorf("0 should mean no truncation")
	}
	if got := TruncateOutput(long, 100); got != long {
		t.Errorf("should not truncate when under limit")
	}
}

func TestLoadRulesFromBytes(t *testing.T) {
	yaml := []byte(`
name: "test-reactive"
mode: "reactive"
triggers:
  - on: "step.done"
    agent: "kiro"
    condition: "category == infra"
    then:
      agent: "claude"
      prompt: "update handoff: {{.Output}}"
  - on: "step.done"
    condition: "tag contains meeting"
    then:
      agent: "local"
      prompt: "summarize: {{.Output}}"
      summarize: true
`)

	rules, err := LoadRulesFromBytes(yaml)
	if err != nil {
		t.Fatalf("LoadRulesFromBytes failed: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].Agent != "kiro" {
		t.Errorf("rule[0].Agent = %q, want kiro", rules[0].Agent)
	}
	if rules[0].Then.Agent != "claude" {
		t.Errorf("rule[0].Then.Agent = %q, want claude", rules[0].Then.Agent)
	}
	if rules[1].Then.Summarize != true {
		t.Error("rule[1].Then.Summarize should be true")
	}
}

func TestLoadRulesFromBytesNonReactive(t *testing.T) {
	yaml := []byte(`
name: "dag-workflow"
mode: "dag"
steps:
  - id: step_1
`)

	_, err := LoadRulesFromBytes(yaml)
	if err == nil {
		t.Error("expected error for non-reactive workflow")
	}
}

func TestLoadRulesFromDir(t *testing.T) {
	// Create temp dir with test files
	dir := t.TempDir()

	// Reactive workflow
	reactive := `
name: "auto-sync"
mode: "reactive"
triggers:
  - on: "step.done"
    agent: "kiro"
    then:
      agent: "claude"
      prompt: "sync {{.Output}}"
`
	os.WriteFile(filepath.Join(dir, "auto-sync.yaml"), []byte(reactive), 0644)

	// Non-reactive (should be skipped)
	dag := `
name: "signoff"
trigger: "signoff"
steps:
  - id: step_1
    agent: kiro
    prompt: "do something"
`
	os.WriteFile(filepath.Join(dir, "signoff.yaml"), []byte(dag), 0644)

	// Non-yaml file (should be skipped)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644)

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (only from reactive file), got %d", len(rules))
	}
	if rules[0].Then.Agent != "claude" {
		t.Errorf("rule.Then.Agent = %q, want claude", rules[0].Then.Agent)
	}
}

func TestLoadRulesNonExistentDir(t *testing.T) {
	rules, err := LoadRules("/nonexistent/path")
	if err != nil {
		t.Fatalf("should not error on nonexistent dir: %v", err)
	}
	if rules != nil {
		t.Errorf("should return nil for nonexistent dir")
	}
}

func TestEvalMetaCondition(t *testing.T) {
	rules := []TriggerRule{
		{
			Name:      "meta-test",
			On:        "step.done",
			Condition: "meta.env == production",
			Then:      Action{Agent: "claude", PromptTemplate: "{{.Output}}"},
		},
	}

	bus := New(rules)

	// Match
	actions, _ := bus.Process(Event{
		Type: "step.done",
		Meta: map[string]string{"env": "production"},
	})
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}

	// No match
	actions, _ = bus.Process(Event{
		Type: "step.done",
		Meta: map[string]string{"env": "staging"},
	})
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %d", len(actions))
	}
}
