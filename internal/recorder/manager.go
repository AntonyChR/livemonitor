package recorder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
)

// Manager coordinates all active recording sessions.
type Manager struct {
	mu           sync.RWMutex
	sessions     map[string]*Session
	closing      bool
	options      config.Settings
	runner       ProcessRunner
	listener     SessionEventListener
	shutdownOnce sync.Once
}

// NewManager creates a new RecordingManager.
func NewManager(
	options config.Settings,
	runner ProcessRunner,
	listener SessionEventListener,
) *Manager {
	if runner == nil {
		runner = NewRealProcessRunner()
	}
	return &Manager{
		sessions: make(map[string]*Session),
		options:  options,
		runner:   runner,
		listener: listener,
	}
}

// UpdateSettings replaces the options used by sessions started from now on.
// Active sessions keep the options they were created with.
func (m *Manager) UpdateSettings(options config.Settings) {
	m.mu.Lock()
	m.options = options
	m.mu.Unlock()
}

// Has returns true if there is an active recording session for the given userID.
func (m *Manager) Has(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.sessions[userID]
	return exists
}

// Get returns the session for a user if one is currently tracked.
func (m *Manager) Get(userID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, exists := m.sessions[userID]
	return sess, exists
}

// Start launches a new recording session for the given user.
func (m *Manager) Start(user domain.UserConfig, liveURL string, origin domain.RecordingOrigin) (*Session, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, errors.New("el gestor de grabaciones se está cerrando")
	}
	if _, exists := m.sessions[user.ID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("ya existe una sesión activa para el usuario %s (%s)", user.Alias, user.ID)
	}

	var session *Session
	session = NewSession(user, liveURL, origin, m.options, m.runner, m.listener, func() {
		m.mu.Lock()
		if current, exists := m.sessions[user.ID]; exists && current == session {
			delete(m.sessions, user.ID)
		}
		m.mu.Unlock()
	})
	m.sessions[user.ID] = session
	m.mu.Unlock()

	go func() {
		err := session.Run()
		if err != nil {
			slog.Error("Sesión finalizada con error",
				"user_id", user.ID,
				"alias", user.Alias,
				"error", err,
			)
		}
	}()

	return session, nil
}

// Stop initiates termination of an active recording for the given userID.
func (m *Manager) Stop(userID string) error {
	m.mu.RLock()
	session, exists := m.sessions[userID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("no existe sesión activa para el usuario con ID %s", userID)
	}

	session.Stop()
	return nil
}

// Summary returns a formatted string representing the status of all current recordings.
func (m *Manager) Summary() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.sessions) == 0 {
		return "ninguna"
	}

	var descriptions []string
	now := time.Now()
	for _, sess := range m.sessions {
		info := sess.Info()
		name := info.Alias

		switch info.State {
		case domain.RecordingStateStarting:
			descriptions = append(descriptions, fmt.Sprintf("%s - iniciando", name))
		case domain.RecordingStateStopping:
			descriptions = append(descriptions, fmt.Sprintf("%s - cerrando", name))
		default:
			if info.StartedAt != nil {
				minutes := int(now.Sub(*info.StartedAt).Minutes())
				minWord := "minutos"
				if minutes == 1 {
					minWord = "minuto"
				}
				descriptions = append(descriptions, fmt.Sprintf("%s - %d %s", name, minutes, minWord))
			} else {
				descriptions = append(descriptions, fmt.Sprintf("%s - iniciando", name))
			}
		}
	}

	return fmt.Sprintf("%s", strings.Join(descriptions, ", "))
}

// ActiveSessions returns a list of snapshots of currently active sessions.
func (m *Manager) ActiveSessions() []domain.RecordingSessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]domain.RecordingSessionInfo, 0, len(m.sessions))
	for _, sess := range m.sessions {
		result = append(result, sess.Info())
	}
	return result
}

// Shutdown stops all active recording sessions in parallel and waits for completion.
func (m *Manager) Shutdown(ctx context.Context) error {
	var sessions []*Session

	m.mu.Lock()
	m.closing = true
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(sess *Session) {
			defer wg.Done()
			sess.Stop()
			select {
			case <-sess.Done():
			case <-ctx.Done():
			}
		}(s)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
