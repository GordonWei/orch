package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/gordonwei/orch/pkg/session"
)

// SessionEvent represents a lifecycle event for a managed session.
type SessionEvent struct {
	Type    string // "died", "restarted", "killed"
	Backend session.Backend
	Err     error
}

// SessionManager manages multiple interactive PTY sessions.
// At most one session per backend; one is "active" at any time (or none).
type SessionManager struct {
	mu                sync.Mutex
	sessions          map[session.Backend]*managedSession
	active            session.Backend // "" means no active session (normal mode)
	autoRestart       map[session.Backend]bool
	events            chan SessionEvent
	stopWatch         chan struct{}
	stopOnce          sync.Once
	shutdown          bool // set once Shutdown() has started; permanently blocks late writes from checkSessions' auto-restart
	generation        int  // bumped by Shutdown()/KillAll(); lets checkSessions() detect a kill-all happened while it was mid-restart
	apiBackendFactory func(session.Backend) (session.StreamingBackend, error)
}

type managedSession struct {
	sess         session.SessionLike
	startedAt    time.Time
	restartCount int
	lastOutput   time.Time
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions:    make(map[session.Backend]*managedSession),
		autoRestart: make(map[session.Backend]bool),
		events:      make(chan SessionEvent, 16),
		stopWatch:   make(chan struct{}),
	}
}

// SetAPIBackendFactory sets the factory function for creating streaming API backends.
// Called during initialization with a closure that captures the apiBackends map.
func (m *SessionManager) SetAPIBackendFactory(factory func(session.Backend) (session.StreamingBackend, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiBackendFactory = factory
}

// Events returns the read-only channel for session lifecycle events.
func (m *SessionManager) Events() <-chan SessionEvent {
	return m.events
}

// stopWatcher signals WatchSessions' goroutine to stop. Closing the channel
// (instead of a non-blocking send) guarantees the signal is never dropped,
// regardless of what the watcher goroutine is doing at the moment this is
// called. Safe to call more than once (Shutdown and KillAll can both call it).
func (m *SessionManager) stopWatcher() {
	m.stopOnce.Do(func() {
		close(m.stopWatch)
	})
}

// SetAutoRestart enables or disables auto-restart for a backend.
// When enabled, if the session dies it will automatically respawn.
func (m *SessionManager) SetAutoRestart(backend session.Backend, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoRestart[backend] = enabled
}

// WatchSessions starts a background goroutine that periodically checks
// session health and emits events when sessions die.
// It also handles auto-restart logic. Call this once after creating the manager.
func (m *SessionManager) WatchSessions() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-m.stopWatch:
				return
			case <-ticker.C:
				m.checkSessions()
			}
		}
	}()
}

// checkSessions inspects all managed sessions and handles dead ones.
func (m *SessionManager) checkSessions() {
	m.mu.Lock()

	gen := m.generation
	var deadBackends []session.Backend
	for backend, ms := range m.sessions {
		if !ms.sess.Alive() {
			deadBackends = append(deadBackends, backend)
		}
	}

	if len(deadBackends) == 0 {
		m.mu.Unlock()
		return
	}

	// Process dead sessions
	for _, backend := range deadBackends {
		ms := m.sessions[backend]
		restartCount := ms.restartCount

		// Clear active pointer if this was the active session
		if m.active == backend {
			m.active = ""
		}

		shouldRestart := m.autoRestart[backend]
		delete(m.sessions, backend)

		m.mu.Unlock()

		// Emit "died" event
		m.emitEvent(SessionEvent{
			Type:    "died",
			Backend: backend,
			Err:     fmt.Errorf("process exited unexpectedly"),
		})

		// Auto-restart if enabled
		if shouldRestart {
			cfg := session.DefaultConfig(backend)
			sess, err := session.Spawn(cfg)
			if err != nil {
				m.emitEvent(SessionEvent{
					Type:    "died",
					Backend: backend,
					Err:     fmt.Errorf("auto-restart failed: %w", err),
				})
			} else {
				m.mu.Lock()
				// While this restart Spawn() was running unlocked, the
				// manager may have been shut down or KillAll'd (generation
				// bumped), or a concurrent Spawn/SpawnOrSwitch call may have
				// already created a new session for this backend. In any of
				// those cases, writing our restarted session into the map
				// would orphan/overwrite something — kill our redundant one
				// instead.
				_, alreadyReplaced := m.sessions[backend]
				discard := m.shutdown || m.generation != gen || alreadyReplaced
				if !discard {
					m.sessions[backend] = &managedSession{
						sess:         sess,
						startedAt:    time.Now(),
						restartCount: restartCount + 1,
						lastOutput:   time.Now(),
					}
				}
				m.mu.Unlock()

				if discard {
					sess.Kill()
				} else {
					m.emitEvent(SessionEvent{
						Type:    "restarted",
						Backend: backend,
					})
				}
			}
		}

		m.mu.Lock()
	}

	m.mu.Unlock()
}

// emitEvent sends a non-blocking event to the events channel.
func (m *SessionManager) emitEvent(event SessionEvent) {
	select {
	case m.events <- event:
	default:
		// Channel full, drop event (non-blocking to avoid deadlock)
	}
}

// Shutdown gracefully terminates all sessions:
// 1. Sends graceful exit commands to all sessions
// 2. Waits up to 5 seconds for all to exit
// 3. Force kills any remaining
// 4. Returns only when all PTY fds are closed
func (m *SessionManager) Shutdown() {
	// Stop the watcher first
	m.stopWatcher()

	m.mu.Lock()
	m.shutdown = true
	if len(m.sessions) == 0 {
		m.mu.Unlock()
		return
	}

	// Collect all sessions
	type sessionEntry struct {
		backend session.Backend
		ms      *managedSession
	}
	var entries []sessionEntry
	for backend, ms := range m.sessions {
		entries = append(entries, sessionEntry{backend: backend, ms: ms})
	}
	m.sessions = make(map[session.Backend]*managedSession)
	m.active = ""
	m.mu.Unlock()

	// Phase 1: Send graceful exit commands to all
	for _, e := range entries {
		exitCmd := "/exit\r"
		if e.backend == session.BackendKiro || e.backend == session.BackendGemini {
			exitCmd = "/quit\r"
		}
		e.ms.sess.Send(exitCmd)
	}

	// Phase 2: Wait up to 5 seconds for all to exit gracefully
	deadline := time.After(5 * time.Second)
	remaining := make([]*managedSession, 0, len(entries))

	for _, e := range entries {
		remaining = append(remaining, e.ms)
	}

	for len(remaining) > 0 {
		select {
		case <-deadline:
			// Timeout — force kill all remaining
			goto forceKill
		default:
			var stillAlive []*managedSession
			for _, ms := range remaining {
				if ms.sess.Alive() {
					stillAlive = append(stillAlive, ms)
				}
			}
			remaining = stillAlive
			if len(remaining) > 0 {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
	return

forceKill:
	// Phase 3: Force kill any that didn't exit gracefully
	var wg sync.WaitGroup
	for _, ms := range remaining {
		if ms.sess.Alive() {
			wg.Add(1)
			go func(s *managedSession) {
				defer wg.Done()
				s.sess.Kill()
			}(ms)
		}
	}
	wg.Wait()

	// Emit killed events only for the sessions that actually had to be
	// force-killed — sessions that already exited gracefully within the
	// deadline were removed from `remaining` above and must not be
	// relabeled as "killed" here.
	for _, e := range entries {
		for _, r := range remaining {
			if r == e.ms {
				m.emitEvent(SessionEvent{
					Type:    "killed",
					Backend: e.backend,
				})
				break
			}
		}
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
		sess:       sess,
		startedAt:  time.Now(),
		lastOutput: time.Now(),
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

	err := ms.sess.Kill()

	m.emitEvent(SessionEvent{
		Type:    "killed",
		Backend: backend,
	})

	return err
}

// KillAll terminates all sessions.
func (m *SessionManager) KillAll() {
	// Stop the watcher
	m.stopWatcher()

	m.mu.Lock()
	// Bump the generation so any auto-restart from checkSessions() that was
	// already in flight (Spawn() running unlocked) discards its result
	// instead of writing into the map after we've cleared it. Unlike
	// Shutdown(), this does NOT permanently disable future restarts —
	// KillAll() can be called mid-session (e.g. `/kill all`) and the
	// manager keeps running afterward.
	m.generation++
	toKill := make([]session.SessionLike, 0, len(m.sessions))
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
func (m *SessionManager) Active() session.SessionLike {
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
			Backend:      backend,
			StartedAt:    ms.startedAt,
			IsActive:     backend == m.active,
			IsIdle:       ms.sess.IsIdle(),
			RestartCount: ms.restartCount,
			LastOutput:   ms.lastOutput,
		})
	}
	return infos
}

// SessionInfo holds display information about a session.
type SessionInfo struct {
	Backend      session.Backend
	StartedAt    time.Time
	IsActive     bool
	IsIdle       bool
	RestartCount int
	LastOutput   time.Time
}

// Get returns the session for a specific backend (nil if not exists/dead).
func (m *SessionManager) Get(backend session.Backend) session.SessionLike {
	m.mu.Lock()
	defer m.mu.Unlock()

	ms, ok := m.sessions[backend]
	if !ok || !ms.sess.Alive() {
		return nil
	}
	return ms.sess
}

// TouchOutput updates the LastOutput timestamp for a backend.
// Called by the REPL when output is received from a session.
func (m *SessionManager) TouchOutput(backend session.Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ms, ok := m.sessions[backend]; ok {
		ms.lastOutput = time.Now()
	}
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

	// API backends: create APISession (no PTY process needed)
	if backend == session.BackendBedrock || backend == session.BackendVertexAI {
		if m.apiBackendFactory == nil {
			return fmt.Errorf("API backend %s not configured (enable in config.yaml)", backend)
		}
		sb, err := m.apiBackendFactory(backend)
		if err != nil {
			return fmt.Errorf("spawn %s: %w", backend, err)
		}
		apiSess := session.NewAPISession(sb, backend)
		m.mu.Lock()
		m.sessions[backend] = &managedSession{
			sess:       apiSess,
			startedAt:  time.Now(),
			lastOutput: time.Now(),
		}
		m.active = backend
		m.mu.Unlock()
		return nil
	}

	// PTY backends: spawn CLI process
	cfg := session.DefaultConfig(backend)
	sess, err := session.Spawn(cfg)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", backend, err)
	}

	m.mu.Lock()
	m.sessions[backend] = &managedSession{
		sess:       sess,
		startedAt:  time.Now(),
		lastOutput: time.Now(),
	}
	m.active = backend
	m.mu.Unlock()
	return nil
}
