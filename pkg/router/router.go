// Package router provides unified routing logic for the orch orchestrator.
// It consolidates keyword→backend hints, CLI detection, and chat pattern matching
// into a single configurable, thread-safe component backed by config.RouteRulesConfig.
package router

import (
	"strings"
	"sync"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/session"
)

// InputClass represents the classification of user input.
type InputClass int

const (
	ClassCommand         InputClass = iota // Direct CLI command (should run in shell)
	ClassNaturalLanguage                   // Natural language task (needs AI planning)
	ClassChat                              // Casual chat / greeting
)

// HintResult contains the result of a route hint evaluation.
type HintResult struct {
	Suggested session.Backend // recommended backend (empty if no hint)
	Keyword   string          // the matched pattern
	Reason    string          // human-readable reason for the hint
	Strength  int             // match strength (1=weak, 2=medium, 3=strong)
}

// historyEntry records a single input and the backend it was routed to.
type historyEntry struct {
	Input   string
	Backend session.Backend
}

// Router provides thread-safe, config-driven routing decisions.
// It replaces both RouteHinter (route_hint.go) and classifyInputType (planner.go).
type Router struct {
	cfg          config.RouteRulesConfig
	inputCounter int
	lastHintAt   int
	history      []historyEntry
	mu           sync.Mutex
}

// New creates a Router from the given configuration.
func New(cfg config.RouteRulesConfig) *Router {
	histSize := cfg.HistorySize
	if histSize <= 0 {
		histSize = 5
	}
	return &Router{
		cfg:        cfg,
		lastHintAt: -cfg.Cooldown, // allow hint on first input
		history:    make([]historyEntry, 0, histSize),
	}
}

// Classify determines whether input is a CLI command, natural language, or chat.
// It uses type="cli" rules for command detection and type="chat" for chat patterns,
// with the same heuristics previously in classifyInputType().
func (r *Router) Classify(input string) InputClass {
	trimmed := strings.TrimSpace(input)
	lower := strings.ToLower(trimmed)

	// Step 1: Check if it's a direct CLI command (starts with known binary).
	// Uses type="cli" rules from config — first-word matching only.
	fields := strings.Fields(lower)
	if len(fields) > 0 {
		firstWord := fields[0]
		for _, rule := range r.cfg.Rules {
			if rule.Type != "cli" {
				continue
			}
			if firstWord == rule.Pattern {
				return ClassCommand
			}
		}
		// Path-based CLI prefixes
		cliPrefixes := []string{"./", "/", "sudo "}
		for _, prefix := range cliPrefixes {
			if strings.HasPrefix(lower, prefix) {
				return ClassCommand
			}
		}
	}

	// Step 2: Technical keywords → it's a task, not chat.
	// These override chat detection when present.
	techIndicators := []string{
		// Infrastructure & cloud
		"kubectl", "helm", "terraform", "aws", "gcloud", "docker", "k8s", "kubernetes",
		"gke", "eks", "ecs", "s3", "ec2", "lambda", "cloudformation", "sam ",
		"cloud run", "bigquery", "vpc", "subnet", "firewall", "load balancer",
		"ingress", "gateway", "metallb", "rke2", "pod", "deploy", "namespace",
		"node", "cluster", "service", "service mesh", "istio", "envoy",
		// Code & dev tools
		"git", "npm", "pnpm", "yarn", "make", "cargo", "pip", "go build", "go test",
		"compile", "build", "test", "debug", "lint", "refactor",
		"function", "class", "struct", "interface", "endpoint", "api",
		// Files & system
		"file", "directory", "folder", "path", "config", "yaml", "json", "log",
		"error", "fix", "bug", "issue", "merge", "branch",
		// Specific tools
		"notion", "slack", "jira", "confluence",
		"litellm", "backstage", "grafana", "prometheus",
		// Action verbs that indicate work
		"整理", "部署", "同步", "查詢", "分析", "修正", "更新", "刪除", "建立",
		"設定", "檢查", "監控", "備份", "還原", "執行", "啟動", "停止",
		"plan", "apply",
	}
	for _, kw := range techIndicators {
		if strings.Contains(lower, kw) {
			return ClassNaturalLanguage
		}
	}

	// Step 3: Check chat patterns from config (type="chat" rules).
	for _, rule := range r.cfg.Rules {
		if rule.Type != "chat" {
			continue
		}
		if strings.Contains(lower, rule.Pattern) {
			return ClassChat
		}
	}
	// "hi" needs prefix match to avoid false positives like "sushi", "this"
	if strings.HasPrefix(lower, "hi ") || lower == "hi" {
		return ClassChat
	}

	// Step 4: Contains Chinese characters → check length
	hasChinese := false
	for _, r := range trimmed {
		if r >= 0x4e00 && r <= 0x9fff {
			hasChinese = true
			break
		}
	}
	if hasChinese {
		if len([]rune(trimmed)) <= 10 {
			return ClassChat
		}
		return ClassNaturalLanguage
	}

	// Step 5: English — sentence structure indicates NL
	if strings.Count(trimmed, " ") >= 3 {
		return ClassNaturalLanguage
	}

	// Step 6: Starts with NL verb
	nlVerbs := []string{"help", "list", "show", "check", "find", "get ", "tell", "what", "how", "why", "can ", "summarize", "analyze", "create", "write", "generate"}
	for _, v := range nlVerbs {
		if strings.HasPrefix(lower, v) {
			return ClassNaturalLanguage
		}
	}

	// Default: short → chat, long → NL
	if len(trimmed) < 15 {
		return ClassChat
	}
	return ClassNaturalLanguage
}

// Hint evaluates the input against phrase/keyword rules and returns a suggestion if:
//   - The matched rule targets a DIFFERENT backend than currentBackend
//   - The match strength is >= 2 (medium or strong)
//   - The cooldown period has elapsed since the last hint
//
// Returns an empty HintResult if no hint should be shown.
func (r *Router) Hint(input string, currentBackend session.Backend) HintResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.inputCounter++
	lower := strings.ToLower(input)

	// Find the best (highest strength) matching rule for the OTHER backend.
	// Only consider phrase and keyword rules (not cli/chat).
	var bestRule *config.RouteRule
	for i := range r.cfg.Rules {
		rule := &r.cfg.Rules[i]

		// Only phrase and keyword rules participate in hints
		if rule.Type != "phrase" && rule.Type != "keyword" {
			continue
		}

		// Only consider rules targeting a different backend
		if session.Backend(rule.Target) == currentBackend {
			continue
		}

		// Only suggest for medium or strong signals
		if rule.Strength < 2 {
			continue
		}

		// Check if pattern matches (substring match)
		if !strings.Contains(lower, rule.Pattern) {
			continue
		}

		// Keep the highest-strength match (phrases are listed first, so
		// for equal strength the first/longest match wins)
		if bestRule == nil || rule.Strength > bestRule.Strength {
			bestRule = rule
		}
	}

	if bestRule == nil {
		return HintResult{}
	}

	// Check cooldown
	if r.inputCounter-r.lastHintAt < r.cfg.Cooldown {
		return HintResult{}
	}

	// Valid hint — record and return
	r.lastHintAt = r.inputCounter
	reason := buildHintReason(bestRule, currentBackend)

	return HintResult{
		Suggested: session.Backend(bestRule.Target),
		Keyword:   bestRule.Pattern,
		Reason:    reason,
		Strength:  bestRule.Strength,
	}
}

// SuggestBackend returns the best backend for the given input based on keyword/phrase
// match strength combined with history momentum. Returns empty backend and reason if
// no strong suggestion can be made.
func (r *Router) SuggestBackend(input string) (session.Backend, string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	lower := strings.ToLower(input)

	// Score each backend based on matched rules
	scores := map[session.Backend]int{}
	var bestKeyword string
	var bestBackend session.Backend

	for i := range r.cfg.Rules {
		rule := &r.cfg.Rules[i]
		// Only phrase and keyword rules
		if rule.Type != "phrase" && rule.Type != "keyword" {
			continue
		}
		if !strings.Contains(lower, rule.Pattern) {
			continue
		}
		target := session.Backend(rule.Target)
		scores[target] += rule.Strength
		if scores[target] > scores[bestBackend] {
			bestBackend = target
			bestKeyword = rule.Pattern
		}
	}

	// Apply history momentum: if recent inputs consistently route to one backend,
	// boost that backend's score for ambiguous inputs.
	if momentum := r.historyMomentum(); momentum != "" {
		// Only apply momentum if current match is weak/ambiguous
		// (top score <= 2, or tied between backends)
		topScore := scores[bestBackend]
		if topScore <= 2 {
			scores[momentum] += 2
			if scores[momentum] > topScore {
				bestBackend = momentum
				bestKeyword = "(history momentum)"
			}
		}
	}

	if bestBackend == "" {
		return "", ""
	}

	return bestBackend, bestKeyword
}

// RecordInput records the input and its matched backend for context-aware history analysis.
func (r *Router) RecordInput(input string, backend session.Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()

	histSize := r.cfg.HistorySize
	if histSize <= 0 {
		histSize = 5
	}

	r.history = append(r.history, historyEntry{
		Input:   input,
		Backend: backend,
	})

	// Trim to sliding window
	if len(r.history) > histSize {
		r.history = r.history[len(r.history)-histSize:]
	}
}

// historyMomentum returns the dominant backend if >= 3 of the last N entries
// went to the same backend, indicating a sustained work context.
// Must be called with r.mu held.
//
// Counts are accumulated in first-seen order (not Go map iteration order) so
// that if history_size is configured above the default 5, allowing two
// backends to independently reach the threshold within the same window, the
// result is deterministic rather than picking a random "winner" per call.
func (r *Router) historyMomentum() session.Backend {
	if len(r.history) < 3 {
		return ""
	}

	const threshold = 3

	counts := map[session.Backend]int{}
	order := make([]session.Backend, 0, len(r.history))
	for _, entry := range r.history {
		if _, seen := counts[entry.Backend]; !seen {
			order = append(order, entry.Backend)
		}
		counts[entry.Backend]++
	}

	for _, backend := range order {
		if counts[backend] >= threshold {
			return backend
		}
	}
	return ""
}

// buildHintReason generates a contextual message explaining WHY the hint is given.
func buildHintReason(rule *config.RouteRule, current session.Backend) string {
	var targetName, currentName string

	switch session.Backend(rule.Target) {
	case session.BackendClaude:
		targetName = "Claude (Victoria)"
	case session.BackendKiro:
		targetName = "Kiro"
	default:
		targetName = rule.Target
	}

	switch current {
	case session.BackendClaude:
		currentName = "Claude"
	case session.BackendKiro:
		currentName = "Kiro"
	default:
		currentName = string(current)
	}

	var strengthLabel string
	switch rule.Strength {
	case 3:
		strengthLabel = "強烈建議"
	case 2:
		strengthLabel = "建議"
	default:
		strengthLabel = "可能"
	}

	var domain string
	switch session.Backend(rule.Target) {
	case session.BackendClaude:
		domain = "Notion/文件/管理"
	case session.BackendKiro:
		domain = "基礎設施/程式碼/部署"
	default:
		domain = rule.Target
	}

	return strengthLabel + "切換至 " + targetName + "：偵測到「" + rule.Pattern +
		"」屬於" + domain + "領域，" + targetName + " 比 " + currentName + " 更適合處理。"
}

// AutoRoute returns whether auto-routing is currently enabled.
func (r *Router) AutoRoute() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.AutoRoute
}

// SetAutoRoute enables or disables auto-routing at runtime.
func (r *Router) SetAutoRoute(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.AutoRoute = enabled
}
