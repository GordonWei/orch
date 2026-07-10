package main

import (
	"strings"
	"sync"

	"github.com/gordonwei/orch/pkg/session"
)

// routeRule defines a single keyword/phrase → backend mapping with a strength signal.
type routeRule struct {
	Pattern  string          // keyword or phrase (lowercase for matching)
	Target   session.Backend // which backend this pattern belongs to
	Strength int             // 1=weak, 2=medium, 3=strong
}

// RouteHinter provides intelligent route suggestions with phrase priority,
// confidence filtering, and cooldown to avoid nagging.
type RouteHinter struct {
	rules         []routeRule
	inputCounter  int // total inputs processed
	lastHintInput int // input counter when last hint was given
	cooldown      int // minimum inputs between hints
	mu            sync.Mutex
}

// NewRouteHinter creates a RouteHinter with all default rules.
// Rules are ordered: multi-word phrases first, then single keywords.
// This ensures phrases like "會議記錄" match before "會" alone.
func NewRouteHinter() *RouteHinter {
	return &RouteHinter{
		rules: []routeRule{
			// ══════════════════════════════════════════════════════════════
			// CLAUDE (Victoria) — Multi-word phrases (check first)
			// ══════════════════════════════════════════════════════════════
			{Pattern: "notion page", Target: session.BackendClaude, Strength: 3},
			{Pattern: "meeting notes", Target: session.BackendClaude, Strength: 3},
			{Pattern: "會議記錄", Target: session.BackendClaude, Strength: 3},
			{Pattern: "分析報告", Target: session.BackendClaude, Strength: 3},
			{Pattern: "同步 notion", Target: session.BackendClaude, Strength: 3},
			{Pattern: "推 notion", Target: session.BackendClaude, Strength: 3},
			{Pattern: "寫交接", Target: session.BackendClaude, Strength: 3},
			{Pattern: "更新交接", Target: session.BackendClaude, Strength: 3},
			{Pattern: "客戶提案", Target: session.BackendClaude, Strength: 3},
			{Pattern: "數位銀行", Target: session.BackendClaude, Strength: 3},
			{Pattern: "google calendar", Target: session.BackendClaude, Strength: 3},

			// ══════════════════════════════════════════════════════════════
			// KIRO — Multi-word phrases (check first)
			// ══════════════════════════════════════════════════════════════
			{Pattern: "terraform plan", Target: session.BackendKiro, Strength: 3},
			{Pattern: "terraform apply", Target: session.BackendKiro, Strength: 3},
			{Pattern: "terraform init", Target: session.BackendKiro, Strength: 3},
			{Pattern: "kubectl apply", Target: session.BackendKiro, Strength: 3},
			{Pattern: "kubectl get", Target: session.BackendKiro, Strength: 3},
			{Pattern: "docker build", Target: session.BackendKiro, Strength: 3},
			{Pattern: "docker compose", Target: session.BackendKiro, Strength: 3},
			{Pattern: "helm install", Target: session.BackendKiro, Strength: 3},
			{Pattern: "helm upgrade", Target: session.BackendKiro, Strength: 3},
			{Pattern: "git push", Target: session.BackendKiro, Strength: 3},
			{Pattern: "git commit", Target: session.BackendKiro, Strength: 3},
			{Pattern: "cloud run", Target: session.BackendKiro, Strength: 3},
			{Pattern: "ci/cd", Target: session.BackendKiro, Strength: 3},
			{Pattern: "設定檔", Target: session.BackendKiro, Strength: 2},

			// ══════════════════════════════════════════════════════════════
			// CLAUDE (Victoria) — Single keywords
			// ══════════════════════════════════════════════════════════════

			// Strong signals (strength 3) — clearly Claude's domain
			{Pattern: "notion", Target: session.BackendClaude, Strength: 3},
			{Pattern: "gcal", Target: session.BackendClaude, Strength: 3},
			{Pattern: "gmail", Target: session.BackendClaude, Strength: 3},
			{Pattern: "salesforce", Target: session.BackendClaude, Strength: 3},
			{Pattern: "podcast", Target: session.BackendClaude, Strength: 3},
			{Pattern: "notebooklm", Target: session.BackendClaude, Strength: 3},

			// Medium signals (strength 2) — likely Claude
			{Pattern: "calendar", Target: session.BackendClaude, Strength: 2},
			{Pattern: "pptx", Target: session.BackendClaude, Strength: 2},
			{Pattern: "簡報", Target: session.BackendClaude, Strength: 2},
			{Pattern: "週報", Target: session.BackendClaude, Strength: 2},
			{Pattern: "月報", Target: session.BackendClaude, Strength: 2},
			{Pattern: "報表", Target: session.BackendClaude, Strength: 2},
			{Pattern: "交接", Target: session.BackendClaude, Strength: 2},
			{Pattern: "同步", Target: session.BackendClaude, Strength: 2},
			{Pattern: "整理", Target: session.BackendClaude, Strength: 2},
			{Pattern: "客戶", Target: session.BackendClaude, Strength: 2},
			{Pattern: "銀行", Target: session.BackendClaude, Strength: 2},
			{Pattern: "提案", Target: session.BackendClaude, Strength: 2},
			{Pattern: "writing", Target: session.BackendClaude, Strength: 2},

			// Weak signals (strength 1) — could be either
			{Pattern: "會議", Target: session.BackendClaude, Strength: 1},
			{Pattern: "筆記", Target: session.BackendClaude, Strength: 1},

			// ══════════════════════════════════════════════════════════════
			// KIRO — Single keywords
			// ══════════════════════════════════════════════════════════════

			// Strong signals (strength 3) — clearly Kiro's domain
			{Pattern: "terraform", Target: session.BackendKiro, Strength: 3},
			{Pattern: "kubectl", Target: session.BackendKiro, Strength: 3},
			{Pattern: "helm", Target: session.BackendKiro, Strength: 3},
			{Pattern: "docker", Target: session.BackendKiro, Strength: 3},
			{Pattern: "deploy", Target: session.BackendKiro, Strength: 3},
			{Pattern: "部署", Target: session.BackendKiro, Strength: 3},
			{Pattern: "lambda", Target: session.BackendKiro, Strength: 3},
			{Pattern: "ec2", Target: session.BackendKiro, Strength: 3},
			{Pattern: "s3", Target: session.BackendKiro, Strength: 3},
			{Pattern: "gke", Target: session.BackendKiro, Strength: 3},
			{Pattern: "pipeline", Target: session.BackendKiro, Strength: 3},
			{Pattern: "infra", Target: session.BackendKiro, Strength: 3},

			// Medium signals (strength 2) — likely Kiro
			{Pattern: "aws", Target: session.BackendKiro, Strength: 2},
			{Pattern: "gcp", Target: session.BackendKiro, Strength: 2},
			{Pattern: "程式碼", Target: session.BackendKiro, Strength: 2},
			{Pattern: "code", Target: session.BackendKiro, Strength: 2},
			{Pattern: "refactor", Target: session.BackendKiro, Strength: 2},
			{Pattern: "commit", Target: session.BackendKiro, Strength: 2},
			{Pattern: "push", Target: session.BackendKiro, Strength: 2},
			{Pattern: "git", Target: session.BackendKiro, Strength: 2},
			{Pattern: "yaml", Target: session.BackendKiro, Strength: 2},
			{Pattern: "架構", Target: session.BackendKiro, Strength: 2},
			{Pattern: "debug", Target: session.BackendKiro, Strength: 2},
			{Pattern: "fix", Target: session.BackendKiro, Strength: 2},
			{Pattern: "修", Target: session.BackendKiro, Strength: 2},

			// Weak signals (strength 1) — ambiguous
			{Pattern: "build", Target: session.BackendKiro, Strength: 1},
			{Pattern: "test", Target: session.BackendKiro, Strength: 1},
		},
		inputCounter:  0,
		lastHintInput: -3, // allow hint on first input
		cooldown:      3,
	}
}

// Hint evaluates the input against all rules and returns a suggestion if:
//   - The matched rule targets a DIFFERENT backend than currentBackend
//   - The match strength is >= 2 (medium or strong)
//   - The cooldown period has elapsed since the last hint
//
// Returns suggestedBackend, matchedKeyword, and a contextual reason string.
// All return values are empty strings if no hint should be shown.
func (h *RouteHinter) Hint(input string, currentBackend session.Backend) (suggestedBackend session.Backend, matchedKeyword string, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.inputCounter++

	lower := strings.ToLower(input)

	// Find the best (highest strength) matching rule for the OTHER backend.
	var bestRule *routeRule
	for i := range h.rules {
		rule := &h.rules[i]

		// Only consider rules targeting a different backend
		if rule.Target == currentBackend {
			continue
		}

		// Only suggest for medium or strong signals
		if rule.Strength < 2 {
			continue
		}

		// Check if pattern matches
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
		return "", "", ""
	}

	// Check cooldown: don't nag if we hinted recently
	if h.inputCounter-h.lastHintInput < h.cooldown {
		return "", "", ""
	}

	// We have a valid hint — record it and return
	h.lastHintInput = h.inputCounter

	reason = buildReason(bestRule, currentBackend)
	return bestRule.Target, bestRule.Pattern, reason
}

// buildReason generates a contextual message explaining WHY the hint is given.
func buildReason(rule *routeRule, current session.Backend) string {
	var targetName, currentName string

	switch rule.Target {
	case session.BackendClaude:
		targetName = "Claude (Victoria)"
	case session.BackendKiro:
		targetName = "Kiro"
	}

	switch current {
	case session.BackendClaude:
		currentName = "Claude"
	case session.BackendKiro:
		currentName = "Kiro"
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

	// Build contextual reason based on domain
	var domain string
	switch {
	case rule.Target == session.BackendClaude:
		domain = "Notion/文件/管理"
	case rule.Target == session.BackendKiro:
		domain = "基礎設施/程式碼/部署"
	}

	return strengthLabel + "切換至 " + targetName + "：偵測到「" + rule.Pattern +
		"」屬於" + domain + "領域，" + targetName + " 比 " + currentName + " 更適合處理。"
}

// ═══════════════════════════════════════════════════════════════════════════════
// Backward compatibility: package-level function using a default global hinter
// ═══════════════════════════════════════════════════════════════════════════════

var defaultHinter = NewRouteHinter()

// RouteHint checks if the user input contains keywords that suggest a different
// backend would be more appropriate. Returns the suggested backend and the
// matched keyword, or empty strings if no hint.
//
// This is the backward-compatible wrapper around the default RouteHinter.
// For full control (custom cooldown, reason messages), use NewRouteHinter().
func RouteHint(input string, currentBackend session.Backend) (suggested session.Backend, keyword string) {
	suggested, keyword, _ = defaultHinter.Hint(input, currentBackend)
	return suggested, keyword
}
