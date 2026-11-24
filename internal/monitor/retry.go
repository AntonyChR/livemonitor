package monitor

import (
	"context"
	"log/slog"
	"time"

	"live-recorder/internal/domain"
)

const (
	defaultRecordingRetryCount = 3
	defaultRecordingRetryDelay = 5 * time.Second
)

// recordingAttempt tracks the retries of one recording episode for a user.
type recordingAttempt struct {
	retries int
	origin  domain.RecordingOrigin
	cancel  context.CancelFunc
}

// SetRecordingRetryPolicy updates how many times a failed recording start is
// retried and how long to wait between attempts.
func (m *Manager) SetRecordingRetryPolicy(count int, delay time.Duration) {
	if count < 0 {
		count = 0
	}
	if delay < 0 {
		delay = 0
	}
	m.mu.Lock()
	m.retryCount = count
	m.retryDelay = delay
	m.mu.Unlock()
}

func (m *Manager) beginAttempt(userID string, origin domain.RecordingOrigin) *recordingAttempt {
	m.mu.Lock()
	if previous := m.attempts[userID]; previous != nil && previous.cancel != nil {
		previous.cancel()
	}
	attempt := &recordingAttempt{origin: origin}
	m.attempts[userID] = attempt
	m.mu.Unlock()
	return attempt
}

func (m *Manager) attemptCurrent(userID string, attempt *recordingAttempt) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.closing && m.attempts[userID] == attempt
}

func (m *Manager) cancelAttempt(userID string) {
	m.cancelAttemptIf(userID, nil)
}

func (m *Manager) cancelAutoAttempt(userID string) {
	origin := domain.RecordingOriginAuto
	m.cancelAttemptIf(userID, &origin)
}

func (m *Manager) cancelAttemptIf(userID string, origin *domain.RecordingOrigin) {
	m.mu.Lock()
	attempt := m.attempts[userID]
	if attempt != nil && (origin == nil || attempt.origin == *origin) {
		if attempt.cancel != nil {
			attempt.cancel()
		}
		delete(m.attempts, userID)
	}
	m.mu.Unlock()
}

func (m *Manager) scheduleRetry(userID string, attempt *recordingAttempt) {
	m.mu.Lock()
	if m.attempts[userID] != attempt {
		m.mu.Unlock()
		return
	}
	if m.closing || m.ctx == nil || m.retryCount <= 0 || attempt.retries >= m.retryCount {
		retries := attempt.retries
		if attempt.cancel != nil {
			attempt.cancel()
		}
		delete(m.attempts, userID)
		m.mu.Unlock()
		if retries > 0 {
			slog.Warn("Reintentos de grabación agotados",
				"user_id", userID,
				"reintentos", retries,
			)
		} else {
			slog.Warn("Grabación fallida; reintentos deshabilitados", "user_id", userID)
		}
		return
	}

	attempt.retries++
	attemptNumber := attempt.retries
	maxRetries := m.retryCount
	delay := m.retryDelay
	ctx, cancel := context.WithCancel(m.ctx)
	attempt.cancel = cancel
	m.mu.Unlock()

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		m.mu.Lock()
		if m.attempts[userID] != attempt {
			m.mu.Unlock()
			return
		}
		attempt.cancel = nil
		m.mu.Unlock()

		// A failed session is removed by the recorder manager right after
		// its failure callback returns. Wait for that before retrying, even
		// when the configured delay is zero.
		if !m.waitForSessionEnd(userID, attempt) {
			return
		}

		user, roomID, ok := m.retryTarget(userID, attempt.origin)
		if !ok {
			m.cancelAttempt(userID)
			return
		}

		slog.Info("Reintentando inicio de grabación",
			"user_id", userID,
			"alias", user.Alias,
			"intento", attemptNumber,
			"max_reintentos", maxRetries,
			"espera_segundos", delay.Seconds(),
		)
		_, _ = m.triggerRecordingAttempt(user, roomID, attempt.origin, attempt)
	}()
}

// waitForSessionEnd waits briefly for a failed session to leave the recorder
// manager. It returns false when the attempt was canceled or the manager
// keeps tracking a session for too long.
func (m *Manager) waitForSessionEnd(userID string, attempt *recordingAttempt) bool {
	deadline := time.Now().Add(2 * time.Second)
	for m.recordings.Has(userID) {
		if !m.attemptCurrent(userID, attempt) {
			return false
		}
		if time.Now().After(deadline) {
			m.cancelAttempt(userID)
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return m.attemptCurrent(userID, attempt)
}

func (m *Manager) retryTarget(userID string, origin domain.RecordingOrigin) (domain.UserConfig, string, bool) {
	m.mu.RLock()
	user, exists := m.users[userID]
	mon := m.monitors[userID]
	closing := m.closing
	m.mu.RUnlock()

	if !exists || mon == nil || closing {
		return domain.UserConfig{}, "", false
	}
	status := mon.Status()
	if status.LiveState != domain.LiveStateLive || status.RoomID == "" {
		return domain.UserConfig{}, "", false
	}
	if origin == domain.RecordingOriginAuto && !status.AutoRecordEnabled {
		return domain.UserConfig{}, "", false
	}
	if m.recordings.Has(userID) {
		return domain.UserConfig{}, "", false
	}
	return user, status.RoomID, true
}

// HandleRecordingConfirmed clears the retry state once FFmpeg reports
// progress and the recording is actually running.
func (m *Manager) HandleRecordingConfirmed(userID string) {
	m.cancelAttempt(userID)
}

// HandleRecordingEnded clears the retry state for a recording that stopped
// normally or failed after it had already started.
func (m *Manager) HandleRecordingEnded(userID string) {
	m.cancelAttempt(userID)
}

// HandleRecordingFailed schedules another attempt when the session failed
// before confirming its first frame. Failures after recording started are
// not retried.
func (m *Manager) HandleRecordingFailed(info domain.RecordingSessionInfo) {
	if info.StartedAt != nil {
		m.cancelAttempt(info.UserID)
		return
	}

	m.mu.RLock()
	attempt := m.attempts[info.UserID]
	m.mu.RUnlock()
	if attempt == nil {
		return
	}
	m.scheduleRetry(info.UserID, attempt)
}
