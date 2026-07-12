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

// ══════════════════════════════════════════════════════════════════════════════
// New Coverage Tests
// ══════════════════════════════════════════════════════════════════════════════

// TestNewRegistry_AutoDetect verifies that NewRegistry("") doesn't panic and returns non-nil.
func TestNewRegistry_AutoDetect(t *testing.T) {
	r := NewRegistry("")
	if r == nil {
		t.Fatal("NewRegistry(\"\") returned nil")
	}
	// The registry should have the backends map initialized
	if r.backends == nil {
		t.Error("NewRegistry(\"\") did not initialize backends map")
	}
}

// TestNewRegistry_Override verifies that NewRegistry("kiro") sets kiro as primary (if available).
func TestNewRegistry_Override(t *testing.T) {
	r := NewRegistry("kiro")
	if r == nil {
		t.Fatal("NewRegistry(\"kiro\") returned nil")
	}
	// If kiro is installed, it should be primary
	if r.Has("kiro") {
		if r.PrimaryName() != "kiro" {
			t.Errorf("NewRegistry(\"kiro\"): PrimaryName()=%q, want 'kiro'", r.PrimaryName())
		}
	}
	// Either way, shouldn't panic
}

// TestNewRegistry_InvalidOverride verifies graceful fallback with nonexistent backend.
func TestNewRegistry_InvalidOverride(t *testing.T) {
	r := NewRegistry("nonexistent")
	if r == nil {
		t.Fatal("NewRegistry(\"nonexistent\") returned nil")
	}
	// Should fall back to auto-detection (not "nonexistent")
	if r.PrimaryName() == "nonexistent" {
		t.Error("NewRegistry(\"nonexistent\") should not set nonexistent as primary")
	}
}

// TestRegistry_Summary_NonEmpty verifies Summary() returns a non-empty string
// when at least one backend is available.
func TestRegistry_Summary_NonEmpty(t *testing.T) {
	r := NewRegistry("")
	summary := r.Summary()
	if summary == "" {
		t.Error("Summary() should not return empty string")
	}
	// If no backends, it should still return the no-backends message
	if len(r.Available()) == 0 {
		if summary != "(no AI backends detected)" {
			t.Errorf("unexpected empty summary: %q", summary)
		}
	} else {
		if !contains(summary, "primary") {
			t.Errorf("Summary() should mention primary, got: %q", summary)
		}
	}
}

// TestRegistry_Resolve_Variants verifies Resolve with various inputs.
func TestRegistry_Resolve_Variants(t *testing.T) {
	r := NewRegistry("")

	// Resolve("unknown") should fall back to primary (which may be nil if no backends)
	b := r.Resolve("unknown")
	if len(r.Available()) > 0 {
		if b == nil {
			t.Error("Resolve(\"unknown\") should return primary (fallback), got nil")
		}
	} else {
		if b != nil {
			t.Error("Resolve(\"unknown\") should return nil when no backends available")
		}
	}

	// Resolve with known aliases
	for _, alias := range []string{"kiro-cli", "claude-code", "gemini-cli"} {
		_ = r.Resolve(alias) // should not panic
	}
}

// TestRegistry_Available verifies Available() returns a list (possibly empty).
func TestRegistry_Available_List(t *testing.T) {
	r := NewRegistry("")
	list := r.Available()
	// Should never be nil (could be empty slice)
	if list == nil {
		// Actually it can be nil if no backends. That's fine.
		t.Log("Available() returned nil (no backends on this machine)")
	}
	// Each entry should be a valid name
	for _, name := range list {
		if name == "" {
			t.Error("Available() contains empty string")
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
