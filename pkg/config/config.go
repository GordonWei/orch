package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RouteRule defines a single routing rule: a pattern that maps to a target backend.
type RouteRule struct {
	Pattern  string `yaml:"pattern"`
	Target   string `yaml:"target"`   // backend: "claude", "kiro", "shell"
	Strength int    `yaml:"strength"` // 1=weak, 2=medium, 3=strong
	Type     string `yaml:"type"`     // "phrase", "keyword", "cli", "chat"
}

// RouteRulesConfig holds the unified routing configuration.
type RouteRulesConfig struct {
	Rules       []RouteRule `yaml:"rules"`
	Cooldown    int         `yaml:"cooldown"`      // min inputs between hints
	AutoRoute   bool        `yaml:"auto_route"`    // auto-spawn+switch
	HistorySize int         `yaml:"history_size"`  // context-aware window
}

type Config struct {
	Persona          Persona             `yaml:"persona"`
	AIBackend        AIBackendConfig     `yaml:"ai_backend"`
	LocalLLM         LocalLLM            `yaml:"local_llm"`
	Models           []ModelDef          `yaml:"models"`
	Memory           MemoryConfig        `yaml:"memory"`
	Workflows        WorkflowConfig      `yaml:"workflows"`
	Workspace        Workspace           `yaml:"workspace"`
	KeywordShortcuts []KeywordShortcut   `yaml:"keyword_shortcuts"`
	RouteRules       RouteRulesConfig    `yaml:"route_rules"`
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
			HistoryLimit:   1000, // auto-prune oldest entries beyond this limit
		},
		Workflows: WorkflowConfig{
			Dir: filepath.Join(home, ".config", "orch", "workflows"),
		},
		Workspace: Workspace{
			Root:    "",
			Subdirs: []Subdir{},
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
		RouteRules: defaultRouteRules(),
	}
}

// defaultRouteRules returns the full set of routing rules migrated from
// route_hint.go (phrase + keyword rules) and classifyInputType (cli + chat rules).
func defaultRouteRules() RouteRulesConfig {
	return RouteRulesConfig{
		Cooldown:    3,
		AutoRoute:   false,
		HistorySize: 5,
		Rules: []RouteRule{
			// ══════════════════════════════════════════════════════════════
			// PHRASE RULES (type="phrase") — multi-word patterns, checked first
			// ══════════════════════════════════════════════════════════════

			// Claude (Victoria) phrases
			{Pattern: "notion page", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "meeting notes", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "會議記錄", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "分析報告", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "同步 notion", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "推 notion", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "寫交接", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "更新交接", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "客戶提案", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "數位銀行", Target: "claude", Strength: 3, Type: "phrase"},
			{Pattern: "google calendar", Target: "claude", Strength: 3, Type: "phrase"},

			// Kiro phrases
			{Pattern: "terraform plan", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "terraform apply", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "terraform init", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "kubectl apply", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "kubectl get", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "docker build", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "docker compose", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "helm install", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "helm upgrade", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "git push", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "git commit", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "cloud run", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "ci/cd", Target: "kiro", Strength: 3, Type: "phrase"},
			{Pattern: "設定檔", Target: "kiro", Strength: 2, Type: "phrase"},

			// ══════════════════════════════════════════════════════════════
			// KEYWORD RULES (type="keyword") — single-word patterns
			// ══════════════════════════════════════════════════════════════

			// Claude keywords — strong (strength 3)
			{Pattern: "notion", Target: "claude", Strength: 3, Type: "keyword"},
			{Pattern: "gcal", Target: "claude", Strength: 3, Type: "keyword"},
			{Pattern: "gmail", Target: "claude", Strength: 3, Type: "keyword"},
			{Pattern: "salesforce", Target: "claude", Strength: 3, Type: "keyword"},
			{Pattern: "podcast", Target: "claude", Strength: 3, Type: "keyword"},
			{Pattern: "notebooklm", Target: "claude", Strength: 3, Type: "keyword"},

			// Claude keywords — medium (strength 2)
			{Pattern: "calendar", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "pptx", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "簡報", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "週報", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "月報", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "報表", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "交接", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "同步", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "整理", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "客戶", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "銀行", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "提案", Target: "claude", Strength: 2, Type: "keyword"},
			{Pattern: "writing", Target: "claude", Strength: 2, Type: "keyword"},

			// Claude keywords — weak (strength 1)
			{Pattern: "會議", Target: "claude", Strength: 1, Type: "keyword"},
			{Pattern: "筆記", Target: "claude", Strength: 1, Type: "keyword"},

			// Kiro keywords — strong (strength 3)
			{Pattern: "terraform", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "kubectl", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "helm", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "docker", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "deploy", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "部署", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "lambda", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "ec2", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "s3", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "gke", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "pipeline", Target: "kiro", Strength: 3, Type: "keyword"},
			{Pattern: "infra", Target: "kiro", Strength: 3, Type: "keyword"},

			// Kiro keywords — medium (strength 2)
			{Pattern: "aws", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "gcp", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "程式碼", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "code", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "refactor", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "commit", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "push", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "git", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "yaml", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "架構", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "debug", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "fix", Target: "kiro", Strength: 2, Type: "keyword"},
			{Pattern: "修", Target: "kiro", Strength: 2, Type: "keyword"},

			// Kiro keywords — weak (strength 1)
			{Pattern: "build", Target: "kiro", Strength: 1, Type: "keyword"},
			{Pattern: "test", Target: "kiro", Strength: 1, Type: "keyword"},

			// ══════════════════════════════════════════════════════════════
			// CLI RULES (type="cli") — first-word detection for shell routing
			// ══════════════════════════════════════════════════════════════

			// Container & orchestration
			{Pattern: "kubectl", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "k", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "helm", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "docker", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "docker-compose", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "podman", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "crictl", Target: "shell", Strength: 3, Type: "cli"},

			// IaC & cloud
			{Pattern: "terraform", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "tf", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "tofu", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "aws", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "gcloud", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "az", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "sam", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "cdk", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "pulumi", Target: "shell", Strength: 3, Type: "cli"},

			// Git & dev
			{Pattern: "git", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "gh", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "glab", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "npm", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "pnpm", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "yarn", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "bun", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "cargo", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "go", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "make", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "just", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "pip", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "poetry", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "uv", Target: "shell", Strength: 3, Type: "cli"},

			// System utilities
			{Pattern: "ls", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "cat", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "grep", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "find", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "ps", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "top", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "df", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "du", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "tail", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "head", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "wc", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "ping", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "curl", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "wget", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "ssh", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "scp", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "rsync", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "chmod", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "chown", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "mkdir", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "rm", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "cp", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "mv", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "echo", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "cd", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "hostname", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "whoami", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "uname", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "sw_vers", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "date", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "which", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "lsof", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "netstat", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "ifconfig", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "ip", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "dig", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "nslookup", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "brew", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "apt", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "yum", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "htop", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "iostat", Target: "shell", Strength: 3, Type: "cli"},
			{Pattern: "vmstat", Target: "shell", Strength: 3, Type: "cli"},

			// ══════════════════════════════════════════════════════════════
			// CHAT RULES (type="chat") — greeting/social patterns
			// ══════════════════════════════════════════════════════════════

			// Chinese greetings and social
			{Pattern: "介紹", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "你好", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "嗨", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "哈囉", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "早安", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "午安", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "晚安", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "你是誰", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "你是谁", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "謝謝", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "谢谢", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "感謝", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "再見", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "掰掰", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "拜拜", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "什麼是", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "什么是", Target: "local", Strength: 3, Type: "chat"},

			// English greetings and social
			{Pattern: "hello", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "hey ", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "who are you", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "introduce yourself", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "thank you", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "thanks", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "bye", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "goodbye", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "good morning", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "good night", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "what is your name", Target: "local", Strength: 3, Type: "chat"},
			{Pattern: "tell me about yourself", Target: "local", Strength: 3, Type: "chat"},
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
