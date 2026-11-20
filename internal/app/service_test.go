package app

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
	"live-recorder/internal/platform/douyin"
	"live-recorder/internal/recorder"
)

type mockDouyinClient struct {
	mu        sync.Mutex
	statusMap map[string]douyin.LiveStatus
}

func (m *mockDouyinClient) GetLiveStatus(ctx context.Context, userID string) (douyin.LiveStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.statusMap[userID]; ok {
		return st, nil
	}
	return douyin.LiveStatus{Status: douyin.StatusOffline}, nil
}

func (m *mockDouyinClient) GetLiveURL(ctx context.Context, roomID string) (string, error) {
	return "https://live.douyin.com/" + roomID, nil
}

type mockRunner struct{}

func (r *mockRunner) StartPipeline(prodArgs, consArgs []string) (recorder.ProcessPipeline, error) {
	return &mockPipeline{done: make(chan struct{})}, nil
}

type mockPipeline struct {
	done chan struct{}
	once sync.Once
}

func (p *mockPipeline) Producer() recorder.ProcessHandle { return nil }
func (p *mockPipeline) Consumer() recorder.ProcessHandle { return nil }
func (p *mockPipeline) ProgressReader() io.Reader        { return nil }
func (p *mockPipeline) StopOrderly(prodTimeout, f, t, k time.Duration) error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *mockPipeline) Wait() (recorder.ProcessResult, recorder.ProcessResult) {
	<-p.done
	return recorder.ProcessResult{}, recorder.ProcessResult{}
}
func (p *mockPipeline) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func TestApplicationService_LifecycleAndSnapshot(t *testing.T) {
	client := &mockDouyinClient{statusMap: make(map[string]douyin.LiveStatus)}
	opts := config.DefaultSettings()
	opts.DefaultCheckLiveStatusInterval = 1 // 1 second interval

	users := []domain.UserConfig{
		{ID: "101", Alias: "Alice", ShouldRecord: true},
		{ID: "102", Alias: "Bob", ShouldRecord: false},
	}

	svc := NewApplicationService(opts, client, &mockRunner{}, users)

	ctx := context.Background()

	// Initial snapshot before monitoring
	snap := svc.GetGlobalStatus(ctx)
	if snap.State != domain.BackendStateStarting {
		t.Errorf("expected starting state, got %s", snap.State)
	}
	if len(snap.Users) != 2 {
		t.Errorf("expected 2 users in snapshot, got %d", len(snap.Users))
	}

	// Subscribe to events
	eventCh, unsub, subSnapshot := svc.Subscribe(ctx, 50)
	defer unsub()

	if len(subSnapshot.Users) != 2 {
		t.Errorf("expected 2 users in subscribe snapshot, got %d", len(subSnapshot.Users))
	}

	// Start monitoring
	err := svc.StartMonitoring(ctx)
	if err != nil {
		t.Fatalf("StartMonitoring failed: %v", err)
	}

	// Receive BackendStatusChangedEvent
	select {
	case ev := <-eventCh:
		if ev.Type() != EventBackendStatusChanged {
			t.Errorf("expected EventBackendStatusChanged, got %s", ev.Type())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for backend status event")
	}

	// Set auto record
	err = svc.SetAutoRecord(ctx, "102", true)
	if err != nil {
		t.Fatalf("SetAutoRecord failed: %v", err)
	}

	// Verify ConfigurationChangedEvent
	foundConfigEvent := false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case ev := <-eventCh:
			if ev.Type() == EventConfigurationChanged {
				foundConfigEvent = true
				break
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
		if foundConfigEvent {
			break
		}
	}
	if !foundConfigEvent {
		t.Error("expected EventConfigurationChanged")
	}

	// Verify Idempotent Shutdown
	err = svc.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	err = svc.Shutdown(ctx)
	if err != nil {
		t.Fatalf("second Shutdown call should be idempotent, got: %v", err)
	}

	finalSnap := svc.GetGlobalStatus(ctx)
	if finalSnap.State != domain.BackendStateStopped {
		t.Errorf("expected stopped state, got %s", finalSnap.State)
	}
}

func TestApplicationService_SlowConsumerNonBlocking(t *testing.T) {
	broker := NewEventBroker()
	defer broker.Close()

	// Small buffer of 1 event
	eventCh, unsub := broker.Subscribe(1)
	defer unsub()

	// Publish 10 events rapidly
	for i := 0; i < 10; i++ {
		broker.Publish(BackendStatusChangedEvent{
			BaseEvent: BaseEvent{
				EventType: EventBackendStatusChanged,
				Time:      time.Now(),
			},
			Previous: domain.BackendStateStarting,
			Current:  domain.BackendStateRunning,
		})
	}

	// Subscriber should have 1 event and not have blocked the loop
	select {
	case ev := <-eventCh:
		if ev.Type() != EventBackendStatusChanged {
			t.Errorf("expected EventBackendStatusChanged, got %s", ev.Type())
		}
	default:
		t.Fatal("expected at least 1 event in buffer")
	}

	// Next read should be empty or have dropped the overflow
	select {
	case <-eventCh:
	default:
		// Dropped excess events non-blockingly, which is correct
	}
}
