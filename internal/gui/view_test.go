package gui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"live-recorder/internal/app"
	"live-recorder/internal/config"
	"live-recorder/internal/domain"
)

type fakeService struct {
	settings config.Settings
	status   domain.GlobalStatus
}

func (f *fakeService) GetGlobalStatus(context.Context) domain.GlobalStatus { return f.status }
func (f *fakeService) GetUserStatus(context.Context, string) (domain.UserStatus, error) {
	return domain.UserStatus{}, nil
}
func (f *fakeService) SetAutoRecord(context.Context, string, bool) error { return nil }
func (f *fakeService) StartRecording(context.Context, string) (string, error) {
	return "", nil
}
func (f *fakeService) StopRecording(context.Context, string) error { return nil }
func (f *fakeService) StartMonitoring(context.Context) error       { return nil }
func (f *fakeService) Shutdown(context.Context) error              { return nil }
func (f *fakeService) Subscribe(context.Context, int) (<-chan app.Event, func(), domain.GlobalStatus) {
	ch := make(chan app.Event)
	close(ch)
	return ch, func() {}, f.status
}
func (f *fakeService) ReloadUsers(context.Context, []domain.UserConfig) {}
func (f *fakeService) AddUser(context.Context, domain.UserConfig) error { return nil }
func (f *fakeService) GetSettings() config.Settings                     { return f.settings }
func (f *fakeService) UpdateSettings(config.Settings) error             { return nil }
func (f *fakeService) Summary() string                                  { return "ninguna" }

func testApp() *App {
	started := time.Now().Add(-75 * time.Second)
	settings := config.DefaultSettings()
	settings.OutputDirectory = "/tmp/recordings"
	settings.Quality = "sd1"
	settings.DefaultUserListFile = "/tmp/users.txt"
	settings.MinimizeToTray = true
	settings.RecordingRetryCount = 4
	settings.RecordingRetryDelaySeconds = 7
	status := domain.GlobalStatus{
		State: domain.BackendStateRunning,
		Users: []domain.UserStatus{
			{ID: "1001", Alias: "Gwen1", LiveState: domain.LiveStateLive,
				RecordingState: domain.RecordingStateRecording, AutoRecordEnabled: true},
			{ID: "1002", Alias: "Gwen2", LiveState: domain.LiveStateOffline,
				RecordingState: domain.RecordingStateIdle},
			{ID: "1003", Alias: "Gwen3", LiveState: domain.LiveStateLive,
				RecordingState: domain.RecordingStateIdle},
		},
		ActiveRecordings: []domain.RecordingSessionInfo{{
			SessionID: "s1", UserID: "1001", Alias: "Gwen1",
			State: domain.RecordingStateRecording, CreatedAt: started, StartedAt: &started,
		}},
		Timestamp: time.Now(),
	}
	return newApp(&fakeService{settings: settings, status: status}, status)
}

func TestUsersTab(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1024, 600)

	for _, want := range []string{"Gwen1", "Gwen2", "Gwen3", "GRABANDO", "Live", "offline"} {
		if !tt.HasText(want) {
			t.Errorf("users tab does not show %q; texts: %q", want, tt.Texts())
		}
	}

	if err := tt.Click("+ Añadir usuario"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("NUEVO USUARIO") {
		t.Errorf("the add form did not open; texts: %q", tt.Texts())
	}
}

func TestIntermediateRecordingStates(t *testing.T) {
	a := testApp()
	a.pendingRecording["1003"] = domain.RecordingStateStarting
	a.pendingRecording["1001"] = domain.RecordingStateStopping
	tt := ui.NewTester(a.view, 1024, 600)

	for _, want := range []string{"Iniciando…", "INICIANDO…", "Deteniendo…", "DETENIENDO…"} {
		if !tt.HasText(want) {
			t.Errorf("intermediate state %q is not shown; texts: %q", want, tt.Texts())
		}
	}
}

func TestSettingsTab(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1024, 600)

	if err := tt.Click("Configuración"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/tmp/recordings", "Archivo de usuarios", "Calidad por defecto", "Reintentos de grabación",
		"Número de reintentos", "Segundos entre reintentos", "sd1",
	} {
		if !tt.HasText(want) {
			t.Errorf("settings tab does not show %q; texts: %q", want, tt.Texts())
		}
	}
	if field, ok := tt.AccessByRole(ui.RoleTextField, "Archivo de usuarios"); !ok || field.Value != "/tmp/users.txt" {
		t.Errorf("user list field = %+v, found %v", field, ok)
	}
	if tray, ok := tt.AccessByRole(ui.RoleCheckBox, "Minimizar al cerrar"); !ok || !tray.Checked {
		t.Errorf("minimize to tray checkbox = %+v, found %v", tray, ok)
	}
}

func TestSettingsScrollsWithNotice(t *testing.T) {
	a := testApp()
	a.notice = "Configuración actualizada."
	tt := ui.NewTester(a.view, 1024, 600)

	if err := tt.Click("Configuración"); err != nil {
		t.Fatal(err)
	}
	before, ok := tt.AccessByRole(ui.RoleTextField, "Calidad por defecto")
	if !ok {
		t.Fatal("quality field not found")
	}

	tt.Scroll(700, 420, 0, 220)
	tt.Frame()

	after, ok := tt.AccessByRole(ui.RoleTextField, "Calidad por defecto")
	if !ok {
		t.Fatal("quality field not found after scrolling")
	}
	if after.Bounds.Y >= before.Bounds.Y {
		t.Errorf("settings content did not scroll: before y=%v, after y=%v", before.Bounds.Y, after.Bounds.Y)
	}
}

func TestFormatClock(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "00:00:00",
		75 * time.Second: "00:01:15",
		time.Hour + 2*time.Minute + 9*time.Second: "01:02:09",
	}
	for d, want := range cases {
		if got := formatClock(d); got != want {
			t.Errorf("formatClock(%s) = %q, want %q", d, got, want)
		}
	}
}
