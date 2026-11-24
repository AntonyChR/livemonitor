package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// settingsFile is the on-disk JSON representation of the user-editable
// application configuration.
type settingsFile struct {
	OutputDirectory            string `json:"output_directory"`
	Quality                    string `json:"quality"`
	UserListFile               string `json:"user_list_file"`
	MinimizeToTray             bool   `json:"minimize_to_tray"`
	RecordingRetryCount        *int   `json:"recording_retry_count,omitempty"`
	RecordingRetryDelaySeconds *int   `json:"recording_retry_delay_seconds,omitempty"`
}

// DefaultSettingsPath returns the JSON file where the application keeps its
// configuration, under the user's configuration directory.
func DefaultSettingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "settings.json")
	}
	return filepath.Join(dir, "live-recorder", "settings.json")
}

// ExpandHome replaces a leading ~ or ~/ with the user's home directory.
func ExpandHome(path string) string {
	path = strings.TrimSpace(path)
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return homeDir
	}
	return filepath.Join(homeDir, path[2:])
}

// LoadSettings reads the persisted configuration over the defaults. The
// boolean reports whether the file existed. A missing file is not an error.
func LoadSettings(path string) (Settings, bool, error) {
	settings := DefaultSettings()
	path = ExpandHome(path)

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, false, nil
	}
	if err != nil {
		return settings, false, err
	}

	var stored settingsFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return settings, true, fmt.Errorf("configuración inválida en %s: %w", path, err)
	}

	if value := strings.TrimSpace(stored.OutputDirectory); value != "" {
		settings.OutputDirectory = ExpandHome(value)
	}
	if value := strings.TrimSpace(stored.Quality); value != "" {
		settings.Quality = value
	}
	if value := strings.TrimSpace(stored.UserListFile); value != "" {
		settings.DefaultUserListFile = ExpandHome(value)
	}
	settings.MinimizeToTray = stored.MinimizeToTray
	if stored.RecordingRetryCount != nil && *stored.RecordingRetryCount >= 0 {
		settings.RecordingRetryCount = *stored.RecordingRetryCount
	}
	if stored.RecordingRetryDelaySeconds != nil && *stored.RecordingRetryDelaySeconds >= 0 {
		settings.RecordingRetryDelaySeconds = *stored.RecordingRetryDelaySeconds
	}

	return settings, true, nil
}

// SaveSettings writes the configuration atomically as JSON.
func SaveSettings(path string, settings Settings) error {
	path = ExpandHome(path)

	retryCount := settings.RecordingRetryCount
	retryDelay := settings.RecordingRetryDelaySeconds
	data, err := json.MarshalIndent(settingsFile{
		OutputDirectory:            settings.OutputDirectory,
		Quality:                    settings.Quality,
		UserListFile:               settings.DefaultUserListFile,
		MinimizeToTray:             settings.MinimizeToTray,
		RecordingRetryCount:        &retryCount,
		RecordingRetryDelaySeconds: &retryDelay,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de configuración: %w", err)
	}

	tempFile, err := os.CreateTemp(dir, "settings_*.json.tmp")
	if err != nil {
		return fmt.Errorf("no se pudo crear el archivo temporal de configuración: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("no se pudo reemplazar la configuración: %w", err)
	}
	return nil
}
