// Package eventbus provides an in-process event bus for reactive workflow chaining.
//
// When a step completes, the executor publishes an Event to the bus.
// The bus checks all registered TriggerRules; if a rule matches, it dispatches
// the follow-up action (another agent call) automatically.
//
// This is Phase 2 (human-triggered): the bus only processes events within a single
// orch invocation. It does NOT run as a background daemon.
package eventbus

import (
	"fmt"
	"strings"
	"text/template"
)

// Event represents a step completion event.
type Event struct {
	Type     string            // "step.done", "step.failed"
	Agent    string            // which agent completed: "kiro", "claude", "shell", etc.
	Category string            // plan category: "infra", "meeting", "code", etc.
	StepID   string            // step ID that produced this event
	Output   string            // step output (may be truncated/summarized)
	Tags     []string          // additional tags for matching
	Meta     map[string]string // arbitrary key-value metadata
}

// TriggerRule defines when to automatically dispatch a follow-up action.
type TriggerRule struct {
	Name      string `yaml:"name"`                 // rule display name
	On        string `yaml:"on"`                   // event type to match: "step.done"
	Agent     string `yaml:"agent,omitempty"`       // filter: only match this agent's events
	Condition string `yaml:"condition,omitempty"`   // filter: category==X or tag contains Y
	Then      Action `yaml:"then"`                  // what to do when triggered
}

// Action defines the follow-up action to execute.
type Action struct {
	Agent         string `yaml:"agent"`                    // target agent: "claude", "kiro", "gemini", "local"
	PromptTemplate string `yaml:"prompt"`                  // Go template: supports {{.Output}}, {{.Agent}}, {{.Category}}
	MaxContext    int    `yaml:"max_context,omitempty"`    // max chars of output to pass (0=unlimited)
	GateWithMLX   bool   `yaml:"gate_with_mlx,omitempty"` // if true, ask MLX first whether to dispatch
	Summarize     bool   `yaml:"summarize,omitempty"`      // if true, MLX summarize output before passing
}

// TriggeredAction is a resolved action ready for execution.
type TriggeredAction struct {
	RuleName string
	Agent    string
	Prompt   string
}

// Bus is the in-process event bus.
type Bus struct {
	rules []TriggerRule
}

// New creates a new event bus with the given trigger rules.
func New(rules []TriggerRule) *Bus {
	return &Bus{rules: rules}
}

// Process evaluates an event against all rules and returns matching actions.
// Multiple rules can match a single event (all are returned).
func (b *Bus) Process(event Event) ([]TriggeredAction, error) {
	var actions []TriggeredAction

	for _, rule := range b.rules {
		if !b.matches(rule, event) {
			continue
		}

		prompt, err := b.renderPrompt(rule.Then.PromptTemplate, event)
		if err != nil {
			return nil, fmt.Errorf("rule %q prompt render failed: %w", rule.Name, err)
		}

		actions = append(actions, TriggeredAction{
			RuleName: rule.Name,
			Agent:    rule.Then.Agent,
			Prompt:   prompt,
		})
	}

	return actions, nil
}

// Rules returns all registered trigger rules (for display/debug).
func (b *Bus) Rules() []TriggerRule {
	return b.rules
}

// HasRules returns true if any trigger rules are loaded.
func (b *Bus) HasRules() bool {
	return len(b.rules) > 0
}

// matches checks if a rule matches an event.
func (b *Bus) matches(rule TriggerRule, event Event) bool {
	// Match event type
	if rule.On != "" && rule.On != event.Type {
		return false
	}

	// Match agent filter
	if rule.Agent != "" && !strings.EqualFold(rule.Agent, event.Agent) {
		return false
	}

	// Match condition
	if rule.Condition != "" && !evalCondition(rule.Condition, event) {
		return false
	}

	return true
}

// evalCondition evaluates a simple condition string against an event.
// Supports:
//   - "category == infra"
//   - "category != meeting"
//   - "tag contains notion"
//   - "agent == kiro"
func evalCondition(cond string, event Event) bool {
	cond = strings.TrimSpace(cond)

	// category == X
	if strings.HasPrefix(cond, "category") {
		return evalFieldCondition(cond, "category", event.Category)
	}

	// agent == X
	if strings.HasPrefix(cond, "agent") {
		return evalFieldCondition(cond, "agent", event.Agent)
	}

	// tag contains X
	if strings.HasPrefix(cond, "tag") || strings.HasPrefix(cond, "tags") {
		parts := strings.Fields(cond)
		if len(parts) >= 3 && parts[1] == "contains" {
			target := strings.ToLower(parts[2])
			for _, t := range event.Tags {
				if strings.ToLower(t) == target {
					return true
				}
			}
			return false
		}
	}

	// meta.key == value
	if strings.HasPrefix(cond, "meta.") {
		return evalMetaCondition(cond, event.Meta)
	}

	return false
}

func evalFieldCondition(cond, field, value string) bool {
	// Remove field prefix
	rest := strings.TrimPrefix(cond, field)
	rest = strings.TrimSpace(rest)

	if strings.HasPrefix(rest, "==") {
		expected := strings.TrimSpace(strings.TrimPrefix(rest, "=="))
		expected = strings.Trim(expected, "\"'")
		return strings.EqualFold(value, expected)
	}

	if strings.HasPrefix(rest, "!=") {
		expected := strings.TrimSpace(strings.TrimPrefix(rest, "!="))
		expected = strings.Trim(expected, "\"'")
		return !strings.EqualFold(value, expected)
	}

	return false
}

func evalMetaCondition(cond string, meta map[string]string) bool {
	// meta.key == value
	parts := strings.SplitN(cond, "==", 2)
	if len(parts) != 2 {
		parts = strings.SplitN(cond, "!=", 2)
		if len(parts) != 2 {
			return false
		}
		key := strings.TrimPrefix(strings.TrimSpace(parts[0]), "meta.")
		expected := strings.TrimSpace(parts[1])
		expected = strings.Trim(expected, "\"'")
		return meta[key] != expected
	}

	key := strings.TrimPrefix(strings.TrimSpace(parts[0]), "meta.")
	expected := strings.TrimSpace(parts[1])
	expected = strings.Trim(expected, "\"'")
	return meta[key] == expected
}

// renderPrompt renders the prompt template with event data.
func (b *Bus) renderPrompt(tmplStr string, event Event) (string, error) {
	if tmplStr == "" {
		return event.Output, nil
	}

	tmpl, err := template.New("prompt").Parse(tmplStr)
	if err != nil {
		return "", err
	}

	data := map[string]interface{}{
		"Output":   event.Output,
		"Agent":    event.Agent,
		"Category": event.Category,
		"StepID":   event.StepID,
		"Tags":     event.Tags,
		"Meta":     event.Meta,
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// TruncateOutput truncates output to maxChars if specified.
// Returns original if maxChars <= 0.
func TruncateOutput(output string, maxChars int) string {
	if maxChars <= 0 || len(output) <= maxChars {
		return output
	}
	return output[:maxChars] + "\n... (truncated to " + fmt.Sprintf("%d", maxChars) + " chars)"
}
