package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the persistent memory layer backed by SQLite.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at the given path.
func Open(dbPath string) (*Store, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Schema Migration ---

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL DEFAULT (datetime('now')),
    input TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL DEFAULT '',
    output_summary TEXT NOT NULL DEFAULT '',
    success INTEGER NOT NULL DEFAULT 1,
    tags TEXT NOT NULL DEFAULT '[]',
    took_ms INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS prompts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    content TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS agents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    command TEXT NOT NULL,
    capabilities TEXT NOT NULL DEFAULT '[]',
    priority INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS skills (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    trigger_keywords TEXT NOT NULL DEFAULT '[]',
    prompt_template TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL DEFAULT '',
    steps_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS shortcuts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    prefix TEXT NOT NULL,
    category TEXT NOT NULL,
    command_template TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS workspaces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    keywords TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS briefing (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    generated_at TEXT NOT NULL DEFAULT (datetime('now')),
    content TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_history_timestamp ON history(timestamp);
CREATE INDEX IF NOT EXISTS idx_history_category ON history(category);
CREATE INDEX IF NOT EXISTS idx_history_tags ON history(tags);
`

// --- History ---

type HistoryEntry struct {
	ID            int64
	Timestamp     string
	Input         string
	Category      string
	Agent         string
	OutputSummary string
	Success       bool
	Tags          []string
	TookMs        int64
}

func (s *Store) AddHistory(entry HistoryEntry) error {
	tags, _ := json.Marshal(entry.Tags)
	success := 0
	if entry.Success {
		success = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO history (input, category, agent, output_summary, success, tags, took_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.Input, entry.Category, entry.Agent, entry.OutputSummary, success, string(tags), entry.TookMs)
	return err
}

// RecentHistory returns the N most recent history entries.
func (s *Store) RecentHistory(limit int) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, timestamp, input, category, agent, output_summary, success, tags, took_ms
		FROM history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var tagsStr string
		var success int
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Input, &e.Category, &e.Agent, &e.OutputSummary, &success, &tagsStr, &e.TookMs); err != nil {
			return nil, err
		}
		e.Success = success == 1
		json.Unmarshal([]byte(tagsStr), &e.Tags)
		entries = append(entries, e)
	}
	return entries, nil
}

// SearchHistory finds history entries matching a keyword in input or tags.
func (s *Store) SearchHistory(keyword string, limit int) ([]HistoryEntry, error) {
	pattern := "%" + keyword + "%"
	rows, err := s.db.Query(`
		SELECT id, timestamp, input, category, agent, output_summary, success, tags, took_ms
		FROM history
		WHERE input LIKE ? OR tags LIKE ?
		ORDER BY id DESC LIMIT ?`, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var tagsStr string
		var success int
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Input, &e.Category, &e.Agent, &e.OutputSummary, &success, &tagsStr, &e.TookMs); err != nil {
			return nil, err
		}
		e.Success = success == 1
		json.Unmarshal([]byte(tagsStr), &e.Tags)
		entries = append(entries, e)
	}
	return entries, nil
}

// --- Briefing ---

func (s *Store) SetBriefing(content string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// Keep only the latest briefing
	tx.Exec("DELETE FROM briefing")
	_, err = tx.Exec("INSERT INTO briefing (content) VALUES (?)", content)
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) GetBriefing() (string, time.Time, error) {
	var content string
	var generatedAt string
	err := s.db.QueryRow("SELECT content, generated_at FROM briefing ORDER BY id DESC LIMIT 1").Scan(&content, &generatedAt)
	if err == sql.ErrNoRows {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	t, _ := time.Parse("2006-01-02 15:04:05", generatedAt)
	return content, t, nil
}

// --- Prompts ---

func (s *Store) SetPrompt(name, content string) error {
	_, err := s.db.Exec(`
		INSERT INTO prompts (name, content) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET content=excluded.content, updated_at=datetime('now')`,
		name, content)
	return err
}

func (s *Store) GetPrompt(name string) (string, error) {
	var content string
	err := s.db.QueryRow("SELECT content FROM prompts WHERE name = ?", name).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return content, err
}

// --- Agents ---

type AgentDef struct {
	Name         string
	Command      string
	Capabilities []string
	Priority     int
	Enabled      bool
}

func (s *Store) SetAgent(a AgentDef) error {
	caps, _ := json.Marshal(a.Capabilities)
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO agents (name, command, capabilities, priority, enabled) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET command=excluded.command, capabilities=excluded.capabilities, priority=excluded.priority, enabled=excluded.enabled`,
		a.Name, a.Command, string(caps), a.Priority, enabled)
	return err
}

func (s *Store) GetAgents() ([]AgentDef, error) {
	rows, err := s.db.Query("SELECT name, command, capabilities, priority, enabled FROM agents ORDER BY priority DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentDef
	for rows.Next() {
		var a AgentDef
		var capsStr string
		var enabled int
		if err := rows.Scan(&a.Name, &a.Command, &capsStr, &a.Priority, &enabled); err != nil {
			return nil, err
		}
		a.Enabled = enabled == 1
		json.Unmarshal([]byte(capsStr), &a.Capabilities)
		agents = append(agents, a)
	}
	return agents, nil
}

// --- Skills ---

type SkillDef struct {
	Name            string
	TriggerKeywords []string
	PromptTemplate  string
	Agent           string
	StepsJSON       string
}

func (s *Store) SetSkill(sk SkillDef) error {
	keywords, _ := json.Marshal(sk.TriggerKeywords)
	_, err := s.db.Exec(`
		INSERT INTO skills (name, trigger_keywords, prompt_template, agent, steps_json) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET trigger_keywords=excluded.trigger_keywords, prompt_template=excluded.prompt_template, agent=excluded.agent, steps_json=excluded.steps_json`,
		sk.Name, string(keywords), sk.PromptTemplate, sk.Agent, sk.StepsJSON)
	return err
}

func (s *Store) GetSkills() ([]SkillDef, error) {
	rows, err := s.db.Query("SELECT name, trigger_keywords, prompt_template, agent, steps_json FROM skills")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var skills []SkillDef
	for rows.Next() {
		var sk SkillDef
		var kwStr string
		if err := rows.Scan(&sk.Name, &kwStr, &sk.PromptTemplate, &sk.Agent, &sk.StepsJSON); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(kwStr), &sk.TriggerKeywords)
		skills = append(skills, sk)
	}
	return skills, nil
}

// --- Prune ---

// PruneHistory deletes history entries older than the given number of seconds.
// Pass 0 to delete all entries.
func (s *Store) PruneHistory(olderThanSeconds int64) (int64, error) {
	var result sql.Result
	var err error
	if olderThanSeconds == 0 {
		result, err = s.db.Exec("DELETE FROM history")
	} else {
		result, err = s.db.Exec(
			"DELETE FROM history WHERE timestamp < datetime('now', ? || ' seconds')",
			fmt.Sprintf("-%d", olderThanSeconds))
	}
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
