package recorder

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
)

// SessionEventListener receives recording lifecycle updates.
type SessionEventListener interface {
	OnRecordingStarted(info domain.RecordingSessionInfo)
	OnRecordingStopping(info domain.RecordingSessionInfo)
	OnRecordingStopped(info domain.RecordingSessionInfo)
	OnRecordingFailed(info domain.RecordingSessionInfo, reason string)
}

// Session represents an individual recording session.
type Session struct {
	User          domain.UserConfig
	SessionID     string
	LiveURL       string
	OutputPath    string
	Origin        domain.RecordingOrigin
	CreatedAt     time.Time
	StartedAt     *time.Time
	LastProgress  time.Time
	MediaTimeUs   int64
	FailureReason string

	state    domain.RecordingState
	pipeline ProcessPipeline
	options  config.Settings
	runner   ProcessRunner
	listener SessionEventListener
	onFinish func()

	done     chan struct{}
	stopOnce sync.Once
	mu       sync.RWMutex
	closed   atomic.Bool
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewSession creates an unstarted recording session.
func NewSession(
	user domain.UserConfig,
	liveURL string,
	origin domain.RecordingOrigin,
	options config.Settings,
	runner ProcessRunner,
	listener SessionEventListener,
	onFinish func(),
) *Session {
	now := time.Now()
	outputPath := config.CreateOutputPath(options.OutputDirectory, user)
	return &Session{
		User:         user,
		SessionID:    newSessionID(),
		LiveURL:      liveURL,
		OutputPath:   outputPath,
		Origin:       origin,
		CreatedAt:    now,
		LastProgress: now,
		state:        domain.RecordingStateStarting,
		options:      options,
		runner:       runner,
		listener:     listener,
		onFinish:     onFinish,
		done:         make(chan struct{}),
	}
}

// State returns the current recording state.
func (s *Session) State() domain.RecordingState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Info returns an immutable snapshot of the session info.
func (s *Session) Info() domain.RecordingSessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var startedAtCopy *time.Time
	if s.StartedAt != nil {
		t := *s.StartedAt
		startedAtCopy = &t
	}

	return domain.RecordingSessionInfo{
		SessionID:     s.SessionID,
		UserID:        s.User.ID,
		Alias:         s.User.Alias,
		LiveURL:       s.LiveURL,
		OutputPath:    s.OutputPath,
		State:         s.state,
		Origin:        s.Origin,
		CreatedAt:     s.CreatedAt,
		StartedAt:     startedAtCopy,
		MediaTimeUs:   s.MediaTimeUs,
		FailureReason: s.FailureReason,
	}
}

// Done returns a channel that is closed when the session completes.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Stop requests graceful termination of the session.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		if s.state != domain.RecordingStateFailed {
			s.state = domain.RecordingStateStopping
		}
		info := s.infoLocked()
		pipe := s.pipeline
		s.mu.Unlock()

		if s.listener != nil {
			s.listener.OnRecordingStopping(info)
		}

		if pipe != nil {
			_ = pipe.StopOrderly(
				s.options.ProducerStopTimeout,
				s.options.FinalizeTimeout,
				s.options.TerminateTimeout,
				s.options.KillTimeout,
			)
		}
	})
}

func (s *Session) infoLocked() domain.RecordingSessionInfo {
	var startedAtCopy *time.Time
	if s.StartedAt != nil {
		t := *s.StartedAt
		startedAtCopy = &t
	}
	return domain.RecordingSessionInfo{
		SessionID:     s.SessionID,
		UserID:        s.User.ID,
		Alias:         s.User.Alias,
		LiveURL:       s.LiveURL,
		OutputPath:    s.OutputPath,
		State:         s.state,
		Origin:        s.Origin,
		CreatedAt:     s.CreatedAt,
		StartedAt:     startedAtCopy,
		MediaTimeUs:   s.MediaTimeUs,
		FailureReason: s.FailureReason,
	}
}

func (s *Session) monitorProgress(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "out_time_us=") {
			continue
		}
		valStr := strings.TrimPrefix(line, "out_time_us=")
		mediaTimeUs, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil || mediaTimeUs <= 0 {
			continue
		}

		s.mu.Lock()
		if mediaTimeUs > s.MediaTimeUs {
			s.MediaTimeUs = mediaTimeUs
			s.LastProgress = time.Now()
		}

		firstProgress := false
		if s.state == domain.RecordingStateStarting {
			s.state = domain.RecordingStateRecording
			now := time.Now()
			s.StartedAt = &now
			firstProgress = true
		}
		info := s.infoLocked()
		s.mu.Unlock()

		if firstProgress && s.listener != nil {
			s.listener.OnRecordingStarted(info)
		}
	}
}

// Run executes the recording session and blocks until completion.
func (s *Session) Run() error {
	defer func() {
		if s.onFinish != nil {
			s.onFinish()
		}
		close(s.done)
	}()

	producerArgs := []string{
		"streamlink",
		"--stdout",
		s.LiveURL,
		s.options.Quality,
	}

	consumerArgs := []string{
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-nostats",
		"-n",
		"-progress", "pipe:1",
		"-i", "pipe:0",
		"-c", "copy",
		"-movflags", "+faststart",
		s.OutputPath,
	}

	pipeline, err := s.runner.StartPipeline(producerArgs, consumerArgs)
	if err != nil {
		s.mu.Lock()
		s.state = domain.RecordingStateFailed
		s.FailureReason = err.Error()
		info := s.infoLocked()
		s.mu.Unlock()

		if s.listener != nil {
			s.listener.OnRecordingFailed(info, err.Error())
		}
		return err
	}

	s.mu.Lock()
	s.pipeline = pipeline
	s.mu.Unlock()

	// Progress reader goroutine
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		if r := pipeline.ProgressReader(); r != nil {
			s.monitorProgress(r)
		}
	}()

	// Watchdog timer
	watchdogTicker := time.NewTicker(s.options.WatchdogInterval)
	defer watchdogTicker.Stop()

	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		for {
			select {
			case <-watchdogTicker.C:
				s.mu.RLock()
				curState := s.state
				now := time.Now()
				var elapsed time.Duration
				var limit time.Duration

				if curState == domain.RecordingStateStarting {
					elapsed = now.Sub(s.CreatedAt)
					limit = s.options.StartupTimeout
				} else if curState == domain.RecordingStateRecording {
					elapsed = now.Sub(s.LastProgress)
					limit = s.options.InactivityTimeout
				}
				s.mu.RUnlock()

				if (curState == domain.RecordingStateStarting || curState == domain.RecordingStateRecording) && elapsed >= limit {
					s.mu.Lock()
					if curState == domain.RecordingStateStarting {
						s.FailureReason = "no se recibió progreso inicial"
					} else {
						s.FailureReason = "se interrumpió el progreso"
					}
					s.mu.Unlock()

					slog.Error("Grabación sin progreso; cerrando para permitir otro intento",
						"alias", s.User.Alias,
						"reason", s.FailureReason,
					)
					s.Stop()
					return
				}
			case <-s.done:
				return
			}
		}
	}()

	// Wait for exit of either producer or consumer to trigger orderly stop of both
	exitOrDone := make(chan struct{})
	go func() {
		defer close(exitOrDone)
		var prodDone <-chan struct{}
		if p := pipeline.Producer(); p != nil {
			prodDone = p.Done()
		}
		var consDone <-chan struct{}
		if c := pipeline.Consumer(); c != nil {
			consDone = c.Done()
		}
		if prodDone == nil && consDone == nil {
			return
		}
		select {
		case <-prodDone:
			s.Stop()
		case <-consDone:
			s.Stop()
		}
	}()

	// Wait for pipeline complete wait
	prodRes, consRes := pipeline.Wait()
	<-progressDone

	s.mu.Lock()
	if s.FailureReason == "" {
		if s.StartedAt == nil {
			s.FailureReason = "no se confirmó la grabación"
		} else if consRes.ExitCode != 0 && consRes.ExitCode != 255 {
			s.FailureReason = fmt.Sprintf("FFmpeg terminó con código de error %d", consRes.ExitCode)
		}
	}

	if s.FailureReason != "" && s.StartedAt == nil {
		s.state = domain.RecordingStateFailed
	} else if s.state == domain.RecordingStateStopping || s.state == domain.RecordingStateRecording {
		s.state = domain.RecordingStateIdle
	}
	finalInfo := s.infoLocked()
	reason := s.FailureReason
	s.mu.Unlock()

	_ = pipeline.Close()

	if s.listener != nil {
		if finalInfo.State == domain.RecordingStateFailed {
			s.listener.OnRecordingFailed(finalInfo, reason)
		} else {
			s.listener.OnRecordingStopped(finalInfo)
		}
	}

	if reason != "" && finalInfo.StartedAt == nil {
		return errors.New(reason)
	}
	_ = prodRes
	return nil
}
