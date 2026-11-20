package gui

import (
	_ "embed"
	"log/slog"

	"github.com/egoist/mygo"
)

//go:embed assets/tray.png
var trayIcon []byte

// handleWindowClose hides the window and shows a tray icon when the user
// asked to keep the application running in the background.
func (a *App) handleWindowClose(e *mygo.CloseEvent) {
	if a.quitting || !a.minimizeToTray {
		return
	}

	e.PreventDefault()

	if err := a.showTray(); err != nil {
		slog.Error("No se pudo crear el icono de la bandeja del sistema", "error", err)
		a.setNotice("No se pudo minimizar a la bandeja del sistema: "+err.Error(), true)
		if a.win != nil {
			a.win.Invalidate()
		}
		return
	}

	a.win.Hide()
	slog.Info("Ventana minimizada a la bandeja del sistema")
}

func (a *App) showTray() error {
	if a.tray != nil {
		return nil
	}

	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "livemonitor",
		Icon:    trayIcon,
		ToolTip: "LiveMonitor",
		Menu: mygo.NewMenu([]*mygo.MenuItem{
			{Label: "Abrir", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.restoreWindow()
			}},
			{Label: "Cerrar", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.quitApp()
			}},
		}),
	})
	if err != nil {
		return err
	}

	a.tray = tray
	return nil
}

func (a *App) restoreWindow() {
	if a.win != nil {
		a.win.Show()
		a.win.Focus()
	}
	if a.tray != nil {
		a.tray.Destroy()
		a.tray = nil
	}
	slog.Info("Ventana restaurada desde la bandeja del sistema")
}

func (a *App) quitApp() {
	a.quitting = true
	slog.Info("Cierre total solicitado desde la bandeja del sistema")
	mygo.App.Quit()
}
