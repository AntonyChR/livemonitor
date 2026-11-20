package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettings_MissingUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, exists, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if exists {
		t.Error("a missing settings file should report exists=false")
	}
	defaults := DefaultSettings()
	if settings.OutputDirectory != defaults.OutputDirectory || settings.Quality != defaults.Quality {
		t.Errorf("unexpected defaults: %+v", settings)
	}
	if settings.DefaultUserListFile != filepath.Join(mustHome(t), "user_list.txt") {
		t.Errorf("default user list = %q", settings.DefaultUserListFile)
	}
	if settings.MinimizeToTray {
		t.Error("minimize to tray should default to false")
	}
}

func TestSaveAndLoadSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	settings := DefaultSettings()
	settings.OutputDirectory = "/tmp/videos"
	settings.Quality = "best"
	settings.DefaultUserListFile = "/tmp/users.txt"
	settings.MinimizeToTray = true

	if err := SaveSettings(path, settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"output_directory"`, `"quality"`, `"user_list_file"`, `"minimize_to_tray"`, "/tmp/videos", "best", "/tmp/users.txt", "true"} {
		if !strings.Contains(text, want) {
			t.Errorf("settings JSON does not contain %q:\n%s", want, text)
		}
	}

	loaded, exists, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if !exists {
		t.Error("settings file should report exists=true")
	}
	if loaded.OutputDirectory != settings.OutputDirectory ||
		loaded.Quality != settings.Quality ||
		loaded.DefaultUserListFile != settings.DefaultUserListFile ||
		loaded.MinimizeToTray != settings.MinimizeToTray {
		t.Errorf("loaded settings = %+v", loaded)
	}
}

func TestLoadSettings_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSettings(path); err == nil {
		t.Error("invalid JSON was accepted")
	}
}

func TestExpandHome(t *testing.T) {
	home := mustHome(t)
	cases := map[string]string{
		"~/user_list.txt": filepath.Join(home, "user_list.txt"),
		"~":               home,
		"/tmp/list.txt":   "/tmp/list.txt",
		"relative.txt":    "relative.txt",
	}
	for input, want := range cases {
		if got := ExpandHome(input); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", input, got, want)
		}
	}
}

func mustHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}
