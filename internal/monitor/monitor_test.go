package monitor

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
	"live-recorder/internal/platform/douyin"
	"live-recorder/internal/recorder"
)

// mockDouyinClient implements douyin.Client for monitor tests.
type mockDouyinClient struct {
	mu           sync.Mutex
	statusMap    map[string]douyin.LiveStatus
	errorMap     map[string]error
	liveURLs     map[string]string
	queryCount   map[string]int
}

func newMockDouyinClient() *mockDouyinClient {
	return &mockDouyinClient{
		statusMap:  make(map[string]douyin.LiveStatus),
		errorMap:   make(map[string]error),
		liveURLs:   make(map[string]string),
		queryCount: make(map[string]int),
	}
}

func (m *mockDouyinClient) GetLiveStatus(ctx context.Context, userID string) (douyin.LiveStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queryCount[userID]++

	if err, ok := m.errorMap[userID]; ok && err != nil {
		return douyin.LiveStatus{}, err
	}
	if status, ok := m.statusMap[userID]; ok {
		return status, nil
	}
	return douyin.LiveStatus{Status: douyin.StatusOffline}, nil
}

func (m *mockDouyinClient) GetLiveURL(ctx context.Context, roomID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.liveURLs[roomID]; ok {
		return u, nil
	}
	return "https://live.douyin.com/" + roomID, nil
}

type mockRunner struct{}

func (r *mockRunner) StartPipeline(prodArgs, consArgs []string) (recorder.ProcessPipeline, error) {
	return newMockPipeline(), nil
}

type mockPipeline struct {
	done chan struct{}
	once sync.Once
}

func newMockPipeline() *mockPipeline {
	return &mockPipeline{done: make(chan struct{})}
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

type mockMonitorEventHandler struct {
	mu            sync.Mutex
	statusChanges []struct {
		prev domain.UserStatus
		curr domain.UserStatus
	}
	errors []struct {
		user domain.UserConfig
		err  error
	}
	liveEvents []struct {
		user   domain.UserConfig
		roomID string
	}
}

func (h *mockMonitorEventHandler) OnUserStatusChanged(prev domain.UserStatus, curr domain.UserStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statusChanges = append(h.statusChanges, struct {
		prev domain.UserStatus
		curr domain.UserStatus
	}{prev, curr})
}

func (h *mockMonitorEventHandler) OnMonitorError(user domain.UserConfig, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errors = append(h.errors, struct {
		user domain.UserConfig
		err  error
	}{user, err})
}

func (h *mockMonitorEventHandler) OnLiveDetected(user domain.UserConfig, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.liveEvents = append(h.liveEvents, struct {
		user   domain.UserConfig
		roomID string
	}{user, roomID})
}

func TestUserMonitor_TransitionsAndErrors(t *testing.T) {
	client := newMockDouyinClient()
	client.statusMap["123"] = douyin.LiveStatus{Status: douyin.StatusOffline}

	handler := &mockMonitorEventHandler{}
	user := domain.UserConfig{ID: "123", Alias: "Ana", ShouldRecord: true}
	mon := NewUserMonitor(user, client, 20*time.Millisecond, nil)

	// Initial Poll: Offline
	mon.PollOnce(context.Background())
	if mon.Status().LiveState != domain.LiveStateOffline {
		t.Errorf("expected offline, got %s", mon.Status().LiveState)
	}

	// Transition to Live
	client.mu.Lock()
	client.statusMap["123"] = douyin.LiveStatus{Status: douyin.StatusLive, RoomID: "room999"}
	client.mu.Unlock()

	mon.PollOnce(context.Background())
	if mon.Status().LiveState != domain.LiveStateLive {
		t.Errorf("expected live, got %s", mon.Status().LiveState)
	}
	if mon.Status().RoomID != "room999" {
		t.Errorf("expected room999, got %s", mon.Status().RoomID)
	}

	// Platform error: must PRESERVE LiveStateLive (not set to offline!)
	client.mu.Lock()
	client.errorMap["123"] = errors.New("timeout de red")
	client.mu.Unlock()

	mon.PollOnce(context.Background())
	if mon.Status().LiveState != domain.LiveStateLive {
		t.Errorf("expected live state preserved during error, got %s", mon.Status().LiveState)
	}
	if mon.Status().LastQueryError != "timeout de red" {
		t.Errorf("expected last query error recorded, got %s", mon.Status().LastQueryError)
	}

	// Transition to Offline
	client.mu.Lock()
	client.errorMap["123"] = nil
	client.statusMap["123"] = douyin.LiveStatus{Status: douyin.StatusOffline}
	client.mu.Unlock()

	mon.PollOnce(context.Background())
	if mon.Status().LiveState != domain.LiveStateOffline {
		t.Errorf("expected offline, got %s", mon.Status().LiveState)
	}
	if mon.Status().LastQueryError != "" {
		t.Errorf("expected error cleared, got %s", mon.Status().LastQueryError)
	}
	_ = handler
}

func TestMonitorManager_AutoRecordAndManualStart(t *testing.T) {
	client := newMockDouyinClient()
	opts := config.DefaultSettings()
	runner := &mockRunner{}
	recorders := recorder.NewManager(opts, runner, nil)
	handler := &mockMonitorEventHandler{}

	manager := NewManager(client, recorders, 50*time.Millisecond, handler)

	user1 := domain.UserConfig{ID: "101", Alias: "Alice", ShouldRecord: true}
	user2 := domain.UserConfig{ID: "102", Alias: "Bob", ShouldRecord: false}

	manager.SyncUsers([]domain.UserConfig{user1, user2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	defer manager.Stop()

	// Wait for initial poll to complete
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := manager.GetUserStatus("101")
		if st.LiveState != domain.LiveStateUnknown {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	st1, err := manager.GetUserStatus("101")
	if err != nil || st1.LiveState != domain.LiveStateOffline {
		t.Errorf("expected offline initially, got %v (err: %v)", st1.LiveState, err)
	}

	// Make user1 live -> should trigger auto recording
	client.mu.Lock()
	client.statusMap["101"] = douyin.LiveStatus{Status: douyin.StatusLive, RoomID: "room101"}
	client.mu.Unlock()

	// Wait for monitor loop to pick up live
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := manager.GetUserStatus("101")
		if st.LiveState == domain.LiveStateLive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	st1, _ = manager.GetUserStatus("101")
	if st1.LiveState != domain.LiveStateLive {
		t.Errorf("expected live state for user1, got %s", st1.LiveState)
	}

	// Make user2 live -> Bob has ShouldRecord = false, should NOT auto record
	client.mu.Lock()
	client.statusMap["102"] = douyin.LiveStatus{Status: douyin.StatusLive, RoomID: "room102"}
	client.mu.Unlock()

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := manager.GetUserStatus("102")
		if st.LiveState == domain.LiveStateLive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if recorders.Has("102") {
		t.Error("Bob should not have an active recording since autoRecord was false")
	}

	// Manual start for Bob (who is live)
	sessionID, err := manager.StartRecording("102")
	if err != nil {
		t.Fatalf("unexpected error starting manual recording for Bob: %v", err)
	}
	if sessionID == "" {
		t.Error("expected non-empty session ID")
	}
	if !recorders.Has("102") {
		t.Error("expected active recording for Bob after manual start")
	}

	// Manual stop
	err = manager.StopRecording("102")
	if err != nil {
		t.Fatalf("unexpected error stopping recording: %v", err)
	}
}

func TestMonitorManager_IndependentMonitors(t *testing.T) {
	client := newMockDouyinClient()
	opts := config.DefaultSettings()
	recorders := recorder.NewManager(opts, &mockRunner{}, nil)
	handler := &mockMonitorEventHandler{}

	manager := NewManager(client, recorders, 50*time.Millisecond, handler)

	user1 := domain.UserConfig{ID: "201", Alias: "SlowFailing", ShouldRecord: false}
	user2 := domain.UserConfig{ID: "202", Alias: "Working", ShouldRecord: false}

	// User 201 has persistent network errors
	client.mu.Lock()
	client.errorMap["201"] = errors.New("platform error")
	client.statusMap["202"] = douyin.LiveStatus{Status: douyin.StatusLive, RoomID: "room202"}
	client.mu.Unlock()

	manager.SyncUsers([]domain.UserConfig{user1, user2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	defer manager.Stop()

	// Wait for user 202 to be marked Live despite 201 failing
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := manager.GetUserStatus("202")
		if st.LiveState == domain.LiveStateLive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	st2, _ := manager.GetUserStatus("202")
	if st2.LiveState != domain.LiveStateLive {
		t.Errorf("user 202 should be live despite user 201 failing, got %s", st2.LiveState)
	}

	st1, _ := manager.GetUserStatus("201")
	if st1.LastQueryError == "" {
		t.Error("user 201 should have query error recorded")
	}
}

func TestMonitorManager_SyncUsersAttributeChangeDeadlock(t *testing.T) {
	client := newMockDouyinClient()
	opts := config.DefaultSettings()
	recorders := recorder.NewManager(opts, &mockRunner{}, nil)
	handler := &mockMonitorEventHandler{}

	manager := NewManager(client, recorders, 50*time.Millisecond, handler)

	user1 := domain.UserConfig{ID: "301", Alias: "OriginalAlias", ShouldRecord: false}
	manager.SyncUsers([]domain.UserConfig{user1})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	defer manager.Stop()

	// Wait for initial poll to set user to offline
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := manager.GetUserStatus("301")
		if st.LiveState == domain.LiveStateOffline {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	st, err := manager.GetUserStatus("301")
	if err != nil || st.LiveState != domain.LiveStateOffline {
		t.Fatalf("expected user to be offline, got %s (err: %v)", st.LiveState, err)
	}

	// Now simulate user_list.txt change: update alias and shouldRecord for existing offline user
	user1Updated := domain.UserConfig{ID: "301", Alias: "UpdatedAlias", ShouldRecord: true}

	done := make(chan struct{})
	go func() {
		manager.SyncUsers([]domain.UserConfig{user1Updated})
		close(done)
	}()

	select {
	case <-done:
		// Succeeded without deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("SyncUsers deadlocked while updating user attributes!")
	}

	// Verify updated attributes
	stUpdated, err := manager.GetUserStatus("301")
	if err != nil {
		t.Fatalf("failed to get user status: %v", err)
	}
	if stUpdated.Alias != "UpdatedAlias" {
		t.Errorf("expected alias 'UpdatedAlias', got '%s'", stUpdated.Alias)
	}
	if !stUpdated.AutoRecordEnabled {
		t.Error("expected autoRecordEnabled to be true")
	}

	// Also test SetAutoRecord on offline user to ensure no deadlock
	doneSetAuto := make(chan struct{})
	go func() {
		_ = manager.SetAutoRecord("301", false)
		close(doneSetAuto)
	}()

	select {
	case <-doneSetAuto:
		// Succeeded without deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("SetAutoRecord deadlocked for offline user!")
	}
}
