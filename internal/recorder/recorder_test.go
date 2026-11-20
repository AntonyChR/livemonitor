package recorder

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
)

// mockProcess implements ProcessHandle for testing.
type mockProcess struct {
	pid       int
	done      chan struct{}
	exited    bool
	exitCode  int
	signal    syscall.Signal
	signaled  []syscall.Signal
	mu        sync.Mutex
	delayExit time.Duration
}

func newMockProcess(pid int, exitCode int, delayExit time.Duration) *mockProcess {
	mp := &mockProcess{
		pid:       pid,
		done:      make(chan struct{}),
		exitCode:  exitCode,
		delayExit: delayExit,
	}
	if delayExit > 0 {
		go func() {
			time.Sleep(delayExit)
			mp.finish(exitCode, 0)
		}()
	}
	return mp
}

func (m *mockProcess) finish(exitCode int, sig syscall.Signal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.exited {
		m.exited = true
		m.exitCode = exitCode
		m.signal = sig
		close(m.done)
	}
}

func (m *mockProcess) PID() int { return m.pid }

func (m *mockProcess) Signal(sig syscall.Signal) error {
	m.mu.Lock()
	m.signaled = append(m.signaled, sig)
	m.mu.Unlock()

	if sig == syscall.SIGKILL || sig == syscall.SIGTERM {
		m.finish(137, sig)
	}
	return nil
}

func (m *mockProcess) Kill() error {
	return m.Signal(syscall.SIGKILL)
}

func (m *mockProcess) Wait() ProcessResult {
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return ProcessResult{
		ExitCode: m.exitCode,
		Signal:   m.signal,
		Exited:   m.exited,
	}
}

func (m *mockProcess) Done() <-chan struct{} { return m.done }

func (m *mockProcess) HasExited() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exited
}

func (m *mockProcess) Result() (ProcessResult, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ProcessResult{
		ExitCode: m.exitCode,
		Signal:   m.signal,
		Exited:   m.exited,
	}, m.exited
}

// mockPipeline implements ProcessPipeline for unit tests.
type mockPipeline struct {
	producer       *mockProcess
	consumer       *mockProcess
	progressBuffer io.Reader
	mu             sync.Mutex
	stopped        bool
}

func (p *mockPipeline) Producer() ProcessHandle   { return p.producer }
func (p *mockPipeline) Consumer() ProcessHandle   { return p.consumer }
func (p *mockPipeline) ProgressReader() io.Reader { return p.progressBuffer }
func (p *mockPipeline) Close() error              { return nil }

func (p *mockPipeline) Wait() (ProcessResult, ProcessResult) {
	var prodRes, consRes ProcessResult
	if p.producer != nil {
		prodRes = p.producer.Wait()
	}
	if p.consumer != nil {
		consRes = p.consumer.Wait()
	}
	return prodRes, consRes
}

func (p *mockPipeline) StopOrderly(pTimeout, fTimeout, tTimeout, kTimeout time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	if p.producer != nil && !p.producer.HasExited() {
		_ = p.producer.Signal(syscall.SIGTERM)
	}
	if p.consumer != nil && !p.consumer.HasExited() {
		_ = p.consumer.Signal(syscall.SIGTERM)
	}
	return nil
}

// mockRunner implements ProcessRunner for tests.
type mockRunner struct {
	mode string
}

func (r *mockRunner) StartPipeline(producerArgs, consumerArgs []string) (ProcessPipeline, error) {
	if r.mode == "spawn-fails" {
		return nil, errors.New("FFmpeg no disponible (prueba)")
	}

	var progress io.Reader
	var producer *mockProcess
	var consumer *mockProcess

	switch r.mode {
	case "video":
		progress = bytes.NewBufferString("out_time_us=1000000\nout_time_us=2000000\n")
		producer = newMockProcess(101, 0, 100*time.Millisecond)
		consumer = newMockProcess(102, 0, 150*time.Millisecond)
	case "no-progress":
		progress = bytes.NewBufferString("")
		producer = newMockProcess(101, 0, 0)
		consumer = newMockProcess(102, 0, 0)
	case "stalled":
		progress = bytes.NewBufferString("out_time_us=500000\n")
		producer = newMockProcess(101, 0, 0)
		consumer = newMockProcess(102, 0, 0)
	case "consumer-fails":
		progress = bytes.NewBufferString("")
		producer = newMockProcess(101, 0, 50*time.Millisecond)
		consumer = newMockProcess(102, 7, 20*time.Millisecond)
	default:
		progress = bytes.NewBufferString("")
		producer = newMockProcess(101, 0, 0)
		consumer = newMockProcess(102, 0, 0)
	}

	return &mockPipeline{
		producer:       producer,
		consumer:       consumer,
		progressBuffer: progress,
	}, nil
}

type mockListener struct {
	mu       sync.Mutex
	started  []domain.RecordingSessionInfo
	stopping []domain.RecordingSessionInfo
	stopped  []domain.RecordingSessionInfo
	failed   []domain.RecordingSessionInfo
}

func (l *mockListener) OnRecordingStarted(info domain.RecordingSessionInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, info)
}

func (l *mockListener) OnRecordingStopping(info domain.RecordingSessionInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopping = append(l.stopping, info)
}

func (l *mockListener) OnRecordingStopped(info domain.RecordingSessionInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = append(l.stopped, info)
}

func (l *mockListener) OnRecordingFailed(info domain.RecordingSessionInfo, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failed = append(l.failed, info)
}

func TestRecordingManager_SuccessfulSession(t *testing.T) {
	listener := &mockListener{}
	opts := config.DefaultSettings()
	opts.WatchdogInterval = 10 * time.Millisecond
	opts.StartupTimeout = 100 * time.Millisecond
	opts.InactivityTimeout = 100 * time.Millisecond

	runner := &mockRunner{mode: "video"}
	manager := NewManager(opts, runner, listener)

	user := domain.UserConfig{ID: "123", Alias: "Prueba", ShouldRecord: true}

	session, err := manager.Start(user, "https://live.douyin.com/test", domain.RecordingOriginAuto)
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}

	// Verify duplicate session is rejected
	_, errDuplicate := manager.Start(user, "https://live.douyin.com/test", domain.RecordingOriginAuto)
	if errDuplicate == nil {
		t.Fatal("expected duplicate session to be rejected")
	}

	// Wait for session to finish
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session timed out")
	}

	listener.mu.Lock()
	if len(listener.started) == 0 {
		t.Error("expected OnRecordingStarted to be called")
	}
	listener.mu.Unlock()

	// Confirm session is no longer active
	if manager.Has(user.ID) {
		t.Error("expected user session to be cleaned up")
	}
}

func TestRecordingManager_NoProgressWatchdog(t *testing.T) {
	listener := &mockListener{}
	opts := config.DefaultSettings()
	opts.WatchdogInterval = 10 * time.Millisecond
	opts.StartupTimeout = 50 * time.Millisecond

	runner := &mockRunner{mode: "no-progress"}
	manager := NewManager(opts, runner, listener)

	user := domain.UserConfig{ID: "123", Alias: "Prueba", ShouldRecord: true}
	session, err := manager.Start(user, "https://live.douyin.com/test", domain.RecordingOriginAuto)
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}

	select {
	case <-session.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("session timed out")
	}

	listener.mu.Lock()
	defer listener.mu.Unlock()
	if len(listener.failed) == 0 {
		t.Error("expected OnRecordingFailed to be called for no-progress session")
	}
}

func TestRecordingManager_StalledWatchdog(t *testing.T) {
	listener := &mockListener{}
	opts := config.DefaultSettings()
	opts.WatchdogInterval = 10 * time.Millisecond
	opts.StartupTimeout = 200 * time.Millisecond
	opts.InactivityTimeout = 50 * time.Millisecond

	runner := &mockRunner{mode: "stalled"}
	manager := NewManager(opts, runner, listener)

	user := domain.UserConfig{ID: "123", Alias: "Prueba", ShouldRecord: true}
	session, err := manager.Start(user, "https://live.douyin.com/test", domain.RecordingOriginAuto)
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}

	select {
	case <-session.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("session timed out")
	}

	listener.mu.Lock()
	defer listener.mu.Unlock()
	if len(listener.started) == 0 {
		t.Error("expected session to start recording")
	}
	if session.FailureReason != "se interrumpió el progreso" {
		t.Errorf("expected failure reason 'se interrumpió el progreso', got %q", session.FailureReason)
	}
}

func TestRecordingManager_ConsumerFails(t *testing.T) {
	listener := &mockListener{}
	opts := config.DefaultSettings()
	opts.WatchdogInterval = 10 * time.Millisecond

	runner := &mockRunner{mode: "consumer-fails"}
	manager := NewManager(opts, runner, listener)

	user := domain.UserConfig{ID: "123", Alias: "Prueba", ShouldRecord: true}
	session, err := manager.Start(user, "https://live.douyin.com/test", domain.RecordingOriginAuto)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-session.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("session timed out")
	}

	if manager.Has(user.ID) {
		t.Error("expected failed session to be cleaned up")
	}
}

func TestRecordingManager_SpawnFails(t *testing.T) {
	listener := &mockListener{}
	opts := config.DefaultSettings()

	runner := &mockRunner{mode: "spawn-fails"}
	manager := NewManager(opts, runner, listener)

	user := domain.UserConfig{ID: "123", Alias: "Prueba", ShouldRecord: true}
	session, err := manager.Start(user, "https://live.douyin.com/test", domain.RecordingOriginAuto)
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}

	select {
	case <-session.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("session timed out")
	}

	if manager.Has(user.ID) {
		t.Error("failed session should not be retained")
	}

	listener.mu.Lock()
	defer listener.mu.Unlock()
	if len(listener.failed) == 0 {
		t.Error("expected OnRecordingFailed to be called when spawn fails")
	}
}

func TestRecordingManager_SummaryAndShutdown(t *testing.T) {
	listener := &mockListener{}
	opts := config.DefaultSettings()
	opts.WatchdogInterval = 100 * time.Millisecond

	runner := &mockRunner{mode: "stalled"}
	manager := NewManager(opts, runner, listener)

	if summary := manager.Summary(); summary != "ninguna" {
		t.Errorf("expected 'ninguna', got %q", summary)
	}

	user1 := domain.UserConfig{ID: "1", Alias: "UserOne", ShouldRecord: true}
	user2 := domain.UserConfig{ID: "2", Alias: "UserTwo", ShouldRecord: true}

	_, _ = manager.Start(user1, "https://live.douyin.com/1", domain.RecordingOriginAuto)
	_, _ = manager.Start(user2, "https://live.douyin.com/2", domain.RecordingOriginAuto)

	time.Sleep(20 * time.Millisecond)
	summary := manager.Summary()
	if summary == "ninguna" {
		t.Errorf("expected active recordings summary, got %q", summary)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := manager.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Verify cannot start new session after shutdown
	_, err = manager.Start(user1, "https://live.douyin.com/1", domain.RecordingOriginAuto)
	if err == nil {
		t.Error("expected Start after Shutdown to fail")
	}
}
