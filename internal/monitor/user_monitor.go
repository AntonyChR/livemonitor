package monitor

import (
	"context"
	"sync"
	"time"

	"live-recorder/internal/domain"
	"live-recorder/internal/platform/douyin"
)

// MonitorCallback receives state updates from a UserMonitor.
type MonitorCallback interface {
	OnUserStatusChanged(prev domain.UserStatus, curr domain.UserStatus)
	OnQueryError(user domain.UserConfig, err error)
	OnLiveDetected(user domain.UserConfig, roomID string)
}

// UserMonitor manages the polling loop for an individual user.
type UserMonitor struct {
	mu          sync.RWMutex
	user        domain.UserConfig
	client      douyin.Client
	interval    time.Duration
	callback    MonitorCallback
	lastRoomID  string
	status      domain.UserStatus
	cancel      context.CancelFunc
	done        chan struct{}
	started     bool
	stoppedOnce sync.Once
}

// NewUserMonitor creates a UserMonitor for a given user.
func NewUserMonitor(
	user domain.UserConfig,
	client douyin.Client,
	interval time.Duration,
	callback MonitorCallback,
) *UserMonitor {
	return &UserMonitor{
		user:     user,
		client:   client,
		interval: interval,
		callback: callback,
		status: domain.UserStatus{
			ID:                user.ID,
			Alias:             user.Alias,
			LiveState:         domain.LiveStateUnknown,
			AutoRecordEnabled: user.ShouldRecord,
			RecordingState:    domain.RecordingStateIdle,
		},
		done: make(chan struct{}),
	}
}

// Start begins the monitoring goroutine.
func (m *UserMonitor) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		cancel()
		return
	}
	m.started = true
	m.cancel = cancel
	m.mu.Unlock()

	go m.loop(ctx)
}

// Stop cleanly stops the monitor. A monitor that was never started has
// nothing to wait for.
func (m *UserMonitor) Stop() {
	m.stoppedOnce.Do(func() {
		m.mu.Lock()
		cancel := m.cancel
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})

	m.mu.RLock()
	started := m.started
	m.mu.RUnlock()
	if started {
		<-m.done
	}
}

// Status returns a snapshot of the current user status.
func (m *UserMonitor) Status() domain.UserStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// SetAutoRecord updates the auto-recording preference.
func (m *UserMonitor) SetAutoRecord(enabled bool) {
	m.mu.Lock()
	m.user.ShouldRecord = enabled
	prev := m.status
	m.status.AutoRecordEnabled = enabled
	curr := m.status
	m.mu.Unlock()
	if m.callback != nil && prev.AutoRecordEnabled != curr.AutoRecordEnabled {
		m.callback.OnUserStatusChanged(prev, curr)
	}
}

// SetAlias updates the user's alias.
func (m *UserMonitor) SetAlias(alias string) {
	m.mu.Lock()
	m.user.Alias = alias
	prev := m.status
	m.status.Alias = alias
	curr := m.status
	m.mu.Unlock()

	if m.callback != nil && prev.Alias != curr.Alias {
		m.callback.OnUserStatusChanged(prev, curr)
	}
}

// SetRecordingState updates the observed recording state for this user.
func (m *UserMonitor) SetRecordingState(state domain.RecordingState, sessionID string) {
	m.mu.Lock()
	prev := m.status
	m.status.RecordingState = state
	m.status.ActiveSessionID = sessionID
	curr := m.status
	m.mu.Unlock()

	if m.callback != nil && (prev.RecordingState != curr.RecordingState || prev.ActiveSessionID != curr.ActiveSessionID) {
		m.callback.OnUserStatusChanged(prev, curr)
	}
}

// PollOnce executes a single live query against the platform.
func (m *UserMonitor) PollOnce(ctx context.Context) {
	m.mu.RLock()
	user := m.user
	prev := m.status
	prevRoomID := m.lastRoomID
	m.mu.RUnlock()

	if prev.RecordingState == domain.RecordingStateRecording {
		return
	}

	liveStatus, err := m.client.GetLiveStatus(ctx, user.ID)
	now := time.Now()

	m.mu.Lock()
	if err != nil {
		// A network or timeout error says nothing about the live state: keep
		// the last known state (and room) instead of reporting offline.
		m.status.LastQueryError = err.Error()

		curr := m.status
		m.mu.Unlock()

		if m.callback != nil {
			m.callback.OnQueryError(user, err)
			m.callback.OnUserStatusChanged(prev, curr)
		}
		return
	}

	m.status.LastQuerySuccess = &now
	m.status.LastQueryError = ""

	var roomIDChanged bool
	var newLiveDetected bool

	if liveStatus.Status == douyin.StatusLive {
		m.status.LiveState = domain.LiveStateLive
		m.status.RoomID = liveStatus.RoomID
		if prevRoomID != liveStatus.RoomID {
			m.lastRoomID = liveStatus.RoomID
			roomIDChanged = true
		}
		if prev.LiveState != domain.LiveStateLive || roomIDChanged {
			newLiveDetected = true
		}
	} else {
		m.status.LiveState = domain.LiveStateOffline
		m.status.RoomID = ""
		m.lastRoomID = ""
	}
	curr := m.status
	m.mu.Unlock()

	if m.callback != nil {
		if prev.LiveState != curr.LiveState || prev.RoomID != curr.RoomID {
			m.callback.OnUserStatusChanged(prev, curr)
		}
		if newLiveDetected {
			m.callback.OnLiveDetected(user, liveStatus.RoomID)
		}
	}
}

func (m *UserMonitor) loop(ctx context.Context) {
	defer close(m.done)

	// Poll immediately on start
	m.PollOnce(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.PollOnce(ctx)
		}
	}
}
