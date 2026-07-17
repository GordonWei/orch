package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// IsASCIIOnly reports whether s contains only ASCII bytes.
func IsASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// isAlnum reports whether b is an ASCII letter or digit.
func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// ContainsWholeWord reports whether needle appears in haystack with
// non-alphanumeric (or string-boundary) characters on both sides — it still
// matches "status" inside "check cluster status", but not inside
// "statusbar" or "gitstatus". Only meaningful for ASCII needles/haystacks
// (CJK text has no reliable word-boundary concept); callers should guard
// with IsASCIIOnly first and fall back to plain strings.Contains otherwise.
func ContainsWholeWord(haystack, needle string) bool {
	start := 0
	for {
		idx := strings.Index(haystack[start:], needle)
		if idx == -1 {
			return false
		}
		idx += start

		beforeOK := idx == 0 || !isAlnum(haystack[idx-1])
		afterIdx := idx + len(needle)
		afterOK := afterIdx >= len(haystack) || !isAlnum(haystack[afterIdx])

		if beforeOK && afterOK {
			return true
		}
		start = idx + 1
	}
}

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
	Cooldown    int         `yaml:"cooldown"`     // min inputs between hints
	AutoRoute   bool        `yaml:"auto_route"`   // auto-spawn+switch
	HistorySize int         `yaml:"history_size"` // context-aware window
	// ChatShortInputMaxLen: fast path that treats Chinese input at or under this
	// many runes as chat without going through MLX classification. Defaults to 1
	// (DefaultRouteRules) — just enough to catch bare acknowledgments like "好"
	// without also catching short task references like "那讀交接". Set to 0 to
	// disable entirely, or raise it back toward the old unconditional behavior
	// (previously hardcoded at 10) if you want more short input to skip MLX.
	ChatShortInputMaxLen int `yaml:"chat_short_input_max_len"`
}

type Config struct {
	Persona          Persona           `yaml:"persona"`
	AIBackend        AIBackendConfig   `yaml:"ai_backend"`
	LocalLLM         LocalLLM          `yaml:"local_llm"`
	Models           []ModelDef        `yaml:"models"`
	Memory           MemoryConfig      `yaml:"memory"`
	Workflows        WorkflowConfig    `yaml:"workflows"`
	Workspace        Workspace         `yaml:"workspace"`
	KeywordShortcuts []KeywordShortcut `yaml:"keyword_shortcuts"`
	RouteRules       RouteRulesConfig  `yaml:"route_rules"`

	// HighRiskPatterns lists substrings that, when found in a shell step's
	// command, trigger the executor's approval gate (see pkg/executor).
	// Ships with a sensible default list (see defaultConfig); set this key
	// in config.yaml to add or replace patterns without a rebuild.
	HighRiskPatterns []string          `yaml:"high_risk_patterns"`
	APIBackends      APIBackendsConfig `yaml:"api_backends"`
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

// APIBackendsConfig holds configuration for external API backends (Bedrock, VertexAI).
type APIBackendsConfig struct {
	Bedrock  BedrockAPIConfig  `yaml:"bedrock"`
	VertexAI VertexAIAPIConfig `yaml:"vertexai"`
}

// BedrockAPIConfig holds AWS Bedrock API settings.
type BedrockAPIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Region  string `yaml:"region"`
	ModelID string `yaml:"model_id"`
}

// VertexAIAPIConfig holds Google Vertex AI API settings.
type VertexAIAPIConfig struct {
	Enabled   bool   `yaml:"enabled"`
	ProjectID string `yaml:"project_id"`
	Region    string `yaml:"region"`
	ModelID   string `yaml:"model_id"`
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
	Name       string `yaml:"name"`                  // display name, e.g., "qwen-3b", "llama-8b"
	Backend    string `yaml:"backend"`               // "mlx", "ollama", "openai-compatible"
	Endpoint   string `yaml:"endpoint"`              // API endpoint URL
	Model      string `yaml:"model"`                 // model identifier for the API
	PythonPath string `yaml:"python_path,omitempty"` // only for mlx backend
	AutoStart  bool   `yaml:"auto_start,omitempty"`
	Port       string `yaml:"port,omitempty"`
	Default    bool   `yaml:"default,omitempty"` // which one to use by default
}

// MemoryConfig defines the persistence layer settings.
type MemoryConfig struct {
	DBPath         string `yaml:"db_path"`          // path to orch.db, default ~/.config/orch/orch.db
	BriefingOnBoot bool   `yaml:"briefing_on_boot"` // load briefing into context on start
	AutoSummarize  bool   `yaml:"auto_summarize"`   // reserved, currently unused — no code path reads this field yet; use BriefingSourceFile instead
	HistoryLimit   int    `yaml:"history_limit"`    // max rows before pruning (0=unlimited)
	// BriefingSourceFile: optional path to a status/handoff document (e.g. a
	// project dashboard you maintain by hand). When set, briefing_on_boot reads
	// and re-summarizes this file fresh on every startup instead of showing
	// whatever was last saved via `orch briefing gen` (which only reflects
	// orch's own task history, not external project state, and goes stale the
	// moment you stop manually re-running it). Not set by default — this is a
	// per-user workflow choice, not something orch assumes everyone has.
	BriefingSourceFile string `yaml:"briefing_source_file,omitempty"`
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
	cfg.Memory.BriefingSourceFile = expandHome(cfg.Memory.BriefingSourceFile)
	cfg.Workflows.Dir = expandHome(cfg.Workflows.Dir)
	for i := range cfg.Models {
		cfg.Models[i].PythonPath = expandHome(cfg.Models[i].PythonPath)
	}

	mergeDefaultRouteRules(cfg)

	return cfg
}

// mergeDefaultRouteRules restores built-in route_rules that a user's config.yaml
// doesn't already define. YAML unmarshaling replaces slices wholesale (never
// merges) when the source document has the key — so a config.yaml with even one
// custom rule under route_rules.rules silently discarded the ~200 built-in
// cli/chat/gemini rules, despite the config.yaml template's own comment claiming
// "they MERGE with defaults (do not override them)". That comment was aspirational
// until this function existed: real-world discovery was a user's config with 4
// custom bedrock/vertexai phrase rules losing every built-in chat greeting
// pattern, silently degrading "你好" and friends to a length-based guess instead
// of a precise match. Keyed on pattern+type so a genuine override (same
// pattern+type, different target/strength) is respected rather than duplicated;
// a no-op when the user's config doesn't touch route_rules.rules at all, since
// cfg.RouteRules.Rules already equals the defaults from defaultConfig() in that case.
func mergeDefaultRouteRules(cfg *Config) {
	existing := make(map[string]bool, len(cfg.RouteRules.Rules))
	for _, r := range cfg.RouteRules.Rules {
		existing[r.Pattern+"|"+r.Type] = true
	}
	for _, def := range DefaultRouteRules().Rules {
		if !existing[def.Pattern+"|"+def.Type] {
			cfg.RouteRules.Rules = append(cfg.RouteRules.Rules, def)
		}
	}
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
		RouteRules:       DefaultRouteRules(),
		HighRiskPatterns: DefaultHighRiskPatterns(),
		APIBackends: APIBackendsConfig{
			Bedrock: BedrockAPIConfig{
				Enabled: false,
				Region:  "us-east-1",
				ModelID: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
			},
			VertexAI: VertexAIAPIConfig{
				Enabled:   false,
				ProjectID: "",
				Region:    "us-central1",
				ModelID:   "gemini-2.5-flash",
			},
		},
	}
}

// DefaultHighRiskPatterns returns the built-in list of substrings that
// trigger the executor's approval gate. Set high_risk_patterns in
// config.yaml to override this list entirely (yaml.Unmarshal replaces the
// whole slice, same merge behavior as route_rules). Exported so callers that
// construct a Config without going through Load() (e.g. tests) can still
// get a sane fallback.
func DefaultHighRiskPatterns() []string {
	return []string{
		// Terraform destructive
		"terraform apply",
		"terraform destroy",
		"tf apply",
		"tf destroy",
		"tofu apply",
		"tofu destroy",
		// Kubernetes destructive
		"kubectl delete",
		"kubectl drain",
		"kubectl cordon",
		// File system destructive
		"rm -rf",
		"rm -r",
		"rmdir",
		// Git destructive
		"git push --force",
		"git push -f",
		"git reset --hard",
		"git clean -f",
		// Docker destructive
		"docker rm",
		"docker rmi",
		"docker system prune",
		// Helm destructive
		"helm uninstall",
		"helm delete",
		// AWS destructive
		"aws s3 rm",
		"aws ec2 terminate",
		"aws rds delete",
		"aws cloudformation delete",
		// GCP destructive
		"gcloud compute instances delete",
		"gcloud container clusters delete",
		"gcloud sql instances delete",
		// Database
		"drop database",
		"drop table",
		"truncate table",
	}
}

// DefaultRouteRules returns the full set of routing rules migrated from
// route_hint.go (phrase + keyword rules) and classifyInputType (cli + chat rules).
// Exported for use by tests that need a hermetic fixture (no file system access).
func DefaultRouteRules() RouteRulesConfig {
	return RouteRulesConfig{
		Cooldown:    3,
		AutoRoute:   false,
		HistorySize: 5,
		// 1 rune: catches bare acknowledgments like "好" without also catching
		// short task references like "那讀交接" (4 runes) or "繼續" (2 runes) —
		// see router.Classify() for the full rationale.
		ChatShortInputMaxLen: 1,
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

			// Gemini phrases
			{Pattern: "用 gemini", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "交給 gemini", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "summarize this", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "摘要這", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "幫我摘要", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "google drive", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "google docs", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "google sheets", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "analyze image", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "analyze video", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "看這張圖", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "看這個影片", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "分析這張", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "long context", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "長文分析", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "深度分析", Target: "gemini", Strength: 3, Type: "phrase"},

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

			// Gemini keywords — strong (strength 3)
			{Pattern: "gemini", Target: "gemini", Strength: 3, Type: "keyword"},
			// "drive" alone is too ambiguous as a keyword (hard drive, test drive,
			// disk drive, sales drive are all common phrases unrelated to Google
			// Drive) — word-boundary matching alone doesn't fix this since "drive"
			// really is a standalone word in those phrases too. Use the specific
			// "google drive" phrase instead.
			{Pattern: "google drive", Target: "gemini", Strength: 3, Type: "phrase"},
			{Pattern: "研究", Target: "gemini", Strength: 3, Type: "keyword"},

			// Gemini keywords — medium (strength 2)
			{Pattern: "summarize", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "摘要", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "影片", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "video", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "image", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "圖片", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "pdf", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "research", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "調研", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "評估", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "比較", Target: "gemini", Strength: 2, Type: "keyword"},
			{Pattern: "compare", Target: "gemini", Strength: 2, Type: "keyword"},

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
