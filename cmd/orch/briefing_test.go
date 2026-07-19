package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/memory"
)

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "orch.db")
	store, err := memory.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestGenerateBriefingFromFile_Success verifies the happy path: a configured
// briefing_source_file is read fresh, summarized via the local model, and the
// result is both returned and persisted via SetBriefing (so `orch briefing`
// reflects it too, not just the boot-time display).
func TestGenerateBriefingFromFile_Success(t *testing.T) {
	mlx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"今日重點：orch v0.16.1 已發布，briefing 現在會讀取真實檔案。"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mlx.Close()

	sourceFile := filepath.Join(t.TempDir(), "handoff.md")
	if err := os.WriteFile(sourceFile, []byte("# Handoff\n\n🔴 待辦：測試 briefing_source_file"), 0644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	cfg := &config.Config{
		Models: []config.ModelDef{
			{Name: "test", Backend: "mlx", Endpoint: mlx.URL, Model: "test-model", Default: true},
		},
		Memory: config.MemoryConfig{BriefingSourceFile: sourceFile},
	}
	store := newTestStore(t)

	answer, err := generateBriefingFromFile(cfg, store)
	if err != nil {
		t.Fatalf("generateBriefingFromFile returned error: %v", err)
	}
	if answer == "" {
		t.Fatal("expected non-empty briefing text")
	}

	saved, _, err := store.GetBriefing()
	if err != nil {
		t.Fatalf("GetBriefing failed: %v", err)
	}
	if saved != answer {
		t.Errorf("GetBriefing() = %q, want it to match generated answer %q", saved, answer)
	}
}

// TestGenerateBriefingFromFile_MissingFile verifies a missing source file
// returns an error (so the boot path can fall back to the cached briefing)
// rather than panicking or blocking startup.
func TestGenerateBriefingFromFile_MissingFile(t *testing.T) {
	cfg := &config.Config{
		Memory: config.MemoryConfig{BriefingSourceFile: filepath.Join(t.TempDir(), "does-not-exist.md")},
	}
	store := newTestStore(t)

	_, err := generateBriefingFromFile(cfg, store)
	if err == nil {
		t.Fatal("expected an error for a missing briefing_source_file, got nil")
	}
}

// TestTruncateStr_RuneSafe is a regression test for a latent bug: truncateStr
// used len(s) (byte length) and s[:max] (byte slice), which for CJK text (3
// bytes/character in UTF-8) can cut through the middle of a multi-byte
// character and produce invalid UTF-8. This matters here specifically because
// generateBriefingFromFile feeds a truncated handoff document (routinely
// CJK-heavy) straight into the MLX request JSON.
func TestTruncateStr_RuneSafe(t *testing.T) {
	// 5 three-byte CJK characters, truncate to 3 runes: a byte-based cut at
	// index 3 would land mid-character; a rune-based cut must not.
	s := "交接清單摘要"
	got := truncateStr(s, 3)
	want := "交接清..."
	if got != want {
		t.Errorf("truncateStr(%q, 3) = %q, want %q", s, got, want)
	}
}

// TestGenerateBriefingFromFile_MLXUnavailable verifies an unreachable local
// model returns an error instead of hanging or panicking.
func TestGenerateBriefingFromFile_MLXUnavailable(t *testing.T) {
	sourceFile := filepath.Join(t.TempDir(), "handoff.md")
	if err := os.WriteFile(sourceFile, []byte("# Handoff"), 0644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	cfg := &config.Config{
		Models: []config.ModelDef{
			{Name: "test", Backend: "mlx", Endpoint: "http://127.0.0.1:1", Model: "test-model", Default: true},
		},
		Memory: config.MemoryConfig{BriefingSourceFile: sourceFile},
	}
	store := newTestStore(t)

	_, err := generateBriefingFromFile(cfg, store)
	if err == nil {
		t.Fatal("expected an error when the local model is unavailable, got nil")
	}
}
