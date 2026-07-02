package backend

import (
	"testing"
)

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"kiro", "kiro"},
		{"kiro-cli", "kiro"},
		{"claude", "claude"},
		{"claude-code", "claude"},
		{"gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"KIRO", "kiro"},
		{"Claude", "claude"},
		{" gemini ", "gemini"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := normalizeName(tt.input)
		if got != tt.want {
			t.Errorf("normalizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRegistryResolve(t *testing.T) {
	// Create a registry with a mock setup
	r := &Registry{
		backends: map[string]Backend{
			"kiro": &KiroBackend{},
		},
		primary: "kiro",
		order:   []string{"kiro"},
	}

	// Requesting an available backend returns it
	b := r.Resolve("kiro")
	if b == nil || b.Name() != "kiro" {
		t.Errorf("Resolve(kiro) should return kiro backend")
	}

	// Requesting an unavailable backend falls back to primary
	b = r.Resolve("claude")
	if b == nil || b.Name() != "kiro" {
		t.Errorf("Resolve(claude) should fallback to kiro (primary)")
	}

	// Requesting with alias
	b = r.Resolve("kiro-cli")
	if b == nil || b.Name() != "kiro" {
		t.Errorf("Resolve(kiro-cli) should resolve to kiro")
	}
}

func TestDetectBackends(t *testing.T) {
	// This test just ensures DetectBackends doesn't panic
	// Actual results depend on the test environment
	backends := DetectBackends()
	t.Logf("Detected backends: %v", backends)
}

func TestRegistrySummary(t *testing.T) {
	r := &Registry{
		backends: map[string]Backend{
			"kiro":   &KiroBackend{},
			"claude": &ClaudeBackend{},
		},
		primary: "kiro",
		order:   []string{"kiro", "claude"},
	}

	summary := r.Summary()
	if summary == "" {
		t.Error("Summary should not be empty")
	}
	t.Logf("Summary: %s", summary)
}

func TestEmptyRegistry(t *testing.T) {
	r := &Registry{
		backends: map[string]Backend{},
		primary:  "",
		order:    []string{},
	}

	if r.Primary() != nil {
		t.Error("Primary should be nil when no backends available")
	}

	if r.Resolve("kiro") != nil {
		t.Error("Resolve should return nil when no backends available")
	}

	summary := r.Summary()
	if summary != "(no AI backends detected)" {
		t.Errorf("unexpected summary for empty registry: %q", summary)
	}
}
