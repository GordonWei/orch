package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Persona          Persona             `yaml:"persona"`
	LocalLLM         LocalLLM            `yaml:"local_llm"`
	Models           []ModelDef          `yaml:"models"`
	Memory           MemoryConfig        `yaml:"memory"`
	Workflows        WorkflowConfig      `yaml:"workflows"`
	Workspace        Workspace           `yaml:"workspace"`
	Routing          map[string][]string `yaml:"routing"`
	KeywordShortcuts []KeywordShortcut   `yaml:"keyword_shortcuts"`
}

// WorkflowConfig 定義工作流系統設定
type WorkflowConfig struct {
	Dir string `yaml:"dir"` // 工作流 YAML 檔案目錄，預設 ~/.config/orch/workflows/
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

// Load 讀取 config，優先順序：ORCH_CONFIG env → ~/.config/orch/config.yaml → 內建預設
func Load() *Config {
	cfg := defaultConfig()

	// 找 config 檔
	path := os.Getenv("ORCH_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "orch", "config.yaml")
	}

	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		// 找不到 config，用預設
		return cfg
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		// parse 失敗，用預設
		return cfg
	}

	// 展開 ~ 路徑
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
			Owner:    "Gordon Wei",
			Language: "zh-TW",
			SystemPrompt: `你是 orch，一個運行在 Mac 上的 AI 幕僚長 CLI 工具。
你的主人是 Gordon Wei（momo 基礎架構部部長）。
你負責協調 kiro、claude、gemini 等多個 AI agent 完成任務。

回覆規則：
- 一律使用繁體中文（zh-TW）回覆
- 簡短直接，不廢話
- 技術術語維持英文（Kubernetes、Terraform、GKE 等）
- 程式碼、指令維持英文`,
		},
		LocalLLM: LocalLLM{
			Endpoint:   "http://localhost:8080",
			Model:      "mlx-community/Qwen2.5-1.5B-Instruct-4bit",
			PythonPath: filepath.Join(home, "mlx-env", "bin", "python3"),
			AutoStart:  true,
		},
		Models: []ModelDef{
			{
				Name:       "qwen-1.5b",
				Backend:    "mlx",
				Endpoint:   "http://localhost:8080",
				Model:      "mlx-community/Qwen2.5-1.5B-Instruct-4bit",
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
			Root: filepath.Join(home, "Desktop", "Cowork"),
			Subdirs: []Subdir{
				{Name: "AWS", Keywords: []string{"aws", "lambda", "s3", "ec2", "sam", "bedrock", "cloudformation"}},
				{Name: "GCP", Keywords: []string{"gcp", "gke", "litellm", "cloud run", "bigquery", "gcloud"}},
				{Name: "OnPremise", Keywords: []string{"on-premise", "onpremise", "rke2", "metallb", "gitea", "機房", "gateway api"}},
				{Name: "momo", Keywords: []string{"momo", "週報", "大促", "績效", "okr", "1-on-1", "課長"}},
				{Name: "Salesforce", Keywords: []string{"salesforce", "網銀", "銀行", "客戶提案", "lucy"}},
				{Name: "Study", Keywords: []string{"study", "筆記", "學習", "orchestrator"}},
			},
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
