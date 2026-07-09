package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/gordonwei/orch/pkg/session"
)

// SessionManager manages multiple interactive PTY sessions.
// At most one session per backend; one is "active" at any time (or none).
type SessionManager struct {
	mu       sync.Mutex
	sessions map[session.Backend]*managedSession
	active   session.Backend // "" means no active session (normal mode)
}

type managedSession struct {
	sess      *session.Session
	startedAt time.Time
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[session.Backend]*managedSession),
	}
}

// Spawn creates a new session for the given backend.
// If one already exists for that backend, returns an error.
func (m *SessionManager) Spawn(backend session.Backend) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ms, ok := m.sessions[backend]; ok && ms.sess.Alive() {
		return fmt.Errorf("session %q already running", backend)
	}

	cfg := session.DefaultConfig(backend)
	sess, err := session.Spawn(cfg)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", backend, err)
	}

	m.sessions[backend] = &managedSession{
		sess:      sess,
		startedAt: time.Now(),
	}
	m.active = backend
	return nil
}

// Switch sets the active session to the given backend.
// The backend must already have a running session.
func (m *SessionManager) Switch(backend session.Backend) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ms, ok := m.sessions[backend]
	if !ok || !ms.sess.Alive() {
		return fmt.Errorf("no running session for %q", backend)
	}
	m.active = backend
	return nil
}

// Back clears the active session pointer (returns to normal mode).
// The session remains alive in the background.
func (m *SessionManager) Back() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = ""
}

// Kill terminates the session for the given backend.
// If it was active, clears the active pointer.
func (m *SessionManager) Kill(backend session.Backend) error {
	m.mu.Lock()
	ms, ok := m.sessions[backend]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no session for %q", backend)
	}
	if m.active == backend {
		m.active = ""
	}
	delete(m.sessions, backend)
	m.mu.Unlock()

	return ms.sess.Kill()
}

// KillAll terminates all sessions.
func (m *SessionManager) KillAll() {
	m.mu.Lock()
	toKill := make([]*session.Session, 0, len(m.sessions))
	for _, ms := range m.sessions {
		toKill = append(toKill, ms.sess)
	}
	m.sessions = make(map[session.Backend]*managedSession)
	m.active = ""
	m.mu.Unlock()

	for _, s := range toKill {
		s.Kill()
	}
}

// Active returns the currently active session (nil if normal mode).
func (m *SessionManager) Active() *session.Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active == "" {
		return nil
	}
	ms, ok := m.sessions[m.active]
	if !ok || !ms.sess.Alive() {
		m.active = ""
		return nil
	}
	return ms.sess
}

// ActiveBackend returns the name of the active backend ("" if none).
func (m *SessionManager) ActiveBackend() session.Backend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// HasActive returns true if there is an active session.
func (m *SessionManager) HasActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active == "" {
		return false
	}
	ms, ok := m.sessions[m.active]
	if !ok || !ms.sess.Alive() {
		m.active = ""
		return false
	}
	return true
}

// List returns info about all running sessions.
func (m *SessionManager) List() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	var infos []SessionInfo
	for backend, ms := range m.sessions {
		if !ms.sess.Alive() {
			continue
		}
		infos = append(infos, SessionInfo{
			Backend:   backend,
			StartedAt: ms.startedAt,
			IsActive:  backend == m.active,
			IsIdle:    ms.sess.IsIdle(),
		})
	}
	return infos
}

// SessionInfo holds display information about a session.
type SessionInfo struct {
	Backend   session.Backend
	StartedAt time.Time
	IsActive  bool
	IsIdle    bool
}

// Get returns the session for a specific backend (nil if not exists/dead).
func (m *SessionManager) Get(backend session.Backend) *session.Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	ms, ok := m.sessions[backend]
	if !ok || !ms.sess.Alive() {
		return nil
	}
	return ms.sess
}

// SpawnOrSwitch spawns a new session if none exists, otherwise switches to it.
func (m *SessionManager) SpawnOrSwitch(backend session.Backend) error {
	m.mu.Lock()
	ms, ok := m.sessions[backend]
	if ok && ms.sess.Alive() {
		m.active = backend
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// Need to spawn (lock released during potentially slow operation)
	cfg := session.DefaultConfig(backend)
	sess, err := session.Spawn(cfg)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", backend, err)
	}

	m.mu.Lock()
	m.sessions[backend] = &managedSession{
		sess:      sess,
		startedAt: time.Now(),
	}
	m.active = backend
	m.mu.Unlock()
	return nil
}
