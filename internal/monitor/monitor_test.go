package monitor

import (
	"context"
	"errors"
	"io"
	"strings"
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
	mu         sync.Mutex
	statusMap  map[string]douyin.LiveStatus
	errorMap   map[string]error
	liveURLs   map[string]string
	queryCount map[string]int
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

// stopRacePipeline reports progress and then blocks the orderly stop until
// the session listener has observed the final stopped state.
type stopRacePipeline struct {
	stopOnce  sync.Once
	stopped   chan struct{}
	stopAsked chan struct{}
}

func newStopRacePipeline() *stopRacePipeline {
	return &stopRacePipeline{
		stopped:   make(chan struct{}),
		stopAsked: make(chan struct{}),
	}
}

func (p *stopRacePipeline) Producer() recorder.ProcessHandle { return nil }
func (p *stopRacePipeline) Consumer() recorder.ProcessHandle { return nil }
func (p *stopRacePipeline) ProgressReader() io.Reader {
	return strings.NewReader("out_time_us=1000000\n")
}
func (p *stopRacePipeline) StopOrderly(prodTimeout, f, t, k time.Duration) error {
	p.stopOnce.Do(func() { close(p.stopAsked) })
	<-p.stopped
	return nil
}
func (p *stopRacePipeline) Wait() (recorder.ProcessResult, recorder.ProcessResult) {
	<-p.stopAsked
	return recorder.ProcessResult{}, recorder.ProcessResult{}
}
func (p *stopRacePipeline) Close() error { return nil }

type stopRaceRunner struct {
	pipelines chan *stopRacePipeline
}

func newStopRaceRunner() *stopRaceRunner {
	return &stopRaceRunner{pipelines: make(chan *stopRacePipeline, 1)}
}

func (r *stopRaceRunner) StartPipeline(prodArgs, consArgs []string) (recorder.ProcessPipeline, error) {
	p := newStopRacePipeline()
	r.pipelines <- p
	return p, nil
}

type recordingStateListener struct {
	manager   *Manager
	finalized chan struct{}
	once      sync.Once
}

func (l *recordingStateListener) OnRecordingStarted(info domain.RecordingSessionInfo) {
	l.manager.UpdateRecordingState(info.UserID, domain.RecordingStateRecording, info.SessionID)
}
func (l *recordingStateListener) OnRecordingStopping(info domain.RecordingSessionInfo) {
	l.manager.UpdateRecordingState(info.UserID, domain.RecordingStateStopping, info.SessionID)
}
func (l *recordingStateListener) OnRecordingStopped(info domain.RecordingSessionInfo) {
	l.manager.UpdateRecordingState(info.UserID, domain.RecordingStateIdle, "")
	l.once.Do(func() { close(l.finalized) })
}
func (l *recordingStateListener) OnRecordingFailed(info domain.RecordingSessionInfo, reason string) {
	l.manager.UpdateRecordingState(info.UserID, domain.RecordingStateFailed, "")
	l.manager.HandleRecordingFailed(info)
	l.once.Do(func() { close(l.finalized) })
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

type flakyRunner struct {
	mu       sync.Mutex
	failures int
	attempts int
}

func (r *flakyRunner) StartPipeline(prodArgs, consArgs []string) (recorder.ProcessPipeline, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.attempts <= r.failures {
		return nil, errors.New("fallo simulado al iniciar el pipeline")
	}
	return newMockPipeline(), nil
}

func (r *flakyRunner) attemptCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

func TestStopRecordingEndsInIdle(t *testing.T) {
	opts := config.DefaultSettings()
	runner := newStopRaceRunner()
	listener := &recordingStateListener{finalized: make(chan struct{})}
	recorders := recorder.NewManager(opts, runner, listener)
	manager := NewManager(newMockDouyinClient(), recorders, time.Second, &mockMonitorEventHandler{})
	listener.manager = manager

	user := domain.UserConfig{ID: "400", Alias: "StopRace"}
	manager.SyncUsers([]domain.UserConfig{user})

	session, err := recorders.Start(user, "https://live.douyin.com/test", domain.RecordingOriginManual)
	if err != nil {
		t.Fatalf("starting recording: %v", err)
	}
	manager.UpdateRecordingState(user.ID, domain.RecordingStateStarting, session.SessionID)
	pipeline := <-runner.pipelines
	listener.finalized = pipeline.stopped

	// Wait until progress has been observed, so the session has a start time
	// and its stop reports "stopped" instead of "failed".
	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.GetUserStatus(user.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.RecordingState == domain.RecordingStateRecording {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recording did not start")
		}
		time.Sleep(time.Millisecond)
	}

	if err := manager.StopRecording(user.ID); err != nil {
		t.Fatalf("stopping recording: %v", err)
	}

	select {
	case <-listener.finalized:
	default:
		t.Fatal("recording did not finalize before StopRecording returned")
	}

	status, err := manager.GetUserStatus(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.RecordingState != domain.RecordingStateIdle {
		t.Errorf("recording state after stop = %q, want idle", status.RecordingState)
	}
}

func TestRecordingRetrySucceeds(t *testing.T) {
	client := newMockDouyinClient()
	client.statusMap["500"] = douyin.LiveStatus{Status: douyin.StatusLive, RoomID: "room500"}

	opts := config.DefaultSettings()
	runner := &flakyRunner{failures: 1}
	listener := &recordingStateListener{finalized: make(chan struct{})}
	recorders := recorder.NewManager(opts, runner, listener)
	manager := NewManager(client, recorders, 20*time.Millisecond, &mockMonitorEventHandler{})
	listener.manager = manager
	manager.SetRecordingRetryPolicy(2, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	defer func() {
		manager.Stop()
		cancel()
	}()
	manager.SyncUsers([]domain.UserConfig{{ID: "500", Alias: "Retry"}})

	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.GetUserStatus("500")
		if err != nil {
			t.Fatal(err)
		}
		if status.LiveState == domain.LiveStateLive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("user did not become live")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := manager.StartRecording("500"); err != nil {
		t.Fatalf("unexpected error starting recording: %v", err)
	}

	deadline = time.Now().Add(time.Second)
	for runner.attemptCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("recording was not retried; attempts=%d", runner.attemptCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	deadline = time.Now().Add(time.Second)
	for !recorders.Has("500") {
		if time.Now().After(deadline) {
			t.Fatal("retried recording session was not created")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := runner.attemptCount(); got != 2 {
		t.Errorf("start attempts = %d, want 2", got)
	}
	_ = manager.StopRecording("500")
}

func TestRecordingRetryStopsAfterLimit(t *testing.T) {
	client := newMockDouyinClient()
	client.statusMap["501"] = douyin.LiveStatus{Status: douyin.StatusLive, RoomID: "room501"}

	opts := config.DefaultSettings()
	runner := &flakyRunner{failures: 100}
	listener := &recordingStateListener{finalized: make(chan struct{})}
	recorders := recorder.NewManager(opts, runner, listener)
	manager := NewManager(client, recorders, 20*time.Millisecond, &mockMonitorEventHandler{})
	listener.manager = manager
	manager.SetRecordingRetryPolicy(2, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	defer func() {
		manager.Stop()
		cancel()
	}()
	manager.SyncUsers([]domain.UserConfig{{ID: "501", Alias: "RetryLimit"}})

	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.GetUserStatus("501")
		if err != nil {
			t.Fatal(err)
		}
		if status.LiveState == domain.LiveStateLive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("user did not become live")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := manager.StartRecording("501"); err != nil {
		t.Fatalf("unexpected error starting recording: %v", err)
	}

	deadline = time.Now().Add(time.Second)
	for runner.attemptCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("retries did not run; attempts=%d", runner.attemptCount())
		}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)
	if got := runner.attemptCount(); got != 3 {
		t.Errorf("start attempts = %d, want 3 (initial + 2 retries)", got)
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
