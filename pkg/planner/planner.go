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
	mlxEndpoint string
	mlxModel    string
}

func New(reg *registry.Registry, cfg *config.Config, br *backend.Registry) *Planner {
	return &Planner{
		registry:    reg,
		cfg:         cfg,
		backendReg:  br,
		mlxEndpoint: cfg.LocalLLM.Endpoint,
		mlxModel:    cfg.LocalLLM.Model,
	}
}

func (p *Planner) GeneratePlan(userInput string) (*Plan, error) {
	// Layer 1: Local keyword fast routing (simple tasks get plan directly)
	if plan := p.tryKeywordPlan(userInput); plan != nil {
		fmt.Fprintf(os.Stderr, "   ⚡ routed by: keyword match\n")
		return plan, nil
	}

	// Layer 2: MLX local LLM (Apple Silicon local inference)
	if p.mlxAvailable() {
		plan, err := p.tryMLX(userInput)
		if err == nil && plan != nil {
			fmt.Fprintf(os.Stderr, "   🍎 routed by: MLX local (Qwen 2.5 3B)\n")
			return plan, nil
		}
		// MLX failed, fallback to cloud
		if err != nil {
			fmt.Fprintf(os.Stderr, "   ⚠️  MLX failed: %v, falling back to cloud\n", err)
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
	toolsSummary := p.registry.Summary()

	systemPrompt := fmt.Sprintf(`You are a task router. Given a user request, output a JSON execution plan.

Available tools: %s

RULES:
1. You are ONLY a router. Do NOT invent shell commands.
2. If the user typed an exact CLI command (kubectl, helm, terraform, aws, gcloud, ls, etc.) → agent "shell", put user input in "command".
3. If the user asks a question or wants information → agent "local", category "chat", put answer in "prompt".
4. For tasks needing AI reasoning → agent "claude" with a "prompt" describing the task.
5. Never put text in "command" unless it is a real executable shell command.
6. "verify_cmd" must be empty string unless you have a real verification command.

AGENT SELECTION:
- claude: complex tasks, Notion, writing, analysis
- kiro: code, infra, AWS, GCP, terraform, kubernetes
- gemini: very long documents, video, image
- shell: user typed an exact command to run
- local: simple Q&A, chat, greetings

OUTPUT FORMAT (valid JSON only, no markdown):
{"task_summary":"one line","difficulty":"simple","category":"infra","steps":[{"id":"step_1","description":"what to do","agent":"claude","prompt":"task description","command":"","verify_cmd":""}]}`, toolsSummary)

	reqBody := map[string]interface{}{
		"model": p.mlxModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userInput},
		},
		"max_tokens":  1024,
		"temperature": 0.1,
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

	raw := extractJSON(result.Choices[0].Message.Content)

	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, fmt.Errorf("mlx JSON parse failed: %w\nraw: %s", err, raw)
	}

	if len(plan.Steps) == 0 {
		return nil, fmt.Errorf("mlx returned plan with no steps")
	}

	// Post-processing: fix common mistakes from small models
	// If shell step's command is not the user's original input, reroute to kiro
	plan = *p.fixPlan(&plan, userInput)

	return &plan, nil
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

	isNaturalLanguage := looksLikeNaturalLanguage(userInput)

	for i := range plan.Steps {
		step := &plan.Steps[i]

		switch step.Agent {
		case "shell", "aws", "gcloud", "kubectl", "helm", "terraform":
			if step.Command != "" {
				// If user input is natural language but small model tried to generate command → reroute to claude
				if isNaturalLanguage && step.Command != userInput {
					step.Agent = "claude"
					step.Prompt = userInput
					step.Command = ""
				} else if looksInvalid(step.Command) {
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

// looksLikeNaturalLanguage checks whether input is natural language (not a direct CLI command)
func looksLikeNaturalLanguage(input string) bool {
	trimmed := strings.TrimSpace(input)

	// Contains Chinese characters → natural language
	for _, r := range trimmed {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}

	// Starts with common CLI tool → not natural language
	cliPrefixes := []string{"kubectl", "helm", "terraform", "tf", "aws", "gcloud", "docker", "git", "ls", "cat", "echo", "hostname", "ping", "curl", "wget", "ssh", "scp", "cd", "mkdir", "rm", "cp", "mv", "grep", "find", "ps", "top", "df", "du", "ifconfig", "ip ", "sw_vers", "uname", "whoami", "date", "which", "brew"}
	lower := strings.ToLower(trimmed)
	for _, prefix := range cliPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}

	// Contains multiple spaces (sentence) → natural language
	if strings.Count(trimmed, " ") >= 3 {
		return true
	}

	// Starts with verb in English (help, list, show, check, find, get...) → likely natural language
	nlVerbs := []string{"help", "list", "show", "check", "find", "get ", "tell", "what", "how", "why", "can ", "please", "幫", "查", "列", "看", "找"}
	for _, v := range nlVerbs {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}

	return false
}

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
		"max_tokens":  2048,
		"temperature": 0.7,
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

	return result.Choices[0].Message.Content, nil
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

	return strings.TrimSpace(s)
}
