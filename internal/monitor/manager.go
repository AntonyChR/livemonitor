package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"live-recorder/internal/domain"
	"live-recorder/internal/platform/douyin"
	"live-recorder/internal/recorder"
)

// MonitorEventHandler receives events triggered by the monitoring manager.
type MonitorEventHandler interface {
	OnUserStatusChanged(prev domain.UserStatus, curr domain.UserStatus)
	OnMonitorError(user domain.UserConfig, err error)
	OnLiveDetected(user domain.UserConfig, roomID string)
}

// Manager coordinates all UserMonitors, synchronizes configurations, and triggers recordings.
type Manager struct {
	mu            sync.RWMutex
	monitors      map[string]*UserMonitor
	users         map[string]domain.UserConfig
	notifiedRooms map[string]string
	client        douyin.Client
	recordings    *recorder.Manager
	interval      time.Duration
	eventHandler  MonitorEventHandler
	ctx           context.Context
	cancel        context.CancelFunc
	closing       bool
}

// NewManager creates a new Monitor Manager.
func NewManager(
	client douyin.Client,
	recordings *recorder.Manager,
	interval time.Duration,
	eventHandler MonitorEventHandler,
) *Manager {
	return &Manager{
		monitors:      make(map[string]*UserMonitor),
		users:         make(map[string]domain.UserConfig),
		notifiedRooms: make(map[string]string),
		client:        client,
		recordings:    recordings,
		interval:      interval,
		eventHandler:  eventHandler,
	}
}

// Start starts the monitor manager with a parent context.
func (m *Manager) Start(parentCtx context.Context) {
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(parentCtx)
	m.closing = false

	for _, mon := range m.monitors {
		mon.Start(m.ctx)
	}
	m.mu.Unlock()
}

// Stop cleanly terminates all user monitors.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.closing = true
	if m.cancel != nil {
		m.cancel()
	}
	var mons []*UserMonitor
	for _, mon := range m.monitors {
		mons = append(mons, mon)
	}
	m.mu.Unlock()

	for _, mon := range mons {
		mon.Stop()
	}
}

// SyncUsers dynamically updates the list of monitored users:
// - Adds monitors for new users
// - Updates alias and shouldRecord for existing users
// - Stops and removes monitors for removed users
// - Note: Removing a user does NOT stop any active recording session.
func (m *Manager) SyncUsers(userList []domain.UserConfig) {
	type userUpdate struct {
		mon          *UserMonitor
		aliasChanged bool
		newAlias     string
		autoChanged  bool
		newAuto      bool
	}
	var updates []userUpdate
	var monsToStop []*UserMonitor

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}

	activeIDs := make(map[string]struct{}, len(userList))
	for _, user := range userList {
		activeIDs[user.ID] = struct{}{}
		existingMon, exists := m.monitors[user.ID]

		if !exists {
			m.users[user.ID] = user
			mon := NewUserMonitor(user, m.client, m.interval, m)
			m.monitors[user.ID] = mon
			if m.ctx != nil {
				mon.Start(m.ctx)
			}
		} else {
			oldUser := m.users[user.ID]
			m.users[user.ID] = user
			upd := userUpdate{mon: existingMon}
			if oldUser.Alias != user.Alias {
				upd.aliasChanged = true
				upd.newAlias = user.Alias
			}
			if oldUser.ShouldRecord != user.ShouldRecord {
				upd.autoChanged = true
				upd.newAuto = user.ShouldRecord
			}
			if upd.aliasChanged || upd.autoChanged {
				updates = append(updates, upd)
			}
		}
	}

	// Remove deleted users
	for id, mon := range m.monitors {
		if _, keep := activeIDs[id]; !keep {
			monsToStop = append(monsToStop, mon)
			delete(m.monitors, id)
			delete(m.users, id)
			delete(m.notifiedRooms, id)
		}
	}
	m.mu.Unlock()

	// Apply updates to existing monitors outside of lock to prevent re-entrant deadlock with OnUserStatusChanged
	for _, upd := range updates {
		if upd.aliasChanged {
			upd.mon.SetAlias(upd.newAlias)
		}
		if upd.autoChanged {
			upd.mon.SetAutoRecord(upd.newAuto)
		}
	}

	// Stop deleted monitors outside of lock to avoid blocking while holding m.mu
	for _, mon := range monsToStop {
		mon.Stop()
	}
}

// GetUserStatus returns the status of a specific user.
func (m *Manager) GetUserStatus(userID string) (domain.UserStatus, error) {
	m.mu.RLock()
	mon, exists := m.monitors[userID]
	m.mu.RUnlock()

	if !exists {
		return domain.UserStatus{}, fmt.Errorf("usuario no encontrado: %s", userID)
	}
	return mon.Status(), nil
}

// GetAllUserStatuses returns the current status of all monitored users.
func (m *Manager) GetAllUserStatuses() []domain.UserStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]domain.UserStatus, 0, len(m.monitors))
	for _, mon := range m.monitors {
		statuses = append(statuses, mon.Status())
	}
	return statuses
}

// SetAutoRecord modifies the automatic recording preference for a user.
func (m *Manager) SetAutoRecord(userID string, enabled bool) error {
	m.mu.Lock()
	mon, exists := m.monitors[userID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("usuario no encontrado: %s", userID)
	}
	user := m.users[userID]
	user.ShouldRecord = enabled
	m.users[userID] = user
	m.mu.Unlock()

	mon.SetAutoRecord(enabled)
	status := mon.Status()

	// If enabled and user is currently live and not recording, trigger auto recording
	if enabled && status.LiveState == domain.LiveStateLive && status.RoomID != "" && !m.recordings.Has(userID) {
		m.triggerRecording(user, status.RoomID, domain.RecordingOriginAuto)
	}

	return nil
}

// StartRecording manually initiates a recording for a user.
func (m *Manager) StartRecording(userID string) (string, error) {
	m.mu.RLock()
	user, exists := m.users[userID]
	mon, monExists := m.monitors[userID]
	m.mu.RUnlock()

	if !exists || !monExists {
		return "", fmt.Errorf("usuario no encontrado: %s", userID)
	}

	status := mon.Status()
	if status.LiveState != domain.LiveStateLive {
		return "", fmt.Errorf("el usuario %s (%s) no está transmitiendo en directo actualmente", user.Alias, userID)
	}
	if status.RoomID == "" {
		return "", fmt.Errorf("el usuario %s (%s) no tiene una sala de directo asociada", user.Alias, userID)
	}
	if m.recordings.Has(userID) {
		return "", fmt.Errorf("ya existe una sesión de grabación activa para %s (%s)", user.Alias, userID)
	}

	return m.triggerRecording(user, status.RoomID, domain.RecordingOriginManual)
}

// StopRecording terminates an active recording session for a user.
func (m *Manager) StopRecording(userID string) error {
	m.mu.RLock()
	mon, exists := m.monitors[userID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("usuario no encontrado: %s", userID)
	}

	err := m.recordings.Stop(userID)
	if err != nil {
		return err
	}

	mon.SetRecordingState(domain.RecordingStateStopping, "")
	return nil
}

func (m *Manager) triggerRecording(user domain.UserConfig, roomID string, origin domain.RecordingOrigin) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	liveURL, err := m.client.GetLiveURL(ctx, roomID)
	if err != nil {
		slog.Error("Error al obtener URL del directo",
			"user_id", user.ID,
			"alias", user.Alias,
			"room_id", roomID,
			"error", err,
		)
		return "", fmt.Errorf("error al obtener URL del directo para %s: %w", user.Alias, err)
	}
	if liveURL == "" {
		slog.Error("No se encontró URL de directo para el usuario",
			"user_id", user.ID,
			"alias", user.Alias,
			"room_id", roomID,
		)
		return "", fmt.Errorf("no se encontró URL de directo para %s", user.Alias)
	}

	session, err := m.recordings.Start(user, liveURL, origin)
	if err != nil {
		slog.Error("No se pudo iniciar la grabación",
			"user_id", user.ID,
			"alias", user.Alias,
			"error", err,
		)
		return "", err
	}

	m.mu.RLock()
	mon := m.monitors[user.ID]
	m.mu.RUnlock()
	if mon != nil {
		mon.SetRecordingState(domain.RecordingStateStarting, session.SessionID)
	}

	return session.SessionID, nil
}

// Implementation of MonitorCallback:

func (m *Manager) OnUserStatusChanged(prev domain.UserStatus, curr domain.UserStatus) {
	if prev.LiveState != domain.LiveStateOffline && curr.LiveState == domain.LiveStateOffline {
		m.mu.Lock()
		delete(m.notifiedRooms, curr.ID)
		m.mu.Unlock()
	}
	if m.eventHandler != nil {
		m.eventHandler.OnUserStatusChanged(prev, curr)
	}
}

func (m *Manager) OnQueryError(user domain.UserConfig, err error) {
	if m.eventHandler != nil {
		m.eventHandler.OnMonitorError(user, err)
	}
}

func (m *Manager) OnLiveDetected(user domain.UserConfig, roomID string) {
	m.mu.Lock()
	prevRoomID, exists := m.notifiedRooms[user.ID]
	m.notifiedRooms[user.ID] = roomID
	mon := m.monitors[user.ID]
	m.mu.Unlock()

	// Notify if it's a new live room
	if !exists || prevRoomID != roomID {
		if m.eventHandler != nil {
			m.eventHandler.OnLiveDetected(user, roomID)
		}
	}

	// Check auto record condition
	if mon != nil {
		status := mon.Status()
		if status.AutoRecordEnabled && !m.recordings.Has(user.ID) {
			go func() {
				_, _ = m.triggerRecording(user, roomID, domain.RecordingOriginAuto)
			}()
		}
	}
}

// UpdateRecordingState updates the recording state inside a UserMonitor when recording lifecycle events occur.
func (m *Manager) UpdateRecordingState(userID string, state domain.RecordingState, sessionID string) {
	m.mu.RLock()
	mon, exists := m.monitors[userID]
	m.mu.RUnlock()

	if exists && mon != nil {
		mon.SetRecordingState(state, sessionID)
	}
}
