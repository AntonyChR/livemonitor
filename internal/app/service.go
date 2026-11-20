package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
	"live-recorder/internal/monitor"
	"live-recorder/internal/platform/douyin"
	"live-recorder/internal/recorder"
)

// Service defines the stable application façade interface.
type Service interface {
	GetGlobalStatus(ctx context.Context) domain.GlobalStatus
	GetUserStatus(ctx context.Context, userID string) (domain.UserStatus, error)
	SetAutoRecord(ctx context.Context, userID string, enabled bool) error
	StartRecording(ctx context.Context, userID string) (string, error)
	StopRecording(ctx context.Context, userID string) error
	StartMonitoring(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Subscribe(ctx context.Context, bufferSize int) (<-chan Event, func(), domain.GlobalStatus)
	ReloadUsers(ctx context.Context, users []domain.UserConfig)
	AddUser(ctx context.Context, user domain.UserConfig) error
	GetSettings() config.Settings
	UpdateSettings(settings config.Settings) error
	Summary() string
}

type applicationService struct {
	mu           sync.RWMutex
	persistMu    sync.Mutex
	state        domain.BackendState
	settings     config.Settings
	broker       *EventBroker
	monitors     *monitor.Manager
	recordings   *recorder.Manager
	client       douyin.Client
	users        []domain.UserConfig
	ctx          context.Context
	cancel       context.CancelFunc
	shutdownOnce sync.Once
}

// NewApplicationService initializes the application components.
func NewApplicationService(
	settings config.Settings,
	client douyin.Client,
	runner recorder.ProcessRunner,
	initialUsers []domain.UserConfig,
) Service {
	broker := NewEventBroker()

	svc := &applicationService{
		state:    domain.BackendStateStarting,
		settings: settings,
		broker:   broker,
		client:   client,
		users:    initialUsers,
	}

	// Create RecordingManager with SessionEventListener hooked into EventBroker
	recorders := recorder.NewManager(settings, runner, svc)
	svc.recordings = recorders

	// Create MonitorManager with MonitorEventHandler hooked into EventBroker
	monitors := monitor.NewManager(client, recorders, time.Duration(settings.DefaultCheckLiveStatusInterval)*time.Second, svc)
	svc.monitors = monitors

	if len(initialUsers) > 0 {
		monitors.SyncUsers(initialUsers)
	}

	return svc
}

// GetGlobalStatus returns an atomic snapshot of the entire backend state.
func (s *applicationService) GetGlobalStatus(ctx context.Context) domain.GlobalStatus {
	s.mu.RLock()
	state := s.state
	s.mu.RUnlock()

	users := s.monitors.GetAllUserStatuses()
	activeRecordings := s.recordings.ActiveSessions()

	return domain.GlobalStatus{
		State:            state,
		Users:            users,
		ActiveRecordings: activeRecordings,
		Timestamp:        time.Now(),
	}
}

// GetUserStatus returns the status of a specific monitored user.
func (s *applicationService) GetUserStatus(ctx context.Context, userID string) (domain.UserStatus, error) {
	return s.monitors.GetUserStatus(userID)
}

// SetAutoRecord modifies auto-record configuration for a user.
func (s *applicationService) SetAutoRecord(ctx context.Context, userID string, enabled bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.Lock()
	for i, u := range s.users {
		if u.ID == userID {
			s.users[i].ShouldRecord = enabled
			break
		}
	}
	users := make([]domain.UserConfig, len(s.users))
	copy(users, s.users)
	userListFile := s.settings.DefaultUserListFile
	s.mu.Unlock()

	err := s.monitors.SetAutoRecord(userID, enabled)
	if err != nil {
		return err
	}

	err = config.SaveUsersAtomic(userListFile, users)
	if err != nil {
		return err
	}

	s.publishConfigurationChanged()

	return nil
}

// StartRecording manually requests a recording for a user.
func (s *applicationService) StartRecording(ctx context.Context, userID string) (string, error) {
	s.mu.RLock()
	state := s.state
	s.mu.RUnlock()

	if state != domain.BackendStateRunning {
		return "", errors.New("el servicio no se encuentra en ejecución")
	}

	return s.monitors.StartRecording(userID)
}

// StopRecording requests manual stop of a recording for a user.
func (s *applicationService) StopRecording(ctx context.Context, userID string) error {
	return s.monitors.StopRecording(userID)
}

// StartMonitoring begins user polling and monitoring.
func (s *applicationService) StartMonitoring(ctx context.Context) error {
	s.mu.Lock()
	if s.state == domain.BackendStateRunning {
		s.mu.Unlock()
		return nil
	}
	if s.state == domain.BackendStateStopping || s.state == domain.BackendStateStopped {
		s.mu.Unlock()
		return errors.New("no se puede iniciar el monitoreo en un servicio cerrado")
	}

	prevState := s.state
	s.state = domain.BackendStateRunning
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	s.monitors.Start(s.ctx)

	s.broker.Publish(BackendStatusChangedEvent{
		BaseEvent: BaseEvent{
			EventType: EventBackendStatusChanged,
			Time:      time.Now(),
		},
		Previous: prevState,
		Current:  domain.BackendStateRunning,
	})

	return nil
}

// Shutdown initiates graceful termination of all monitors and active recordings.
func (s *applicationService) Shutdown(ctx context.Context) error {
	var err error
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		prevState := s.state
		s.state = domain.BackendStateStopping
		s.mu.Unlock()

		s.broker.Publish(BackendStatusChangedEvent{
			BaseEvent: BaseEvent{
				EventType: EventBackendStatusChanged,
				Time:      time.Now(),
			},
			Previous: prevState,
			Current:  domain.BackendStateStopping,
		})

		slog.Info("Iniciando apagado ordenado del servicio...")

		// Stop monitoring first to prevent new recordings
		s.monitors.Stop()
		if s.cancel != nil {
			s.cancel()
		}

		// Shutdown all recordings gracefully
		err = s.recordings.Shutdown(ctx)

		s.mu.Lock()
		s.state = domain.BackendStateStopped
		s.mu.Unlock()

		s.broker.Publish(BackendStatusChangedEvent{
			BaseEvent: BaseEvent{
				EventType: EventBackendStatusChanged,
				Time:      time.Now(),
			},
			Previous: domain.BackendStateStopping,
			Current:  domain.BackendStateStopped,
		})

		s.broker.Close()
		slog.Info("Apagado ordenado finalizado")
	})

	return err
}

// Subscribe returns an event channel, an unsubscribe function, and the current initial snapshot.
func (s *applicationService) Subscribe(ctx context.Context, bufferSize int) (<-chan Event, func(), domain.GlobalStatus) {
	ch, unsub := s.broker.Subscribe(bufferSize)
	snapshot := s.GetGlobalStatus(ctx)

	// If context is canceled, unsubscribe automatically
	go func() {
		select {
		case <-ctx.Done():
			unsub()
		case <-s.brokerDone():
		}
	}()

	return ch, unsub, snapshot
}

func (s *applicationService) brokerDone() <-chan struct{} {
	ch := make(chan struct{})
	return ch
}

// ReloadUsers synchronizes the list of monitored users from config.
func (s *applicationService) ReloadUsers(ctx context.Context, users []domain.UserConfig) {
	s.mu.Lock()
	s.users = users
	s.mu.Unlock()
	s.monitors.SyncUsers(users)

	s.publishConfigurationChanged()
}

// AddUser validates, persists, and starts monitoring a new user.
func (s *applicationService) AddUser(ctx context.Context, user domain.UserConfig) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	user.ID = strings.TrimSpace(user.ID)
	user.Alias = strings.TrimSpace(user.Alias)
	if err := config.ValidateUser(user); err != nil {
		return err
	}

	s.mu.Lock()
	for _, existing := range s.users {
		if existing.ID == user.ID {
			s.mu.Unlock()
			return fmt.Errorf("ya existe un usuario con ID %s", user.ID)
		}
	}

	updated := make([]domain.UserConfig, len(s.users), len(s.users)+1)
	copy(updated, s.users)
	updated = append(updated, user)
	settings := s.settings

	if err := config.SaveUsersAtomic(settings.DefaultUserListFile, updated); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("no se pudo guardar la lista de usuarios: %w", err)
	}

	s.users = updated
	s.mu.Unlock()

	s.monitors.SyncUsers(updated)
	s.publishConfigurationChanged()

	return nil
}

// GetSettings returns a snapshot of the current application settings.
func (s *applicationService) GetSettings() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// UpdateSettings validates, persists, and applies new recording and user
// list settings. Existing recordings keep the options they were created with.
func (s *applicationService) UpdateSettings(settings config.Settings) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	settings.OutputDirectory = config.ExpandHome(settings.OutputDirectory)
	settings.DefaultUserListFile = config.ExpandHome(settings.DefaultUserListFile)
	settings.Quality = strings.TrimSpace(settings.Quality)
	if settings.OutputDirectory == "" {
		return errors.New("la carpeta de grabaciones no puede estar vacía")
	}
	if settings.Quality == "" {
		return errors.New("la calidad de grabación no puede estar vacía")
	}
	if settings.DefaultUserListFile == "" {
		return errors.New("la ruta del archivo de usuarios no puede estar vacía")
	}
	if err := os.MkdirAll(settings.OutputDirectory, 0755); err != nil {
		return fmt.Errorf("no se pudo crear la carpeta de grabaciones: %w", err)
	}

	s.mu.RLock()
	current := s.settings
	s.mu.RUnlock()

	settings.SettingsFile = current.SettingsFile
	userListChanged := settings.DefaultUserListFile != current.DefaultUserListFile

	var users []domain.UserConfig
	if userListChanged {
		var err error
		users, err = config.ReadUsersFromFile(settings.DefaultUserListFile)
		if err != nil {
			return fmt.Errorf("no se pudo leer el archivo de usuarios %s: %w", settings.DefaultUserListFile, err)
		}
	}

	if settings.SettingsFile != "" {
		if err := config.SaveSettings(settings.SettingsFile, settings); err != nil {
			return fmt.Errorf("no se pudo guardar la configuración: %w", err)
		}
	}

	s.mu.Lock()
	s.settings = settings
	if userListChanged {
		s.users = users
	}
	s.mu.Unlock()

	s.recordings.UpdateSettings(settings)
	if userListChanged {
		s.monitors.SyncUsers(users)
	}
	s.publishConfigurationChanged()

	return nil
}

func (s *applicationService) publishConfigurationChanged() {
	s.mu.RLock()
	users := make([]domain.UserConfig, len(s.users))
	copy(users, s.users)
	settings := s.settings
	s.mu.RUnlock()

	s.broker.Publish(ConfigurationChangedEvent{
		BaseEvent: BaseEvent{
			EventType: EventConfigurationChanged,
			Time:      time.Now(),
		},
		Users:    users,
		Settings: settings,
	})
}

// Summary returns a human-readable summary of active recordings.
func (s *applicationService) Summary() string {
	return s.recordings.Summary()
}

// Implementation of recorder.SessionEventListener:

func (s *applicationService) OnRecordingStarted(info domain.RecordingSessionInfo) {
	s.monitors.UpdateRecordingState(info.UserID, domain.RecordingStateRecording, info.SessionID)

	s.broker.Publish(RecordingStartedEvent{
		BaseEvent: BaseEvent{
			EventType: EventRecordingStarted,
			Time:      time.Now(),
		},
		Session: info,
	})
}

func (s *applicationService) OnRecordingStopping(info domain.RecordingSessionInfo) {
	s.monitors.UpdateRecordingState(info.UserID, domain.RecordingStateStopping, info.SessionID)

	s.broker.Publish(RecordingStoppingEvent{
		BaseEvent: BaseEvent{
			EventType: EventRecordingStopping,
			Time:      time.Now(),
		},
		Session: info,
	})
}

func (s *applicationService) OnRecordingStopped(info domain.RecordingSessionInfo) {
	s.monitors.UpdateRecordingState(info.UserID, domain.RecordingStateIdle, "")

	s.broker.Publish(RecordingStoppedEvent{
		BaseEvent: BaseEvent{
			EventType: EventRecordingStopped,
			Time:      time.Now(),
		},
		Session: info,
	})
}

func (s *applicationService) OnRecordingFailed(info domain.RecordingSessionInfo, reason string) {
	s.monitors.UpdateRecordingState(info.UserID, domain.RecordingStateFailed, "")

	s.broker.Publish(RecordingFailedEvent{
		BaseEvent: BaseEvent{
			EventType: EventRecordingFailed,
			Time:      time.Now(),
		},
		Session: info,
		Reason:  reason,
	})
}

// Implementation of monitor.MonitorEventHandler:

func (s *applicationService) OnUserStatusChanged(prev domain.UserStatus, curr domain.UserStatus) {
	s.broker.Publish(UserStatusChangedEvent{
		BaseEvent: BaseEvent{
			EventType: EventUserStatusChanged,
			Time:      time.Now(),
		},
		UserID:   curr.ID,
		Alias:    curr.Alias,
		Previous: prev,
		Current:  curr,
	})
}

func (s *applicationService) OnMonitorError(user domain.UserConfig, err error) {
	s.broker.Publish(MonitorErrorEvent{
		BaseEvent: BaseEvent{
			EventType: EventMonitorError,
			Time:      time.Now(),
		},
		UserID: user.ID,
		Alias:  user.Alias,
		Error:  err.Error(),
	})
}

func (s *applicationService) OnLiveDetected(user domain.UserConfig, roomID string) {
	slog.Info("Usuario en directo detectado",
		"alias", user.Alias,
		"user_id", user.ID,
		"room_id", roomID,
	)
}
