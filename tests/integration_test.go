package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
	"live-recorder/internal/recorder"
)

// syntheticProcessRunner replaces the streamlink command with an ffmpeg synthetic stream generator
// to test the real pipe and ffmpeg consumer on Linux without network or Douyin dependencies.
type syntheticProcessRunner struct {
	underlying *recorder.RealProcessRunner
}

func (r *syntheticProcessRunner) StartPipeline(producerArgs, consumerArgs []string) (recorder.ProcessPipeline, error) {
	// Replace streamlink producer with lavfi testsrc generator
	if len(producerArgs) > 0 && producerArgs[0] == "streamlink" {
		producerArgs = []string{
			"ffmpeg",
			"-hide_banner",
			"-loglevel", "error",
			"-re",
			"-f", "lavfi",
			"-i", "testsrc=size=64x64:rate=10",
			"-c:v", "mpeg2video",
			"-f", "mpegts",
			"pipe:1",
		}
	}
	return r.underlying.StartPipeline(producerArgs, consumerArgs)
}

func TestIntegration_RealRecordingAndFFprobe(t *testing.T) {
	// Check if ffmpeg and ffprobe are available in PATH
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg no encontrado en PATH; omitiendo prueba de integración")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe no encontrado en PATH; omitiendo prueba de integración")
	}

	tempDir := t.TempDir()

	opts := config.DefaultSettings()
	opts.OutputDirectory = tempDir
	opts.StartupTimeout = 15 * time.Second
	opts.InactivityTimeout = 15 * time.Second
	opts.WatchdogInterval = 100 * time.Millisecond
	opts.ProducerStopTimeout = 1 * time.Second
	opts.FinalizeTimeout = 5 * time.Second

	runner := &syntheticProcessRunner{underlying: recorder.NewRealProcessRunner()}
	manager := recorder.NewManager(opts, runner, nil)

	user := domain.UserConfig{ID: "777", Alias: "PruebaReal", ShouldRecord: true}

	session, err := manager.Start(user, "https://live.douyin.com/mock", domain.RecordingOriginAuto)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify duplicate session rejected
	_, errDup := manager.Start(user, "https://live.douyin.com/mock", domain.RecordingOriginAuto)
	if errDup == nil {
		t.Fatal("expected duplicate session to be rejected")
	}

	// Wait until session is confirmed recording (via ffmpeg progress pipe)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if session.State() == domain.RecordingStateRecording {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if session.State() != domain.RecordingStateRecording {
		t.Fatalf("session failed to enter recording state, current state: %s", session.State())
	}

	summary := manager.Summary()
	if !strings.Contains(summary, "PruebaReal") {
		t.Errorf("expected summary to contain user alias, got: %s", summary)
	}

	// Graceful shutdown of manager
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = manager.Shutdown(shutdownCtx)
	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Check output file exists
	outputPath := session.OutputPath
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file does not exist: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("output file is empty")
	}

	// Probe output file using ffprobe
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe failed on %s: %v, output: %s", filepath.Base(outputPath), err, string(out))
	}
	outStr := string(out)
	if !strings.Contains(outStr, "duration=") {
		t.Errorf("expected ffprobe output to contain duration=, got: %s", outStr)
	}
}
