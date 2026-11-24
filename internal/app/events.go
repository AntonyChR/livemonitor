package app

import (
	"sync"
	"sync/atomic"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
)

// EventType is an identifier for distinct domain events.
type EventType string

const (
	EventUserStatusChanged    EventType = "user_status_changed"
	EventRecordingStarted     EventType = "recording_started"
	EventRecordingStopping    EventType = "recording_stopping"
	EventRecordingStopped     EventType = "recording_stopped"
	EventRecordingFailed      EventType = "recording_failed"
	EventConfigurationChanged EventType = "configuration_changed"
	EventMonitorError         EventType = "monitor_error"
	EventBackendStatusChanged EventType = "backend_status_changed"
)

// Event is the interface implemented by all domain event structures.
type Event interface {
	Type() EventType
	Timestamp() time.Time
}

type BaseEvent struct {
	EventType EventType `json:"type"`
	Time      time.Time `json:"timestamp"`
}

func (b BaseEvent) Type() EventType      { return b.EventType }
func (b BaseEvent) Timestamp() time.Time { return b.Time }

// UserStatusChangedEvent is emitted whenever a user's streaming or recording status changes.
type UserStatusChangedEvent struct {
	BaseEvent
	UserID   string            `json:"user_id"`
	Alias    string            `json:"alias"`
	Previous domain.UserStatus `json:"previous"`
	Current  domain.UserStatus `json:"current"`
}

// RecordingStartedEvent is emitted when video progress is first detected and confirmed.
type RecordingStartedEvent struct {
	BaseEvent
	Session domain.RecordingSessionInfo `json:"session"`
}

// RecordingStoppingEvent is emitted when a recording enters graceful shutdown.
type RecordingStoppingEvent struct {
	BaseEvent
	Session domain.RecordingSessionInfo `json:"session"`
}

// RecordingStoppedEvent is emitted when a recording has completed and finalized cleanly.
type RecordingStoppedEvent struct {
	BaseEvent
	Session domain.RecordingSessionInfo `json:"session"`
}

// RecordingFailedEvent is emitted when a recording fails or encounters an unrecoverable error.
type RecordingFailedEvent struct {
	BaseEvent
	Session domain.RecordingSessionInfo `json:"session"`
	Reason  string                      `json:"reason"`
}

// ConfigurationChangedEvent is emitted when users or settings are updated.
type ConfigurationChangedEvent struct {
	BaseEvent
	Users    []domain.UserConfig `json:"users"`
	Settings config.Settings     `json:"settings"`
}

// MonitorErrorEvent is emitted when an error occurs while polling a user's status.
type MonitorErrorEvent struct {
	BaseEvent
	UserID string `json:"user_id"`
	Alias  string `json:"alias"`
	ErrorMsg  string `json:"error"`
}

func (m MonitorErrorEvent) Error() string {
	return m.ErrorMsg
}

// BackendStatusChangedEvent is emitted when the backend transitions between lifecycle states.
type BackendStatusChangedEvent struct {
	BaseEvent
	Previous domain.BackendState `json:"previous"`
	Current  domain.BackendState `json:"current"`
}

type subscriber struct {
	id           uint64
	ch           chan Event
	droppedCount atomic.Uint64
}

// EventBroker coordinates thread-safe, non-blocking publishing and subscription of events.
type EventBroker struct {
	mu          sync.RWMutex
	subscribers map[uint64]*subscriber
	nextID      uint64
	closed      bool
}

// NewEventBroker creates an EventBroker.
func NewEventBroker() *EventBroker {
	return &EventBroker{
		subscribers: make(map[uint64]*subscriber),
	}
}

// Publish broadcasts an event to all active subscribers.
// If a subscriber channel is full, the event is dropped for that subscriber
// without blocking the publisher, and the subscriber's dropped counter is incremented.
func (b *EventBroker) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, sub := range b.subscribers {
		select {
		case sub.ch <- event:
		default:
			sub.droppedCount.Add(1)
		}
	}
}

// Subscribe registers a new subscriber with a buffered event channel and an unsubscribe func.
func (b *EventBroker) Subscribe(bufferSize int) (<-chan Event, func()) {
	if bufferSize <= 0 {
		bufferSize = 100
	}

	ch := make(chan Event, bufferSize)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	id := b.nextID
	sub := &subscriber{id: id, ch: ch}
	b.subscribers[id] = sub

	var unsubOnce sync.Once
	unsubscribe := func() {
		unsubOnce.Do(func() {
			b.mu.Lock()
			if _, exists := b.subscribers[id]; exists {
				delete(b.subscribers, id)
				close(ch)
			}
			b.mu.Unlock()
		})
	}

	return ch, unsubscribe
}

// Close shuts down the broker and closes all subscriber channels.
func (b *EventBroker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	for id, sub := range b.subscribers {
		close(sub.ch)
		delete(b.subscribers, id)
	}
}
