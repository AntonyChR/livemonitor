package domain

import (
	"time"
)

// LiveState represents the streaming status of a user.
type LiveState string

const (
	LiveStateUnknown LiveState = "unknown"
	LiveStateOffline LiveState = "offline"
	LiveStateLive    LiveState = "live"
)

// RecordingState represents the state of a recording session.
type RecordingState string

const (
	RecordingStateIdle      RecordingState = "idle"
	RecordingStateStarting  RecordingState = "starting"
	RecordingStateRecording RecordingState = "recording"
	RecordingStateStopping  RecordingState = "stopping"
	RecordingStateFailed    RecordingState = "failed"
)

// RecordingOrigin specifies whether a recording was triggered automatically or manually.
type RecordingOrigin string

const (
	RecordingOriginAuto   RecordingOrigin = "auto"
	RecordingOriginManual RecordingOrigin = "manual"
)

// UserConfig defines the configuration for a monitored user.
type UserConfig struct {
	ID           string `json:"id"`
	Alias        string `json:"alias"`
	ShouldRecord bool   `json:"should_record"`
}

// UserStatus represents the complete runtime status of a user.
type UserStatus struct {
	ID                string         `json:"id"`
	Alias             string         `json:"alias"`
	LiveState         LiveState      `json:"live_state"`
	RoomID            string         `json:"room_id,omitempty"`
	LastQuerySuccess  *time.Time     `json:"last_query_success,omitempty"`
	LastQueryError    string         `json:"last_query_error,omitempty"`
	AutoRecordEnabled bool           `json:"auto_record_enabled"`
	RecordingState    RecordingState `json:"recording_state"`
	ActiveSessionID   string         `json:"active_session_id,omitempty"`
}

// RecordingSessionInfo contains observable information about an active or completed recording session.
type RecordingSessionInfo struct {
	SessionID     string          `json:"session_id"`
	UserID        string          `json:"user_id"`
	Alias         string          `json:"alias"`
	LiveURL       string          `json:"live_url"`
	OutputPath    string          `json:"output_path"`
	State         RecordingState  `json:"state"`
	Origin        RecordingOrigin `json:"origin"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	MediaTimeUs   int64           `json:"media_time_us"`
	FailureReason string          `json:"failure_reason,omitempty"`
}

// BackendState represents the lifecycle status of the backend service.
type BackendState string

const (
	BackendStateStarting BackendState = "starting"
	BackendStateRunning  BackendState = "running"
	BackendStateStopping BackendState = "stopping"
	BackendStateStopped  BackendState = "stopped"
)

// GlobalStatus represents a complete snapshot of the backend state.
type GlobalStatus struct {
	State            BackendState           `json:"state"`
	Users            []UserStatus           `json:"users"`
	ActiveRecordings []RecordingSessionInfo `json:"active_recordings"`
	Timestamp        time.Time              `json:"timestamp"`
}
