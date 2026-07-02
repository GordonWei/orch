package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) (*Store, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return store, func() {
		store.Close()
		os.RemoveAll(dir)
	}
}

func TestHistory(t *testing.T) {
	store, cleanup := tempDB(t)
	defer cleanup()

	// Add entries
	for i := 0; i < 5; i++ {
		err := store.AddHistory(HistoryEntry{
			Input:         "kubectl get pods",
			Category:      "infra",
			Agent:         "shell",
			OutputSummary: "3 pods running",
			Success:       true,
			Tags:          []string{"k8s", "gke"},
			TookMs:        1200,
		})
		if err != nil {
			t.Fatalf("add history: %v", err)
		}
	}

	// Recent
	entries, err := store.RecentHistory(3)
	if err != nil {
		t.Fatalf("recent history: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	// Search
	found, err := store.SearchHistory("kubectl", 10)
	if err != nil {
		t.Fatalf("search history: %v", err)
	}
	if len(found) != 5 {
		t.Errorf("expected 5 entries, got %d", len(found))
	}

	// Search by tag
	found, err = store.SearchHistory("gke", 10)
	if err != nil {
		t.Fatalf("search history by tag: %v", err)
	}
	if len(found) != 5 {
		t.Errorf("expected 5 entries by tag, got %d", len(found))
	}
}

func TestBriefing(t *testing.T) {
	store, cleanup := tempDB(t)
	defer cleanup()

	// Empty
	content, _, err := store.GetBriefing()
	if err != nil {
		t.Fatalf("get empty briefing: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty briefing, got %q", content)
	}

	// Set
	if err := store.SetBriefing("Today's focus: litellm PRD deployment complete"); err != nil {
		t.Fatalf("set briefing: %v", err)
	}

	content, _, err = store.GetBriefing()
	if err != nil {
		t.Fatalf("get briefing: %v", err)
	}
	if content != "Today's focus: litellm PRD deployment complete" {
		t.Errorf("unexpected briefing: %q", content)
	}

	// Overwrite
	if err := store.SetBriefing("new briefing"); err != nil {
		t.Fatalf("set briefing 2: %v", err)
	}
	content, _, err = store.GetBriefing()
	if err != nil {
		t.Fatalf("get briefing 2: %v", err)
	}
	if content != "new briefing" {
		t.Errorf("expected overwrite, got %q", content)
	}
}

func TestPrompts(t *testing.T) {
	store, cleanup := tempDB(t)
	defer cleanup()

	// Set
	if err := store.SetPrompt("default", "you are orch..."); err != nil {
		t.Fatalf("set prompt: %v", err)
	}

	// Get
	content, err := store.GetPrompt("default")
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if content != "you are orch..." {
		t.Errorf("unexpected prompt: %q", content)
	}

	// Update (upsert)
	if err := store.SetPrompt("default", "updated prompt"); err != nil {
		t.Fatalf("update prompt: %v", err)
	}
	content, err = store.GetPrompt("default")
	if err != nil {
		t.Fatalf("get updated prompt: %v", err)
	}
	if content != "updated prompt" {
		t.Errorf("expected update, got %q", content)
	}

	// Non-existent
	content, err = store.GetPrompt("ghost")
	if err != nil {
		t.Fatalf("get ghost prompt: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty for non-existent, got %q", content)
	}
}

func TestAgents(t *testing.T) {
	store, cleanup := tempDB(t)
	defer cleanup()

	if err := store.SetAgent(AgentDef{
		Name:         "claude",
		Command:      "claude -p",
		Capabilities: []string{"code", "writing", "notion"},
		Priority:     10,
		Enabled:      true,
	}); err != nil {
		t.Fatalf("set agent: %v", err)
	}

	if err := store.SetAgent(AgentDef{
		Name:         "gemini",
		Command:      "gemini -p",
		Capabilities: []string{"long-context", "video"},
		Priority:     5,
		Enabled:      true,
	}); err != nil {
		t.Fatalf("set agent 2: %v", err)
	}

	agents, err := store.GetAgents()
	if err != nil {
		t.Fatalf("get agents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	// Sorted by priority DESC
	if agents[0].Name != "claude" {
		t.Errorf("expected claude first (priority 10), got %q", agents[0].Name)
	}
}

func TestSkills(t *testing.T) {
	store, cleanup := tempDB(t)
	defer cleanup()

	if err := store.SetSkill(SkillDef{
		Name:            "k8s-debug",
		TriggerKeywords: []string{"pod crash", "crashloopbackoff", "oom"},
		PromptTemplate:  "Diagnose: {{input}}",
		Agent:           "kiro",
		StepsJSON:       `[{"id":"step_1","agent":"shell","command":"kubectl describe pod {{pod}}"}]`,
	}); err != nil {
		t.Fatalf("set skill: %v", err)
	}

	skills, err := store.GetSkills()
	if err != nil {
		t.Fatalf("get skills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "k8s-debug" {
		t.Errorf("expected k8s-debug, got %q", skills[0].Name)
	}
	if len(skills[0].TriggerKeywords) != 3 {
		t.Errorf("expected 3 keywords, got %d", len(skills[0].TriggerKeywords))
	}
}

func TestPruneHistory(t *testing.T) {
	store, cleanup := tempDB(t)
	defer cleanup()

	// Add an entry
	if err := store.AddHistory(HistoryEntry{
		Input:    "old task",
		Category: "test",
		Agent:    "shell",
		Success:  true,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Prune entries older than 1 hour — entry was just created, should NOT be deleted
	deleted, err := store.PruneHistory(3600)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted (entry is fresh), got %d", deleted)
	}

	// Verify still there
	entries, _ := store.RecentHistory(10)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry still present, got %d", len(entries))
	}

	// Prune entries older than 0 seconds — everything is older than "now minus 0 seconds"
	// Actually this means "delete where timestamp < now" which is everything
	deleted, err = store.PruneHistory(0)
	if err != nil {
		t.Fatalf("prune all: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	entries, _ = store.RecentHistory(10)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after prune, got %d", len(entries))
	}
}
