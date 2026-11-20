package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"live-recorder/internal/config"
	"live-recorder/internal/domain"
	"live-recorder/internal/platform/douyin"
)

func TestApplicationService_AddUser(t *testing.T) {
	dir := t.TempDir()
	opts := config.DefaultSettings()
	opts.DefaultUserListFile = filepath.Join(dir, "user_list.txt")
	opts.OutputDirectory = filepath.Join(dir, "videos")

	client := &mockDouyinClient{statusMap: make(map[string]douyin.LiveStatus)}
	svc := NewApplicationService(opts, client, &mockRunner{}, []domain.UserConfig{
		{ID: "101", Alias: "Alice", ShouldRecord: true},
	})
	ctx := context.Background()

	if err := svc.AddUser(ctx, domain.UserConfig{ID: "202", Alias: "Bob"}); err != nil {
		t.Fatalf("AddUser failed: %v", err)
	}

	users, err := config.ReadUsersFromFile(opts.DefaultUserListFile)
	if err != nil {
		t.Fatalf("reading saved users: %v", err)
	}
	if len(users) != 2 || users[1].ID != "202" || users[1].Alias != "Bob" {
		t.Fatalf("unexpected saved users: %+v", users)
	}
	if got := len(svc.GetGlobalStatus(ctx).Users); got != 2 {
		t.Fatalf("status has %d users, want 2", got)
	}

	if err := svc.AddUser(ctx, domain.UserConfig{ID: "202", Alias: "Other"}); err == nil {
		t.Error("duplicate ID was accepted")
	}
	if err := svc.AddUser(ctx, domain.UserConfig{ID: "303", Alias: "bad alias"}); err == nil {
		t.Error("alias with whitespace was accepted")
	}
	if err := svc.AddUser(ctx, domain.UserConfig{ID: "abc", Alias: "Carol"}); err == nil {
		t.Error("non-numeric ID was accepted")
	}
}

func TestApplicationService_UpdateSettings(t *testing.T) {
	dir := t.TempDir()
	userFile1 := filepath.Join(dir, "users1.txt")
	userFile2 := filepath.Join(dir, "users2.txt")
	if err := config.SaveUsersAtomic(userFile1, []domain.UserConfig{{ID: "101", Alias: "Alice"}}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveUsersAtomic(userFile2, []domain.UserConfig{
		{ID: "202", Alias: "Bob"},
		{ID: "303", Alias: "Carol"},
	}); err != nil {
		t.Fatal(err)
	}

	settingsFile := filepath.Join(dir, "config", "settings.json")
	opts := config.DefaultSettings()
	opts.DefaultUserListFile = userFile1
	opts.OutputDirectory = filepath.Join(dir, "videos")
	opts.SettingsFile = settingsFile

	client := &mockDouyinClient{statusMap: make(map[string]douyin.LiveStatus)}
	svc := NewApplicationService(opts, client, &mockRunner{}, []domain.UserConfig{
		{ID: "101", Alias: "Alice"},
	})
	ctx := context.Background()

	next := svc.GetSettings()
	newOutput := filepath.Join(dir, "recordings")
	next.OutputDirectory = newOutput
	next.Quality = "best"
	next.DefaultUserListFile = userFile2

	if err := svc.UpdateSettings(next); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}

	got := svc.GetSettings()
	if got.Quality != "best" || got.OutputDirectory != newOutput {
		t.Fatalf("unexpected settings: %+v", got)
	}
	if got.DefaultUserListFile != userFile2 {
		t.Errorf("user list path = %q, want %q", got.DefaultUserListFile, userFile2)
	}
	if _, err := os.Stat(newOutput); err != nil {
		t.Errorf("output directory was not created: %v", err)
	}

	if users := svc.GetGlobalStatus(ctx).Users; len(users) != 2 {
		t.Errorf("status has %d users after changing the list, want 2", len(users))
	}

	loaded, exists, err := config.LoadSettings(settingsFile)
	if err != nil {
		t.Fatalf("loading saved settings: %v", err)
	}
	if !exists {
		t.Fatal("settings file was not created")
	}
	if loaded.OutputDirectory != newOutput || loaded.Quality != "best" || loaded.DefaultUserListFile != userFile2 {
		t.Errorf("persisted settings = %+v", loaded)
	}

	bad := got
	bad.Quality = ""
	if err := svc.UpdateSettings(bad); err == nil {
		t.Error("empty quality was accepted")
	}

	missing := got
	missing.DefaultUserListFile = filepath.Join(dir, "missing.txt")
	if err := svc.UpdateSettings(missing); err == nil {
		t.Error("missing user list file was accepted")
	}
	if again := svc.GetSettings(); again.DefaultUserListFile != userFile2 {
		t.Errorf("settings changed after a failed update: %+v", again)
	}
}
