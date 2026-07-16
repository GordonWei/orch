// Package workflow provides YAML workflow template system.
// Users can define in ~/.config/orch/workflows/ define reusable multi-step workflows,
// automatically execute via trigger keywords, bypass planner's AI planning phase.
package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/planner"
	"gopkg.in/yaml.v3"
)

// WorkflowStep defines a single step in a workflow
type WorkflowStep struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Agent       string   `yaml:"agent"`
	Prompt      string   `yaml:"prompt,omitempty"`
	Command     string   `yaml:"command,omitempty"`
	VerifyCmd   string   `yaml:"verify_cmd,omitempty"`
	DependsOn   []string `yaml:"depends_on,omitempty"`
	OnFailure   string   `yaml:"on_failure,omitempty"`
}

// Triggers is a string slice that supports YAML unmarshalling from both
// a single string (backward-compatible) and a list of strings.
//
//	trigger: "收工"           → Triggers{"收工"}
//	trigger: ["收工", "下班"]  → Triggers{"收工", "下班"}
type Triggers []string

// UnmarshalYAML implements yaml.Unmarshaler for backward-compatible parsing.
func (t *Triggers) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// single string: trigger: "收工"
		*t = Triggers{value.Value}
		return nil
	case yaml.SequenceNode:
		// list of strings: trigger: ["收工", "下班"]
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*t = list
		return nil
	default:
		// fallback: try scalar
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*t = Triggers{s}
		return nil
	}
}

// Workflow defines a complete workflow template
type Workflow struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Trigger     Triggers          `yaml:"trigger"`
	Variables   map[string]string `yaml:"variables,omitempty"`
	Steps       []WorkflowStep    `yaml:"steps"`
}

// LoadAll loads all .yaml/.yml workflow files from specified directory
func LoadAll(dir string) ([]Workflow, error) {
	dir = expandHome(dir)

	// returns empty slice when directory doesn't exist (not treated as error)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var workflows []Workflow
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // skip files that fail to read
		}

		var w Workflow
		if err := yaml.Unmarshal(data, &w); err != nil {
			continue // skip files with format errors
		}

		// requires at least one non-empty trigger and steps to be valid
		if hasValidTrigger(w.Trigger) && len(w.Steps) > 0 {
			workflows = append(workflows, w)
		}
	}

	return workflows, nil
}

// hasValidTrigger reports whether the Triggers slice contains at least one
// non-empty string.
func hasValidTrigger(triggers Triggers) bool {
	for _, t := range triggers {
		if strings.TrimSpace(t) != "" {
			return true
		}
	}
	return false
}

// Match match workflow trigger keywords against user input
// returns first matching workflow, returns nil if no match
//
// ASCII-only triggers (e.g. "status", "deploy staging") require a word
// boundary on both sides, so a short common English word doesn't fire inside
// an unrelated sentence (e.g. "check the GKE cluster status" or "statusbar"
// must not match the "status" trigger). CJK triggers keep plain substring
// matching — CJK text has no reliable word-boundary concept, and casual
// phrasing like "我要收工了" is expected to still match the "收工" trigger.
func Match(input string, workflows []Workflow) *Workflow {
	inputLower := strings.ToLower(strings.TrimSpace(input))

	for i := range workflows {
		for _, trigger := range workflows[i].Trigger {
			triggerLower := strings.ToLower(strings.TrimSpace(trigger))
			if triggerLower == "" {
				continue
			}

			if inputLower == triggerLower {
				return &workflows[i]
			}

			if config.IsASCIIOnly(triggerLower) {
				if config.ContainsWholeWord(inputLower, triggerLower) {
					return &workflows[i]
				}
				continue
			}

			if strings.Contains(inputLower, triggerLower) {
				return &workflows[i]
			}
		}
	}

	return nil
}

// ToPlanner converts Workflow to planner.Plan for executor
// vars are user-defined variables, merged with built-in variables for template substitution
func ToPlanner(w *Workflow, vars map[string]string, cfg *config.Config) *planner.Plan {
	// merge built-in variables
	allVars := builtinVars(cfg)
	// add workflow's own default variables
	for k, v := range w.Variables {
		allVars[k] = v
	}
	// user-provided variables take priority
	for k, v := range vars {
		allVars[k] = v
	}

	steps := make([]planner.Step, 0, len(w.Steps))
	for _, ws := range w.Steps {
		// template substitution for prompt and command
		prompt := renderTemplate(ws.Prompt, allVars)
		command := renderTemplate(ws.Command, allVars)
		description := renderTemplate(ws.Description, allVars)

		step := planner.Step{
			ID:          ws.ID,
			Description: description,
			Agent:       ws.Agent,
			Prompt:      prompt,
			Command:     command,
			VerifyCmd:   ws.VerifyCmd,
			DependsOn:   ws.DependsOn,
			OnFailure:   ws.OnFailure,
		}
		steps = append(steps, step)
	}

	return &planner.Plan{
		TaskSummary: w.Description,
		Difficulty:  "workflow",
		Category:    "workflow",
		Steps:       steps,
	}
}

// builtinVars returns built-in template variables
func builtinVars(cfg *config.Config) map[string]string {
	now := time.Now()
	vars := map[string]string{
		"date": now.Format("2006-01-02"),
		"time": now.Format("15:04:05"),
	}

	// get username from config
	if cfg != nil && cfg.Persona.Owner != "" {
		vars["user"] = cfg.Persona.Owner
	}

	return vars
}

// renderTemplate execute Go template substitution
// if template parse or render fails, return original string
func renderTemplate(text string, vars map[string]string) string {
	if text == "" {
		return ""
	}

	// quick check: return directly if no template syntax
	if !strings.Contains(text, "{{") {
		return text
	}

	tmpl, err := template.New("wf").Parse(text)
	if err != nil {
		return text
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, vars); err != nil {
		return text
	}

	return buf.String()
}

// expandHome expand ~ prefix path
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
