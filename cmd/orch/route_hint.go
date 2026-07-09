package main

import (
	"strings"

	"github.com/gordonwei/orch/pkg/session"
)

// routeHintRules maps keywords to the backend they belong to.
// When user input in a session matches a keyword for a DIFFERENT backend,
// we suggest switching.
var routeHintRules = map[string]session.Backend{
	// Claude (Victoria) domain — Notion, GCal, meetings, writing
	"notion":  session.BackendClaude,
	"同步":      session.BackendClaude,
	"會議":      session.BackendClaude,
	"會議記錄":    session.BackendClaude,
	"gcal":    session.BackendClaude,
	"gmail":   session.BackendClaude,
	"週報":      session.BackendClaude,
	"交接":      session.BackendClaude,
	"簡報":      session.BackendClaude,
	"pptx":    session.BackendClaude,
	"podcast": session.BackendClaude,

	// Kiro domain — infra, code, deploy, terraform
	"terraform": session.BackendKiro,
	"deploy":    session.BackendKiro,
	"部署":       session.BackendKiro,
	"kubectl":   session.BackendKiro,
	"helm":      session.BackendKiro,
	"docker":    session.BackendKiro,
	"程式碼":      session.BackendKiro,
	"refactor":  session.BackendKiro,
	"build":     session.BackendKiro,
	"test":      session.BackendKiro,
	"commit":    session.BackendKiro,
	"push":      session.BackendKiro,
	"aws":       session.BackendKiro,
	"gcp":       session.BackendKiro,
	"s3":        session.BackendKiro,
}

// RouteHint checks if the user input contains keywords that suggest a different
// backend would be more appropriate. Returns the suggested backend and the
// matched keyword, or empty strings if no hint.
func RouteHint(input string, currentBackend session.Backend) (suggested session.Backend, keyword string) {
	lower := strings.ToLower(input)
	for kw, target := range routeHintRules {
		if target == currentBackend {
			continue // only hint for cross-domain
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			return target, kw
		}
	}
	return "", ""
}
