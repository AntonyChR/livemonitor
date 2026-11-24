package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"live-recorder/internal/domain"
)

// Settings holds all configurable parameters of the application.
type Settings struct {
	OutputDirectory                string        `json:"output_directory"`
	Quality                        string        `json:"quality"`
	DefaultUserListFile            string        `json:"default_user_list_file"`
	SettingsFile                   string        `json:"-"`
	MinimizeToTray                 bool          `json:"minimize_to_tray"`
	RecordingRetryCount            int           `json:"recording_retry_count"`
	RecordingRetryDelaySeconds     int           `json:"recording_retry_delay_seconds"`
	RequestTimeout                 time.Duration `json:"request_timeout"`
	NotificationTimeout            time.Duration `json:"notification_timeout"`
	StartupTimeout                 time.Duration `json:"startup_timeout"`
	InactivityTimeout              time.Duration `json:"inactivity_timeout"`
	ProducerStopTimeout            time.Duration `json:"producer_stop_timeout"`
	FinalizeTimeout                time.Duration `json:"finalize_timeout"`
	KillTimeout                    time.Duration `json:"kill_timeout"`
	TerminateTimeout               time.Duration `json:"terminate_timeout"`
	WatchdogInterval               time.Duration `json:"watchdog_interval"`
	QueryConcurrency               int           `json:"query_concurrency"`
	DefaultCheckLiveStatusInterval int           `json:"default_interval"`
	DefaultSummaryLogInterval      int           `json:"default_summary_log_interval"`
}

// DefaultSettings returns the standard configuration settings.
func DefaultSettings() Settings {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	return Settings{
		OutputDirectory:                filepath.Join(homeDir, "solid", "videos"),
		Quality:                        "sd1",
		DefaultUserListFile:            filepath.Join(homeDir, "user_list.txt"),
		RecordingRetryCount:            3,
		RecordingRetryDelaySeconds:     5,
		RequestTimeout:                 30 * time.Second,
		NotificationTimeout:            5 * time.Second,
		StartupTimeout:                 60 * time.Second,
		InactivityTimeout:              120 * time.Second,
		ProducerStopTimeout:            5 * time.Second,
		FinalizeTimeout:                30 * time.Second,
		TerminateTimeout:               10 * time.Second,
		KillTimeout:                    5 * time.Second,
		WatchdogInterval:               1 * time.Second,
		QueryConcurrency:               3,
		DefaultCheckLiveStatusInterval: 120,
		DefaultSummaryLogInterval:      60,
	}
}

// ParseInterval converts a string into interval seconds.
// If value is empty, it returns DefaultCheckLiveStatusInterval (120).
func ParseInterval(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 120, nil
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return 0, fmt.Errorf("el intervalo debe ser un entero positivo de segundos")
		}
	}
	intervalSeconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || intervalSeconds <= 0 || intervalSeconds > 86400*365 {
		return 0, fmt.Errorf("el intervalo debe ser un entero positivo de segundos")
	}
	return int(intervalSeconds), nil
}

// randomHex generates n random hex characters.
func randomHex(n int) string {
	bytes := make([]byte, (n+1)/2)
	if _, err := rand.Read(bytes); err != nil {
		// fallback to timestamp-derived hex if rand fails
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xFFFFFFFF)[:n]
	}
	return hex.EncodeToString(bytes)[:n]
}

// SanitizeAlias cleans up an alias for safe file system usage.
func SanitizeAlias(raw string) string {
	var builder strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	alias := builder.String()
	runes := []rune(alias)
	if len(runes) > 80 {
		alias = string(runes[:80])
	}
	if strings.Trim(alias, "_") == "" {
		return "usuario"
	}
	return alias
}

// CreateOutputPath creates a unique, collisions-resistant destination path for recording.
// Formatted with sanitized alias, user ID, UTC timestamp, and a short random hex token.
func CreateOutputPath(directory string, user domain.UserConfig) string {
	alias := SanitizeAlias(user.Alias)
	now := time.Now().UTC()
	// Format UTC ISO8601 with colons and dots replaced by dashes:
	// 2006-01-02T15-04-05-000Z
	timestamp := now.Format("2006-01-02T15-04-05.000Z")
	timestamp = strings.ReplaceAll(timestamp, ":", "-")
	timestamp = strings.ReplaceAll(timestamp, ".", "-")

	filename := fmt.Sprintf("%s_%s_%s_%s.mp4", alias, user.ID, timestamp, randomHex(4))
	return filepath.Join(directory, filename)
}
