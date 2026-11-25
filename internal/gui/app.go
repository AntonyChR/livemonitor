package gui

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"live-recorder/internal/app"
	"live-recorder/internal/config"
	"live-recorder/internal/domain"
)

// App is the state of the native window. It lives for the whole session and
// is read by the view on the main thread.
type App struct {
	service  app.Service
	win      *mygo.Window
	theme    *ui.Theme
	tray     *mygo.Tray
	quitting bool

	status   domain.GlobalStatus
	settings config.Settings

	tab            int
	userList       ui.ListState
	settingsScroll ui.ScrollState

	addOpen   bool
	addAlias  string
	addUserID string
	addErr    string
	adding    bool

	pendingRecording map[string]domain.RecordingState
	pendingAuto      map[string]bool

	qualityDraft    string
	userListDraft   string
	retryCountDraft float64
	retryDelayDraft float64
	minimizeToTray  bool
	savingSettings  bool

	notice    string
	noticeErr bool
}

// Run shows the native application window and blocks until it closes.
func Run(ctx context.Context, service app.Service) error {
	events, unsubscribe, snapshot := service.Subscribe(ctx, 256)
	a := newApp(service, snapshot)

	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "LiveMonitor",
			Width:           1024,
			Height:          600,
			MinWidth:        1024,
			MaxWidth:        1024,
			MinHeight:       600,
			MaxHeight:       600,
			DisableResize:   true,
			BackgroundColor: "#000000",
			Content:         ui.View(a.view),
		})
		a.win.OnClose(a.handleWindowClose)
		go a.pump(ctx, events)
	})

	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		a.quitting = true
	})

	// Closing the window is the normal exit path; a signal quits the same way.
	go func() {
		<-ctx.Done()
		mygo.App.Quit()
	}()

	err := mygo.App.Run()
	unsubscribe()
	return err
}

func newApp(service app.Service, snapshot domain.GlobalStatus) *App {
	settings := service.GetSettings()
	sortUsers(snapshot.Users)
	return &App{
		service:          service,
		theme:            metroTheme(),
		status:           snapshot,
		settings:         settings,
		qualityDraft:     settings.Quality,
		userListDraft:    settings.DefaultUserListFile,
		retryCountDraft:  float64(settings.RecordingRetryCount),
		retryDelayDraft:  float64(settings.RecordingRetryDelaySeconds),
		minimizeToTray:   settings.MinimizeToTray,
		pendingRecording: make(map[string]domain.RecordingState),
		pendingAuto:      make(map[string]bool),
	}
}

func (a *App) pump(ctx context.Context, events <-chan app.Event) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			a.refresh()
		case <-ticker.C:
			a.refresh()
		}
	}
}

func (a *App) refresh() {
	status := a.service.GetGlobalStatus(context.Background())
	a.win.Update(func() {
		// Ignore a snapshot that was taken before the one already shown.
		if status.Timestamp.Before(a.status.Timestamp) {
			return
		}
		sortUsers(status.Users)
		a.status = status
	})
}

// sortUsers gives the list a stable order: the monitor manager keeps users
// in a map, whose iteration order changes on every snapshot.
func sortUsers(users []domain.UserStatus) {
	sort.Slice(users, func(i, j int) bool {
		left, right := strings.ToLower(users[i].Alias), strings.ToLower(users[j].Alias)
		if left != right {
			return left < right
		}
		return users[i].ID < users[j].ID
	})
}

func (a *App) recordingStateFor(user domain.UserStatus) domain.RecordingState {
	if state, ok := a.pendingRecording[user.ID]; ok {
		return state
	}
	return user.RecordingState
}

func (a *App) autoRecordFor(user domain.UserStatus) bool {
	if enabled, ok := a.pendingAuto[user.ID]; ok {
		return enabled
	}
	return user.AutoRecordEnabled
}

func (a *App) sessionFor(userID string) (domain.RecordingSessionInfo, bool) {
	for _, session := range a.status.ActiveRecordings {
		if session.UserID == userID {
			return session, true
		}
	}
	return domain.RecordingSessionInfo{}, false
}

func (a *App) setNotice(text string, isError bool) {
	a.notice = text
	a.noticeErr = isError
}

// The methods below run on the main thread, from view callbacks. Slow work
// goes to a goroutine and comes back through Window.Update.

func (a *App) startRecording(userID string) {
	if _, busy := a.pendingRecording[userID]; busy {
		return
	}
	a.pendingRecording[userID] = domain.RecordingStateStarting
	go func() {
		_, err := a.service.StartRecording(context.Background(), userID)
		a.win.Update(func() {
			delete(a.pendingRecording, userID)
			if err != nil {
				a.setNotice("No se pudo iniciar la grabación: "+err.Error(), true)
			}
		})
		a.refresh()
	}()
}

func (a *App) stopRecording(userID string) {
	if _, busy := a.pendingRecording[userID]; busy {
		return
	}
	a.pendingRecording[userID] = domain.RecordingStateStopping
	go func() {
		err := a.service.StopRecording(context.Background(), userID)
		a.win.Update(func() {
			delete(a.pendingRecording, userID)
			if err != nil {
				a.setNotice("No se pudo detener la grabación: "+err.Error(), true)
			}
		})
		a.refresh()
	}()
}

func (a *App) setAutoRecord(userID string, enabled bool) {
	if _, busy := a.pendingAuto[userID]; busy {
		return
	}
	a.pendingAuto[userID] = enabled
	go func() {
		err := a.service.SetAutoRecord(context.Background(), userID, enabled)
		a.win.Update(func() {
			delete(a.pendingAuto, userID)
			if err != nil {
				a.setNotice("No se pudo actualizar la grabación automática: "+err.Error(), true)
			}
		})
		a.refresh()
	}()
}

func (a *App) submitNewUser() {
	if a.adding {
		return
	}
	alias := strings.TrimSpace(a.addAlias)
	userID := strings.TrimSpace(a.addUserID)
	if alias == "" || userID == "" {
		a.addErr = "Alias y User ID son obligatorios."
		return
	}

	a.addErr = ""
	a.adding = true
	go func() {
		err := a.service.AddUser(context.Background(), domain.UserConfig{ID: userID, Alias: alias})
		a.win.Update(func() {
			a.adding = false
			if err != nil {
				a.addErr = err.Error()
				return
			}
			a.addOpen = false
			a.addAlias = ""
			a.addUserID = ""
			a.setNotice("Usuario "+alias+" añadido.", false)
		})
		a.refresh()
	}()
}

func (a *App) cancelNewUser() {
	a.addOpen = false
	a.addAlias = ""
	a.addUserID = ""
	a.addErr = ""
}

func (a *App) saveQuality() {
	quality := strings.TrimSpace(a.qualityDraft)
	if quality == "" {
		a.setNotice("La calidad de grabación no puede estar vacía.", true)
		return
	}
	if quality == a.settings.Quality {
		return
	}
	next := a.settings
	next.Quality = quality
	a.updateSettings(next, "Calidad actualizada a "+quality+".")
}

func (a *App) saveUserListPath() {
	path := strings.TrimSpace(a.userListDraft)
	if path == "" {
		a.setNotice("La ruta del archivo de usuarios no puede estar vacía.", true)
		return
	}
	if path == a.settings.DefaultUserListFile {
		return
	}
	next := a.settings
	next.DefaultUserListFile = path
	a.updateSettings(next, "Archivo de usuarios actualizado.")
}

func (a *App) chooseUserListFile() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      a.win,
		Title:       "Seleccionar archivo de usuarios",
		DefaultPath: a.settings.DefaultUserListFile,
		ButtonLabel: "Seleccionar",
		Filters: []mygo.FileFilter{{
			Name:       "Lista de usuarios",
			Extensions: []string{"txt"},
		}},
	})
	if err != nil {
		a.setNotice("No se pudo abrir el selector de archivos: "+err.Error(), true)
		return
	}
	if len(paths) == 0 {
		return
	}
	a.userListDraft = paths[0]
	a.saveUserListPath()
}

func (a *App) saveMinimizeToTray() {
	if a.savingSettings {
		a.minimizeToTray = a.settings.MinimizeToTray
		return
	}
	if a.minimizeToTray == a.settings.MinimizeToTray {
		return
	}
	next := a.settings
	next.MinimizeToTray = a.minimizeToTray
	a.updateSettings(next, "Minimizar al cerrar actualizado.")
}

func (a *App) saveRetrySettings() {
	count := int(math.Round(a.retryCountDraft))
	delay := int(math.Round(a.retryDelayDraft))
	if count < 0 || delay < 0 {
		a.setNotice("Los reintentos y el tiempo entre ellos no pueden ser negativos.", true)
		return
	}
	if count == a.settings.RecordingRetryCount && delay == a.settings.RecordingRetryDelaySeconds {
		return
	}
	next := a.settings
	next.RecordingRetryCount = count
	next.RecordingRetryDelaySeconds = delay
	a.updateSettings(next, "Reintentos de grabación actualizados.")
}

func (a *App) chooseOutputDirectory() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      a.win,
		Title:       "Seleccionar carpeta de grabaciones",
		DefaultPath: a.settings.OutputDirectory,
		ButtonLabel: "Seleccionar",
		Directory:   true,
	})
	if err != nil {
		a.setNotice("No se pudo abrir el selector de carpetas: "+err.Error(), true)
		return
	}
	if len(paths) == 0 {
		return
	}
	next := a.settings
	next.OutputDirectory = paths[0]
	a.updateSettings(next, "Carpeta de grabaciones actualizada.")
}

func (a *App) updateSettings(next config.Settings, success string) {
	if a.savingSettings {
		return
	}
	a.savingSettings = true
	go func() {
		err := a.service.UpdateSettings(next)
		applied := a.service.GetSettings()
		a.win.Update(func() {
			a.savingSettings = false
			if err != nil {
				a.minimizeToTray = a.settings.MinimizeToTray
				a.retryCountDraft = float64(a.settings.RecordingRetryCount)
				a.retryDelayDraft = float64(a.settings.RecordingRetryDelaySeconds)
				a.setNotice("No se pudo actualizar la configuración: "+err.Error(), true)
				return
			}
			a.settings = applied
			a.qualityDraft = applied.Quality
			a.userListDraft = applied.DefaultUserListFile
			a.retryCountDraft = float64(applied.RecordingRetryCount)
			a.retryDelayDraft = float64(applied.RecordingRetryDelaySeconds)
			a.minimizeToTray = applied.MinimizeToTray
			if success != "" {
				a.setNotice(success, false)
			}
		})
		a.refresh()
	}()
}

func (a *App) openOutputDirectory() {
	if err := mygo.Shell.OpenPath(a.settings.OutputDirectory); err != nil {
		a.setNotice("No se pudo abrir la carpeta de grabaciones: "+err.Error(), true)
	}
}
