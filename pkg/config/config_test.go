package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_DefaultConfig(t *testing.T) {
	// Set ORCH_CONFIG to a non-existent path so Load() falls back to defaults
	t.Setenv("ORCH_CONFIG", "/nonexistent/path/config.yaml")

	cfg := Load()

	if cfg == nil {
		t.Fatal("Load() returned nil")
	}

	// Verify persona defaults
	if cfg.Persona.Name != "orch" {
		t.Errorf("Persona.Name = %q, want 'orch'", cfg.Persona.Name)
	}
	if cfg.Persona.Language != "en" {
		t.Errorf("Persona.Language = %q, want 'en'", cfg.Persona.Language)
	}

	// Verify LocalLLM defaults
	if cfg.LocalLLM.Endpoint != "http://localhost:8080" {
		t.Errorf("LocalLLM.Endpoint = %q, want 'http://localhost:8080'", cfg.LocalLLM.Endpoint)
	}
	if cfg.LocalLLM.Model != "mlx-community/Qwen2.5-3B-Instruct-4bit" {
		t.Errorf("LocalLLM.Model = %q, want 'mlx-community/Qwen2.5-3B-Instruct-4bit'", cfg.LocalLLM.Model)
	}
	if !cfg.LocalLLM.AutoStart {
		t.Error("LocalLLM.AutoStart should default to true")
	}

	// Verify models have a default entry
	if len(cfg.Models) == 0 {
		t.Fatal("Models should have at least one default entry")
	}
	if !cfg.Models[0].Default {
		t.Error("first model should have Default=true")
	}

	// Verify memory defaults
	if cfg.Memory.HistoryLimit != 1000 {
		t.Errorf("Memory.HistoryLimit = %d, want 1000", cfg.Memory.HistoryLimit)
	}
	if !cfg.Memory.BriefingOnBoot {
		t.Error("Memory.BriefingOnBoot should default to true")
	}

	// Verify keyword shortcuts
	if len(cfg.KeywordShortcuts) == 0 {
		t.Error("KeywordShortcuts should not be empty")
	}
}

func TestLoad_FromYAML(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	yamlContent := `
persona:
  name: "test-orch"
  owner: "TestUser"
  language: "zh-TW"
  system_prompt: "You are a test assistant."

ai_backend:
  primary: "claude"

local_llm:
  endpoint: "http://localhost:9090"
  model: "test-model/v1"
  python_path: "~/test-env/bin/python3"
  auto_start: false

models:
  - name: "test-model"
    backend: "ollama"
    endpoint: "http://localhost:11434"
    model: "llama3"
    default: true
  - name: "secondary"
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "qwen-3b"

memory:
  db_path: "~/test-data/orch.db"
  briefing_on_boot: false
  auto_summarize: true
  history_limit: 500

workspace:
  root: "~/Projects"
  subdirs:
    - name: "infra"
      keywords: ["terraform", "kubernetes"]

keyword_shortcuts:
  - prefix: "myctl"
    category: "custom"

route_rules:
  cooldown: 5
  auto_route: true
  history_size: 10
  rules:
    - pattern: "custom phrase"
      target: "kiro"
      strength: 3
      type: "phrase"
`

	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ORCH_CONFIG", configPath)

	cfg := Load()
	if cfg == nil {
		t.Fatal("Load() returned nil")
	}

	// Persona
	if cfg.Persona.Name != "test-orch" {
		t.Errorf("Persona.Name = %q, want 'test-orch'", cfg.Persona.Name)
	}
	if cfg.Persona.Owner != "TestUser" {
		t.Errorf("Persona.Owner = %q, want 'TestUser'", cfg.Persona.Owner)
	}
	if cfg.Persona.Language != "zh-TW" {
		t.Errorf("Persona.Language = %q, want 'zh-TW'", cfg.Persona.Language)
	}

	// AI Backend
	if cfg.AIBackend.Primary != "claude" {
		t.Errorf("AIBackend.Primary = %q, want 'claude'", cfg.AIBackend.Primary)
	}

	// LocalLLM
	if cfg.LocalLLM.Endpoint != "http://localhost:9090" {
		t.Errorf("LocalLLM.Endpoint = %q, want 'http://localhost:9090'", cfg.LocalLLM.Endpoint)
	}
	if cfg.LocalLLM.AutoStart {
		t.Error("LocalLLM.AutoStart should be false")
	}

	// Models
	if len(cfg.Models) != 2 {
		t.Fatalf("len(Models) = %d, want 2", len(cfg.Models))
	}
	if cfg.Models[0].Name != "test-model" {
		t.Errorf("Models[0].Name = %q, want 'test-model'", cfg.Models[0].Name)
	}
	if cfg.Models[0].Backend != "ollama" {
		t.Errorf("Models[0].Backend = %q, want 'ollama'", cfg.Models[0].Backend)
	}

	// Memory
	if cfg.Memory.HistoryLimit != 500 {
		t.Errorf("Memory.HistoryLimit = %d, want 500", cfg.Memory.HistoryLimit)
	}
	if cfg.Memory.BriefingOnBoot {
		t.Error("Memory.BriefingOnBoot should be false")
	}

	// Workspace
	if len(cfg.Workspace.Subdirs) != 1 {
		t.Fatalf("len(Workspace.Subdirs) = %d, want 1", len(cfg.Workspace.Subdirs))
	}
	if cfg.Workspace.Subdirs[0].Name != "infra" {
		t.Errorf("Workspace.Subdirs[0].Name = %q, want 'infra'", cfg.Workspace.Subdirs[0].Name)
	}

	// KeywordShortcuts
	if len(cfg.KeywordShortcuts) != 1 {
		t.Fatalf("len(KeywordShortcuts) = %d, want 1", len(cfg.KeywordShortcuts))
	}
	if cfg.KeywordShortcuts[0].Prefix != "myctl" {
		t.Errorf("KeywordShortcuts[0].Prefix = %q, want 'myctl'", cfg.KeywordShortcuts[0].Prefix)
	}

	// RouteRules
	if cfg.RouteRules.Cooldown != 5 {
		t.Errorf("RouteRules.Cooldown = %d, want 5", cfg.RouteRules.Cooldown)
	}
	if !cfg.RouteRules.AutoRoute {
		t.Error("RouteRules.AutoRoute should be true")
	}
	if cfg.RouteRules.HistorySize != 10 {
		t.Errorf("RouteRules.HistorySize = %d, want 10", cfg.RouteRules.HistorySize)
	}
	if len(cfg.RouteRules.Rules) != 1 {
		t.Fatalf("len(RouteRules.Rules) = %d, want 1", len(cfg.RouteRules.Rules))
	}
	if cfg.RouteRules.Rules[0].Pattern != "custom phrase" {
		t.Errorf("RouteRules.Rules[0].Pattern = %q, want 'custom phrase'", cfg.RouteRules.Rules[0].Pattern)
	}
}

func TestLoad_ExpandHome(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	yamlContent := `
local_llm:
  endpoint: "http://localhost:8080"
  model: "test-model"
  python_path: "~/my-env/bin/python3"

memory:
  db_path: "~/data/orch.db"

workflows:
  dir: "~/my-workflows"

workspace:
  root: "~/Workspace"
`

	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ORCH_CONFIG", configPath)

	cfg := Load()
	home, _ := os.UserHomeDir()

	// All ~ paths should be expanded
	if strings.HasPrefix(cfg.LocalLLM.PythonPath, "~/") {
		t.Errorf("PythonPath not expanded: %q", cfg.LocalLLM.PythonPath)
	}
	if !strings.HasPrefix(cfg.LocalLLM.PythonPath, home) {
		t.Errorf("PythonPath = %q, want prefix %q", cfg.LocalLLM.PythonPath, home)
	}

	if strings.HasPrefix(cfg.Memory.DBPath, "~/") {
		t.Errorf("Memory.DBPath not expanded: %q", cfg.Memory.DBPath)
	}
	if !strings.HasPrefix(cfg.Memory.DBPath, home) {
		t.Errorf("Memory.DBPath = %q, want prefix %q", cfg.Memory.DBPath, home)
	}

	if strings.HasPrefix(cfg.Workflows.Dir, "~/") {
		t.Errorf("Workflows.Dir not expanded: %q", cfg.Workflows.Dir)
	}
	if !strings.HasPrefix(cfg.Workflows.Dir, home) {
		t.Errorf("Workflows.Dir = %q, want prefix %q", cfg.Workflows.Dir, home)
	}

	if strings.HasPrefix(cfg.Workspace.Root, "~/") {
		t.Errorf("Workspace.Root not expanded: %q", cfg.Workspace.Root)
	}
	if !strings.HasPrefix(cfg.Workspace.Root, home) {
		t.Errorf("Workspace.Root = %q, want prefix %q", cfg.Workspace.Root, home)
	}
}

func TestActiveModel_DefaultFlag(t *testing.T) {
	cfg := &Config{
		Models: []ModelDef{
			{Name: "first", Backend: "mlx", Default: false},
			{Name: "second", Backend: "ollama", Default: true},
			{Name: "third", Backend: "mlx", Default: false},
		},
	}

	active := cfg.ActiveModel()
	if active.Name != "second" {
		t.Errorf("ActiveModel().Name = %q, want 'second' (has Default=true)", active.Name)
	}
	if active.Backend != "ollama" {
		t.Errorf("ActiveModel().Backend = %q, want 'ollama'", active.Backend)
	}
}

func TestActiveModel_Fallback(t *testing.T) {
	t.Run("no_default_flag", func(t *testing.T) {
		cfg := &Config{
			Models: []ModelDef{
				{Name: "alpha", Backend: "mlx"},
				{Name: "beta", Backend: "ollama"},
			},
		}

		active := cfg.ActiveModel()
		if active.Name != "alpha" {
			t.Errorf("ActiveModel().Name = %q, want 'alpha' (first model as fallback)", active.Name)
		}
	})

	t.Run("no_models_defined", func(t *testing.T) {
		cfg := &Config{
			Models: []ModelDef{},
			LocalLLM: LocalLLM{
				Endpoint:   "http://localhost:8080",
				Model:      "legacy-model",
				PythonPath: "/usr/bin/python3",
				AutoStart:  true,
			},
		}

		active := cfg.ActiveModel()
		if active.Name != "default" {
			t.Errorf("ActiveModel().Name = %q, want 'default' (fallback from LocalLLM)", active.Name)
		}
		if active.Model != "legacy-model" {
			t.Errorf("ActiveModel().Model = %q, want 'legacy-model'", active.Model)
		}
		if active.Backend != "mlx" {
			t.Errorf("ActiveModel().Backend = %q, want 'mlx'", active.Backend)
		}
	})
}

func TestDefaultConfig_RouteRules(t *testing.T) {
	t.Setenv("ORCH_CONFIG", "/nonexistent/path/config.yaml")
	cfg := Load()

	rules := cfg.RouteRules

	// Cooldown should be set
	if rules.Cooldown <= 0 {
		t.Errorf("RouteRules.Cooldown = %d, want > 0", rules.Cooldown)
	}

	// HistorySize should be set
	if rules.HistorySize <= 0 {
		t.Errorf("RouteRules.HistorySize = %d, want > 0", rules.HistorySize)
	}

	// AutoRoute should default to false
	if rules.AutoRoute {
		t.Error("RouteRules.AutoRoute should default to false")
	}

	// Should have rules for all types
	typeCounts := map[string]int{}
	for _, rule := range rules.Rules {
		typeCounts[rule.Type]++
	}

	if typeCounts["phrase"] == 0 {
		t.Error("should have phrase rules")
	}
	if typeCounts["keyword"] == 0 {
		t.Error("should have keyword rules")
	}
	if typeCounts["cli"] == 0 {
		t.Error("should have cli rules")
	}
	if typeCounts["chat"] == 0 {
		t.Error("should have chat rules")
	}

	// Verify specific important rules exist
	foundRules := map[string]bool{}
	for _, rule := range rules.Rules {
		foundRules[rule.Pattern+":"+rule.Type] = true
	}

	requiredRules := []string{
		"kubectl:cli",
		"terraform:cli",
		"git:cli",
		"ls:cli",
		"notion:keyword",
		"部署:keyword",
		"你好:chat",
		"hello:chat",
		"terraform plan:phrase",
		"會議記錄:phrase",
		"meeting notes:phrase",
	}

	for _, required := range requiredRules {
		if !foundRules[required] {
			t.Errorf("missing required rule: %s", required)
		}
	}

	// Verify targets are valid
	validTargets := map[string]bool{"claude": true, "kiro": true, "shell": true, "local": true}
	for _, rule := range rules.Rules {
		if !validTargets[rule.Target] {
			t.Errorf("rule %q has invalid target %q", rule.Pattern, rule.Target)
		}
	}

	// Verify strengths are in valid range
	for _, rule := range rules.Rules {
		if rule.Strength < 1 || rule.Strength > 3 {
			t.Errorf("rule %q has invalid strength %d (want 1-3)", rule.Pattern, rule.Strength)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()

	cases := []struct {
		name string
		path string
		want string
	}{
		{"tilde_prefix", "~/Documents", filepath.Join(home, "Documents")},
		{"tilde_nested", "~/a/b/c", filepath.Join(home, "a/b/c")},
		{"no_tilde", "/usr/local/bin", "/usr/local/bin"},
		{"relative", "relative/path", "relative/path"},
		{"empty", "", ""},
		{"tilde_only_file", "~/file.txt", filepath.Join(home, "file.txt")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandHome(tc.path)
			if got != tc.want {
				t.Errorf("expandHome(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
