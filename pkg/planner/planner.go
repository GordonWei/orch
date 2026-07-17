package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/registry"
	"github.com/gordonwei/orch/pkg/router"
)

// Step represents a single step in an execution plan.
// DependsOn supports multiple dependencies: a step must wait for all upstream steps to complete before starting.
type Step struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Agent       string   `json:"agent"`
	Command     string   `json:"command,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	VerifyCmd   string   `json:"verify_cmd,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"` // upstream step ID list (supports DAG parallelism)
	OnFailure   string   `json:"on_failure,omitempty"` // "retry" (default), "skip", "re-plan", "abort"
}

// UnmarshalJSON provides backward compatible parsing: accepts "depends_on": "step_1" (string) or ["step_1","step_2"] (array).
func (s *Step) UnmarshalJSON(data []byte) error {
	// Use alias to avoid infinite recursion
	type stepAlias Step
	type stepRaw struct {
		stepAlias
		DependsOnRaw json.RawMessage `json:"depends_on,omitempty"`
	}

	var raw stepRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*s = Step(raw.stepAlias)

	if len(raw.DependsOnRaw) == 0 || string(raw.DependsOnRaw) == "null" {
		s.DependsOn = nil
		return nil
	}

	// Try to parse as string array
	var arr []string
	if err := json.Unmarshal(raw.DependsOnRaw, &arr); err == nil {
		s.DependsOn = arr
		return nil
	}

	// Try to parse as single string (backward compatible with old format)
	var single string
	if err := json.Unmarshal(raw.DependsOnRaw, &single); err == nil {
		if single != "" {
			s.DependsOn = []string{single}
		}
		return nil
	}

	return fmt.Errorf("depends_on: expected string or []string, got %s", string(raw.DependsOnRaw))
}

type Plan struct {
	TaskSummary string `json:"task_summary"`
	Difficulty  string `json:"difficulty"`
	Category    string `json:"category"`
	Steps       []Step `json:"steps"`
}

type Planner struct {
	registry    *registry.Registry
	cfg         *config.Config
	backendReg  *backend.Registry
	router      *router.Router
	mlxEndpoint string
	mlxModel    string
	Verbose     bool
}

func New(reg *registry.Registry, cfg *config.Config, br *backend.Registry, r *router.Router) *Planner {
	if r == nil {
		r = router.New(cfg.RouteRules)
	}
	return &Planner{
		registry:    reg,
		cfg:         cfg,
		backendReg:  br,
		router:      r,
		mlxEndpoint: cfg.LocalLLM.Endpoint,
		mlxModel:    cfg.LocalLLM.Model,
	}
}

// Router returns the planner's router instance for external use.
func (p *Planner) Router() *router.Router {
	return p.router
}

func (p *Planner) GeneratePlan(userInput string) (*Plan, error) {
	// Extract the actual user request (strip REPL session context wrapper if present)
	// Classification should only look at the current request, not prior conversation.
	classifyInput := userInput
	if idx := strings.Index(userInput, "Current request: "); idx != -1 {
		classifyInput = strings.TrimSpace(userInput[idx+len("Current request: "):])
	}

	// Layer 1: Local keyword fast routing (simple tasks get plan directly)
	// tryKeywordPlan already builds Command/Prompt from classifyInput, so no further
	// adjustment is needed here (unlike Layer 2 below, which re-injects session context).
	if plan := p.tryKeywordPlan(classifyInput); plan != nil {
		fmt.Fprintf(os.Stderr, "   ⚡ routed by: keyword match\n")
		return plan, nil
	}

	// Layer 2: MLX local LLM (Apple Silicon local inference)
	if p.mlxAvailable() {
		// Use classifyInput for classification (no session context noise)
		plan, err := p.tryMLX(classifyInput)
		if err == nil && plan != nil {
			// The 3B model's classification is a guess, not a guarantee — fixPlan catches
			// cases where it picked agent="shell" for input that isn't an actual shell
			// command (e.g. natural language it echoed verbatim into step.Command).
			plan = p.fixPlan(plan, classifyInput)
			fmt.Fprintf(os.Stderr, "   🍎 routed by: MLX local (Qwen 2.5 3B)\n")
			// But use full userInput (with session context) for the actual prompt
			if plan.Steps[0].Agent != "shell" {
				plan.Steps[0].Prompt = userInput
			}
			return plan, nil
		}
		// MLX failed, fallback to cloud
		if err != nil {
			if p.Verbose {
				fmt.Fprintf(os.Stderr, "   ⚠️  MLX failed: %v, falling back to cloud\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "   ⚠️  MLX routing failed, falling back to cloud\n")
			}
		}
	}

	// Layer 3: Cloud LLM (primary AI backend)
	primaryName := "none"
	if p.backendReg.Primary() != nil {
		primaryName = p.backendReg.PrimaryName()
	}
	fmt.Fprintf(os.Stderr, "   ☁️  routed by: %s (cloud)\n", primaryName)
	return p.tryCloud(userInput)
}

// ===== Layer 1: Keyword Match =====

func (p *Planner) tryKeywordPlan(input string) *Plan {
	lower := strings.ToLower(input)

	// Chat/greeting detection — route directly to local agent, skip MLX planning.
	// Uses the router's Classify() method (single source of truth).
	// Falls back to package-level classifyInputType if router is nil (test compat).
	var isChat bool
	if p.router != nil {
		isChat = p.router.Classify(input) == router.ClassChat
	} else {
		isChat = classifyInputType(input) == inputTypeChat
	}
	if isChat {
		return &Plan{
			TaskSummary: input,
			Difficulty:  "simple",
			Category:    "chat",
			Steps: []Step{
				{
					ID:          "step_1",
					Description: input,
					Agent:       "local",
					Prompt:      input,
				},
			},
		}
	}

	// Check configured keyword shortcuts (prefix match)
	for _, sc := range p.cfg.KeywordShortcuts {
		if strings.HasPrefix(lower, strings.ToLower(sc.Prefix)) {
			return &Plan{
				TaskSummary: input,
				Difficulty:  "simple",
				Category:    sc.Category,
				Steps: []Step{
					{
						ID:          "step_1",
						Description: input,
						Agent:       "shell",
						Command:     input,
					},
				},
			}
		}
	}

	// First-word CLI detection: if the first token is a known CLI binary, route to shell directly.
	// This catches aliases (k, tf) and CLIs not in keyword_shortcuts.
	firstWord := strings.Fields(lower)
	if len(firstWord) > 0 {
		knownCLIs := map[string]string{
			// Container & orchestration
			"kubectl": "infra", "k": "infra", "helm": "deploy", "docker": "infra",
			"docker-compose": "infra", "podman": "infra", "crictl": "infra",
			// IaC & cloud
			"terraform": "infra", "tf": "infra", "tofu": "infra",
			"aws": "query", "gcloud": "query", "az": "query",
			"sam": "deploy", "cdk": "deploy", "pulumi": "deploy",
			// Git & dev
			"git": "code", "gh": "code", "glab": "code",
			"npm": "code", "pnpm": "code", "yarn": "code", "bun": "code",
			"cargo": "code", "go": "code", "make": "code", "just": "code",
			"pip": "code", "poetry": "code", "uv": "code",
			// System
			"ls": "query", "cat": "query", "grep": "query", "find": "query",
			"ps": "query", "top": "query", "df": "query", "du": "query",
			"tail": "query", "head": "query", "wc": "query",
			"ping": "query", "curl": "query", "wget": "query",
			"ssh": "infra", "scp": "infra", "rsync": "infra",
			"chmod": "infra", "chown": "infra", "mkdir": "infra",
			"rm": "infra", "cp": "infra", "mv": "infra",
			"echo": "query", "cd": "query",
			"hostname": "query", "whoami": "query", "uname": "query",
			"sw_vers": "query", "date": "query", "which": "query",
			"lsof": "query", "netstat": "query", "ifconfig": "query",
			"ip": "query", "dig": "query", "nslookup": "query",
			"brew": "infra", "apt": "infra", "yum": "infra",
			// Monitoring
			"htop": "query", "iostat": "query", "vmstat": "query",
		}
		if category, ok := knownCLIs[firstWord[0]]; ok {
			return &Plan{
				TaskSummary: input,
				Difficulty:  "simple",
				Category:    category,
				Steps: []Step{
					{
						ID:          "step_1",
						Description: input,
						Agent:       "shell",
						Command:     input,
					},
				},
			}
		}
	}

	// API backend routing: check if route_rules strongly suggest bedrock/vertexai.
	// This allows users to configure patterns like "用 bedrock" or "bedrock 翻譯"
	// that route directly to API backends without going through MLX classification.
	if p.router != nil {
		suggested, _ := p.router.SuggestBackend(input)
		if suggested == "bedrock" || suggested == "vertexai" {
			return &Plan{
				TaskSummary: input,
				Difficulty:  "simple",
				Category:    "api",
				Steps: []Step{
					{
						ID:          "step_1",
						Description: input,
						Agent:       string(suggested),
						Prompt:      input,
					},
				},
			}
		}
	}

	return nil
}

// ===== Layer 2: MLX LM Server (OpenAI-compatible local API) =====

func (p *Planner) mlxAvailable() bool {
	// Try to ping mlx_lm.server
	resp, err := http.Get(p.mlxEndpoint + "/v1/models")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func (p *Planner) tryMLX(userInput string) (*Plan, error) {
	// Strategy: ask the small model to output ONLY a classification line.
	// Format: "agent:category" e.g., "local:chat", "kiro:infra", "claude:docs"
	// This is far more reliable than asking a 3B model to generate valid JSON.

	systemPrompt := `You are a task classifier. Given a user request, output EXACTLY one line with the format:
agent:category

AGENTS: local, kiro, claude, gemini, shell, bedrock, vertexai
CATEGORIES: chat, infra, code, docs, query, meeting, deploy, api

RULES:
- local:chat → greetings, Q&A, simple conversations, self-introduction
- shell:infra → user typed an exact CLI command (kubectl, helm, terraform, aws, gcloud, docker, git, ls, etc.)
- kiro:infra → infrastructure tasks described in natural language (AWS, GCP, kubernetes, terraform)
- kiro:code → code generation, debugging, file operations, build, test
- claude:docs → writing, analysis, meeting notes, Notion sync
- claude:query → complex questions needing deep reasoning
- gemini:docs → very long document summarization
- bedrock:api → user explicitly requests Bedrock, or task needs direct cloud model API (e.g., "用 bedrock", "bedrock 翻譯")
- vertexai:api → user explicitly requests Vertex AI, or task needs Gemini model via API (e.g., "用 vertex", "vertex 分析")

OUTPUT ONLY ONE LINE. No explanation. No JSON. No markdown. Example outputs:
local:chat
kiro:infra
shell:infra
claude:docs
bedrock:api`

	reqBody := map[string]interface{}{
		"model": p.mlxModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userInput},
		},
		"max_tokens":         20,
		"temperature":        0.05,
		"repetition_penalty": 1.2,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(p.mlxEndpoint+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mlx server unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("mlx server returned %d", resp.StatusCode)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("mlx response decode failed: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("mlx returned no choices")
	}

	raw := strings.TrimSpace(result.Choices[0].Message.Content)

	// Parse "agent:category" format
	agent, category := parseClassification(raw)

	if p.Verbose {
		fmt.Fprintf(os.Stderr, "   🔍 MLX classification raw: %q → agent=%s, category=%s\n", raw, agent, category)
	}

	// Build plan from classification
	plan := &Plan{
		TaskSummary: userInput,
		Difficulty:  "simple",
		Category:    category,
		Steps: []Step{
			{
				ID:          "step_1",
				Description: userInput,
				Agent:       agent,
				Prompt:      userInput,
			},
		},
	}

	// If agent is shell, put input as command
	if agent == "shell" {
		plan.Steps[0].Command = userInput
		plan.Steps[0].Prompt = ""
	}

	return plan, nil
}

// parseClassification extracts agent and category from MLX output.
// Expected format: "agent:category" but handles various garbage gracefully.
func parseClassification(raw string) (agent string, category string) {
	// Default fallback
	agent = "local"
	category = "chat"

	// Take only the first line
	lines := strings.Split(raw, "\n")
	first := strings.TrimSpace(lines[0])

	// Remove any surrounding quotes or backticks
	first = strings.Trim(first, "`\"'")

	// Split on colon
	parts := strings.SplitN(first, ":", 2)
	if len(parts) == 2 {
		a := strings.TrimSpace(strings.ToLower(parts[0]))
		c := strings.TrimSpace(strings.ToLower(parts[1]))

		// Validate agent
		validAgents := map[string]bool{"local": true, "kiro": true, "claude": true, "gemini": true, "shell": true}
		if validAgents[a] {
			agent = a
		}

		// Validate category
		validCategories := map[string]bool{"chat": true, "infra": true, "code": true, "docs": true, "query": true, "meeting": true, "deploy": true}
		if validCategories[c] {
			category = c
		}
	}

	return agent, category
}

// ===== Plan Post-Processing =====

// fixPlan fixes common mistakes from small models:
// 1. User input is natural language but routed to shell → reroute to claude
// 2. command contains placeholder (your-region, <xxx>) → hallucinated by small model
// 3. shell step but no command and no prompt → cannot execute
// 4. plan itself is schema copy-paste (task_summary="one line" etc.) → convert to chat
func (p *Planner) fixPlan(plan *Plan, userInput string) *Plan {
	// Detect plan-level schema copy-paste
	schemaJunk := []string{"one line", "what to do", "task description", "one line summary"}
	for _, junk := range schemaJunk {
		if strings.ToLower(plan.TaskSummary) == junk {
			// Entire plan is schema copy-paste, convert to chat
			return &Plan{
				TaskSummary: userInput,
				Difficulty:  "simple",
				Category:    "chat",
				Steps: []Step{
					{ID: "step_1", Description: userInput, Agent: "local", Prompt: userInput},
				},
			}
		}
	}
	for _, step := range plan.Steps {
		for _, junk := range schemaJunk {
			if strings.ToLower(step.Description) == junk {
				return &Plan{
					TaskSummary: userInput,
					Difficulty:  "simple",
					Category:    "chat",
					Steps: []Step{
						{ID: "step_1", Description: userInput, Agent: "local", Prompt: userInput},
					},
				}
			}
		}
	}

	var isNaturalLanguage bool
	if p.router != nil {
		isNaturalLanguage = p.router.Classify(userInput) == router.ClassNaturalLanguage
	} else {
		isNaturalLanguage = classifyInputType(userInput) == inputTypeNaturalLanguage
	}

	for i := range plan.Steps {
		step := &plan.Steps[i]

		switch step.Agent {
		case "shell", "aws", "gcloud", "kubectl", "helm", "terraform":
			if step.Command != "" {
				// If user input is natural language, it's never a real shell command —
				// reroute to claude regardless of whether the model paraphrased it or
				// (as tryMLX does) copied it verbatim into step.Command.
				if isNaturalLanguage || looksInvalid(step.Command) {
					step.Agent = "claude"
					step.Prompt = userInput
					step.Command = ""
				}
			} else if step.Prompt == "" {
				step.Agent = "claude"
				step.Prompt = userInput
			}
		}
	}

	return plan
}

// inputType represents the classification of user input.
type inputType int

const (
	inputTypeCommand         inputType = iota // Direct CLI command (should run in shell)
	inputTypeNaturalLanguage                  // Natural language task (needs AI planning)
	inputTypeChat                             // Casual chat / greeting
)

// classifyInputType determines whether input is a CLI command, a natural language task, or chat.
// This is a backward-compatible wrapper that delegates to the router package.
// It uses a package-level router instance initialized with default config for standalone usage
// (e.g., tests that call classifyInputType directly without a Planner).
func classifyInputType(input string) inputType {
	class := defaultRouter.Classify(input)
	switch class {
	case router.ClassCommand:
		return inputTypeCommand
	case router.ClassChat:
		return inputTypeChat
	default:
		return inputTypeNaturalLanguage
	}
}

// defaultRouter is a package-level router for backward compatibility with classifyInputType().
var defaultRouter = router.New(config.DefaultRouteRules())

// looksInvalid checks whether command is obviously invalid
func looksInvalid(cmd string) bool {
	lower := strings.ToLower(cmd)

	// Contains placeholder
	placeholders := []string{"your-", "<your", "${your", "example.com", "placeholder", "<region>", "<project>", "<cluster>", "only for", "for ai agents", "direct answer", "what to do", "task description"}
	for _, ph := range placeholders {
		if strings.Contains(lower, ph) {
			return true
		}
	}

	// Contains angle brackets placeholder pattern (<xxx>)
	if strings.Contains(cmd, "<") && strings.Contains(cmd, ">") {
		return true
	}

	// Contains "..." ellipsis (model copy-pasted schema)
	if strings.Contains(cmd, "...") {
		return true
	}

	// Contains explanation text in parentheses (like "(only for shell)")
	if strings.Contains(cmd, "(") && strings.Contains(cmd, ")") {
		return true
	}

	return false
}

// ===== Layer 3: Cloud LLM =====

func (p *Planner) tryCloud(userInput string) (*Plan, error) {
	b := p.backendReg.Primary()
	if b == nil {
		return nil, fmt.Errorf("no AI backend available (install kiro-cli, claude, or gemini)")
	}

	systemPrompt := p.buildSystemPrompt()
	fullPrompt := fmt.Sprintf("%s\n\nUser request:\n%s", systemPrompt, userInput)

	output, err := b.Execute(fullPrompt, "")
	if err != nil {
		return nil, fmt.Errorf("%s planning failed: %w", b.Name(), err)
	}

	raw := extractJSON(output)

	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, fmt.Errorf("cloud JSON parse failed: %w\nraw: %s", err, raw)
	}

	return &plan, nil
}

func (p *Planner) buildSystemPrompt() string {
	toolsSummary := p.registry.Summary()

	return fmt.Sprintf(`You are an AI task planner. Given a user request, analyze it and produce an execution plan as JSON.

Available tools on this machine:
%s

Working directory context: This is a multi-project workspace (Cowork/) with subprojects: AWS/, GCP/, OnPremise/, momo/, Salesforce/, Study/

Your job:
1. Classify the task: category (infra, code, docs, query, meeting, deploy) and difficulty (simple, moderate, complex)
2. Break it into steps. Each step has:
   - id: step identifier (step_1, step_2, ...)
   - description: what this step does
   - agent: which tool to use (kiro, claude, gemini, terraform, kubectl, helm, aws, gcloud, or "shell" for direct commands)
   - command: (optional) exact shell command if it's a direct tool call
   - prompt: (optional) natural language prompt if delegating to an AI agent
   - verify_cmd: (optional) command to verify success (exit 0 = pass)
   - depends_on: (optional) step_id this depends on
3. For simple queries, one step is fine. For complex tasks, break into plan → implement → verify.
4. AI agents (kiro, claude) can handle multi-step work internally. Use them for complex subtasks.
5. Prefer kiro for: code, infra, AWS, GCP, terraform, file operations, build/test
6. Prefer claude for: Notion sync, Google Workspace, meeting notes, writing
7. Prefer gemini for: very long documents (>50k tokens), video/image analysis

Respond ONLY with valid JSON matching this schema:
{
  "task_summary": "one line summary",
  "difficulty": "simple|moderate|complex",
  "category": "infra|code|docs|query|meeting|deploy",
  "steps": [...]
}`, toolsSummary)
}

// DirectChat uses local MLX to directly answer general chat (bypasses plan→execute)
func (p *Planner) DirectChat(userInput string) (string, error) {
	if !p.mlxAvailable() {
		return "", fmt.Errorf("MLX server not available for direct chat")
	}

	systemPrompt := p.cfg.Persona.SystemPrompt

	reqBody := map[string]interface{}{
		"model": p.mlxModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userInput},
		},
		"max_tokens":         2048,
		"temperature":        0.7,
		"repetition_penalty": 1.3,
		"repetition_context_size": 128,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(p.mlxEndpoint+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from MLX")
	}

	output := truncateRepetition(result.Choices[0].Message.Content)
	if output == "" {
		return "", fmt.Errorf("MLX output was entirely repetitive")
	}
	return output, nil
}

// truncateRepetition detects repetitive output from small models and truncates
// at the point where repetition begins, keeping only the valid prefix.
func truncateRepetition(s string) string {
	// Strategy 1: detect repeated words/tokens
	// Split into words, find where the same word appears N+ times consecutively
	words := strings.Fields(s)
	if len(words) < 5 {
		return s // too short to have repetition issues
	}

	// Find the first position where a word repeats 4+ times consecutively
	cutAt := -1
	for i := 0; i < len(words)-3; i++ {
		count := 1
		for j := i + 1; j < len(words); j++ {
			if words[j] == words[i] {
				count++
			} else {
				break
			}
		}
		if count >= 4 {
			cutAt = i
			break
		}
	}

	if cutAt > 0 {
		// Keep everything before the repetition
		result := strings.Join(words[:cutAt], " ")
		return strings.TrimSpace(result)
	}

	// Strategy 2: detect repeated phrases (2-5 word ngrams)
	for ngram := 2; ngram <= 5; ngram++ {
		if len(words) < ngram*3 {
			continue
		}
		for i := 0; i <= len(words)-ngram*3; i++ {
			phrase := strings.Join(words[i:i+ngram], " ")
			// Count consecutive occurrences of this phrase
			count := 1
			pos := i + ngram
			for pos+ngram <= len(words) {
				next := strings.Join(words[pos:pos+ngram], " ")
				if next == phrase {
					count++
					pos += ngram
				} else {
					break
				}
			}
			if count >= 3 {
				// Truncate at first occurrence of repeated phrase
				result := strings.Join(words[:i+ngram], " ")
				return strings.TrimSpace(result)
			}
		}
	}

	return s
}

// ===== Helpers =====

func extractJSON(s string) string {
	// Try to find ```json ... ``` wrapper
	if idx := strings.Index(s, "```json"); idx != -1 {
		s = s[idx+7:]
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
	} else if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
	}

	// Try to find first { to last }
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	}

	// Sanitize: remove lines that break JSON structure (non-ASCII garbage outside quotes)
	s = sanitizeJSON(s)

	// Fix common JSON errors from small models
	s = fixJSON(s)

	return strings.TrimSpace(s)
}

// sanitizeJSON attempts to fix common small-model JSON corruption:
// removes lines that are pure non-ASCII garbage (not part of any JSON key-value structure).
func sanitizeJSON(s string) string {
	lines := strings.Split(s, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip empty lines
		if trimmed == "" {
			cleaned = append(cleaned, line)
			continue
		}
		// If line contains a colon (key:value pair) or structural JSON chars, keep it
		if strings.Contains(trimmed, ":") || strings.ContainsAny(trimmed, "{}[],") {
			cleaned = append(cleaned, line)
			continue
		}
		// Line has no JSON structure — check if it's just garbage text
		hasNonASCII := false
		for _, r := range trimmed {
			if r > 127 {
				hasNonASCII = true
				break
			}
		}
		if hasNonASCII {
			// Pure garbage line, drop it
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

// fixJSON fixes common JSON errors produced by small models:
// - Single quotes → double quotes (outside already-double-quoted strings)
// - Trailing commas before } or ]
func fixJSON(s string) string {
	// Step 1: Replace triple quotes """ with single "
	s = strings.ReplaceAll(s, `"""`, `""`)
	// Then fix empty strings that became "" → ""  (already valid, leave it)

	// Step 2: Replace single-quoted values: 'value' → "value"
	var buf strings.Builder
	inDoubleQuote := false
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && inDoubleQuote && i+1 < len(runes) {
			buf.WriteRune(r)
			i++
			buf.WriteRune(runes[i])
			continue
		}
		if r == '"' {
			inDoubleQuote = !inDoubleQuote
			buf.WriteRune(r)
			continue
		}
		if r == '\'' && !inDoubleQuote {
			buf.WriteRune('"')
			continue
		}
		buf.WriteRune(r)
	}
	s = buf.String()

	// Step 3: Fix unquoted keys — e.g., `steps:` → `"steps":`
	// Match pattern: beginning of line, optional whitespace, bare word, colon
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip lines that already have quoted keys or are structural
		if strings.HasPrefix(trimmed, "\"") || trimmed == "{" || trimmed == "}" ||
			trimmed == "[" || trimmed == "]" || trimmed == "}," || trimmed == "]," {
			continue
		}
		// Look for `word:` or `word :` pattern at start
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx > 0 {
			key := strings.TrimSpace(trimmed[:colonIdx])
			// Only fix if key is a simple identifier (letters, digits, underscore)
			if isSimpleIdent(key) {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				rest := trimmed[colonIdx:]
				lines[i] = fmt.Sprintf("%s\"%s\"%s", indent, key, rest)
			}
		}
	}
	s = strings.Join(lines, "\n")

	// Step 4: Remove consecutive commas: ,, or , , or ,\n,
	for strings.Contains(s, ",,") {
		s = strings.ReplaceAll(s, ",,", ",")
	}
	// Remove comma followed by whitespace then another comma
	for strings.Contains(s, ", ,") {
		s = strings.ReplaceAll(s, ", ,", ",")
	}

	// Step 5: Remove trailing commas before } or ]
	for _, pair := range []struct{ old, new string }{
		{",\n}", "\n}"},
		{",\n]", "\n]"},
		{", }", " }"},
		{", ]", " ]"},
		{",}", "}"},
		{",]", "]"},
	} {
		s = strings.ReplaceAll(s, pair.old, pair.new)
	}

	// Step 6: Remove leading commas after { or [
	s = strings.ReplaceAll(s, "{\n,", "{\n")
	s = strings.ReplaceAll(s, "[\n,", "[\n")

	return s
}

// isSimpleIdent checks if a string is a valid identifier (for unquoted JSON key detection)
func isSimpleIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}
