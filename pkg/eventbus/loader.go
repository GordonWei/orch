package eventbus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReactiveWorkflow represents a reactive workflow YAML file.
type ReactiveWorkflow struct {
	Name     string        `yaml:"name"`
	Mode     string        `yaml:"mode"`     // "reactive"
	Triggers []TriggerRule `yaml:"triggers"`
}

// LoadRules loads trigger rules from all reactive workflow YAML files in a directory.
// Only files with mode: "reactive" are loaded. Regular (DAG) workflows are skipped.
func LoadRules(dir string) ([]TriggerRule, error) {
	if dir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workflow dir: %w", err)
	}

	var allRules []TriggerRule

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)
		rules, err := loadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		allRules = append(allRules, rules...)
	}

	return allRules, nil
}

// loadFile loads trigger rules from a single YAML file.
// Returns empty slice (not error) if the file is not a reactive workflow.
func loadFile(path string) ([]TriggerRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var wf ReactiveWorkflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}

	// Skip non-reactive workflows
	if wf.Mode != "reactive" {
		return nil, nil
	}

	// Default event type if not specified
	for i := range wf.Triggers {
		if wf.Triggers[i].On == "" {
			wf.Triggers[i].On = "step.done"
		}
		if wf.Triggers[i].Name == "" {
			wf.Triggers[i].Name = fmt.Sprintf("%s/trigger_%d", wf.Name, i+1)
		}
	}

	return wf.Triggers, nil
}

// LoadRulesFromBytes loads trigger rules from YAML bytes (for testing).
func LoadRulesFromBytes(data []byte) ([]TriggerRule, error) {
	var wf ReactiveWorkflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}

	if wf.Mode != "reactive" {
		return nil, fmt.Errorf("not a reactive workflow (mode=%q)", wf.Mode)
	}

	for i := range wf.Triggers {
		if wf.Triggers[i].On == "" {
			wf.Triggers[i].On = "step.done"
		}
		if wf.Triggers[i].Name == "" {
			wf.Triggers[i].Name = fmt.Sprintf("%s/trigger_%d", wf.Name, i+1)
		}
	}

	return wf.Triggers, nil
}
