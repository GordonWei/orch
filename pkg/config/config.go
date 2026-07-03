package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Persona          Persona             `yaml:"persona"`
	AIBackend        AIBackendConfig     `yaml:"ai_backend"`
	LocalLLM         LocalLLM            `yaml:"local_llm"`
	Models           []ModelDef          `yaml:"models"`
	Memory           MemoryConfig        `yaml:"memory"`
	Workflows        WorkflowConfig      `yaml:"workflows"`
	Workspace        Workspace           `yaml:"workspace"`
	Routing          map[string][]string `yaml:"routing"`
	KeywordShortcuts []KeywordShortcut   `yaml:"keyword_shortcuts"`
}

// AIBackendConfig defines which AI CLI backend to use for cloud planning/execution.
type AIBackendConfig struct {
	// Primary is the preferred AI CLI backend: "kiro", "claude", or "gemini".
	// If empty or unavailable, auto-detect is used.
	Primary string `yaml:"primary"`
}

// WorkflowConfig defines the workflow system configuration.
type WorkflowConfig struct {
	Dir string `yaml:"dir"` // workflow YAML directory, default ~/.config/orch/workflows/
}

type Persona struct {
	Name         string `yaml:"name"`
	Owner        string `yaml:"owner"`
	Language     string `yaml:"language"`
	SystemPrompt string `yaml:"system_prompt"`
}

type LocalLLM struct {
	Endpoint   string `yaml:"endpoint"`
	Model      string `yaml:"model"`
	PythonPath string `yaml:"python_path"`
	AutoStart  bool   `yaml:"auto_start"`
}

// ModelDef defines a switchable model backend.
type ModelDef struct {
	Name       string `yaml:"name"`       // display name, e.g., "qwen-3b", "llama-8b"
	Backend    string `yaml:"backend"`    // "mlx", "ollama", "openai-compatible"
	Endpoint   string `yaml:"endpoint"`   // API endpoint URL
	Model      string `yaml:"model"`      // model identifier for the API
	PythonPath string `yaml:"python_path,omitempty"` // only for mlx backend
	AutoStart  bool   `yaml:"auto_start,omitempty"`
	Port       string `yaml:"port,omitempty"`
	Default    bool   `yaml:"default,omitempty"` // which one to use by default
}

// MemoryConfig defines the persistence layer settings.
type MemoryConfig struct {
	DBPath         string `yaml:"db_path"`          // path to orch.db, default ~/.config/orch/orch.db
	BriefingOnBoot bool   `yaml:"briefing_on_boot"` // load briefing into context on start
	AutoSummarize  bool   `yaml:"auto_summarize"`   // MLX summarize after each task
	HistoryLimit   int    `yaml:"history_limit"`     // max rows before pruning (0=unlimited)
}

type Workspace struct {
	Root    string   `yaml:"root"`
	Subdirs []Subdir `yaml:"subdirs"`
}

type Subdir struct {
	Name     string   `yaml:"name"`
	Keywords []string `yaml:"keywords"`
}

type KeywordShortcut struct {
	Prefix   string `yaml:"prefix"`
	Category string `yaml:"category"`
}

// Load reads config with priority: ORCH_CONFIG env → ~/.config/orch/config.yaml → built-in defaults
func Load() *Config {
	cfg := defaultConfig()

	// Find config file
	path := os.Getenv("ORCH_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "orch", "config.yaml")
	}

	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		// Config not found, use defaults
		return cfg
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		// Parse failed, use defaults
		return cfg
	}

	// Expand ~ paths
	cfg.LocalLLM.PythonPath = expandHome(cfg.LocalLLM.PythonPath)
	cfg.Workspace.Root = expandHome(cfg.Workspace.Root)
	cfg.Memory.DBPath = expandHome(cfg.Memory.DBPath)
	cfg.Workflows.Dir = expandHome(cfg.Workflows.Dir)
	for i := range cfg.Models {
		cfg.Models[i].PythonPath = expandHome(cfg.Models[i].PythonPath)
	}

	return cfg
}

// ActiveModel returns the default model definition.
// Falls back to constructing one from LocalLLM if no Models are defined.
func (c *Config) ActiveModel() ModelDef {
	for _, m := range c.Models {
		if m.Default {
			return m
		}
	}
	// fallback: if Models is empty, use legacy LocalLLM field
	if len(c.Models) == 0 {
		return ModelDef{
			Name:       "default",
			Backend:    "mlx",
			Endpoint:   c.LocalLLM.Endpoint,
			Model:      c.LocalLLM.Model,
			PythonPath: c.LocalLLM.PythonPath,
			AutoStart:  c.LocalLLM.AutoStart,
			Port:       "8080",
			Default:    true,
		}
	}
	// no default flag set, use first one
	return c.Models[0]
}

func defaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Persona: Persona{
			Name:     "orch",
			Owner:    "",
			Language: "en",
			SystemPrompt: `You are orch, an AI task orchestration CLI running on macOS (Apple Silicon).
You coordinate AI agents to complete tasks efficiently.

Response rules:
- Be concise and direct
- Keep technical terms in English
- If the user's language is detected, reply in the same language`,
		},
		AIBackend: AIBackendConfig{
			Primary: "", // empty = auto-detect
		},
		LocalLLM: LocalLLM{
			Endpoint:   "http://localhost:8080",
			Model:      "mlx-community/Qwen2.5-3B-Instruct-4bit",
			PythonPath: filepath.Join(home, "mlx-env", "bin", "python3"),
			AutoStart:  true,
		},
		Models: []ModelDef{
			{
				Name:       "qwen-3b",
				Backend:    "mlx",
				Endpoint:   "http://localhost:8080",
				Model:      "mlx-community/Qwen2.5-3B-Instruct-4bit",
				PythonPath: filepath.Join(home, "mlx-env", "bin", "python3"),
				AutoStart:  true,
				Port:       "8080",
				Default:    true,
			},
		},
		Memory: MemoryConfig{
			DBPath:         filepath.Join(home, ".config", "orch", "orch.db"),
			BriefingOnBoot: true,
			AutoSummarize:  true,
			HistoryLimit:   0, // unlimited
		},
		Workflows: WorkflowConfig{
			Dir: filepath.Join(home, ".config", "orch", "workflows"),
		},
		Workspace: Workspace{
			Root:    "",
			Subdirs: []Subdir{},
		},
		Routing: map[string][]string{
			"kiro":   {"code", "infra", "aws", "gcp", "terraform", "deploy", "build", "test", "file-ops", "shell"},
			"claude": {"notion", "gcal", "gmail", "google-workspace", "writing", "analysis", "meeting-notes"},
			"gemini": {"long-context", "video", "image", "summarization", "google-drive"},
		},
		KeywordShortcuts: []KeywordShortcut{
			{Prefix: "kubectl", Category: "infra"},
			{Prefix: "terraform plan", Category: "infra"},
			{Prefix: "terraform apply", Category: "infra"},
			{Prefix: "tf plan", Category: "infra"},
			{Prefix: "tf apply", Category: "infra"},
			{Prefix: "helm upgrade", Category: "deploy"},
			{Prefix: "helm install", Category: "deploy"},
			{Prefix: "helm list", Category: "deploy"},
			{Prefix: "aws s3", Category: "query"},
			{Prefix: "aws ec2", Category: "query"},
			{Prefix: "gcloud compute", Category: "query"},
			{Prefix: "gcloud container", Category: "query"},
		},
	}
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
