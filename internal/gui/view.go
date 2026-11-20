package gui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"live-recorder/internal/domain"
)

const (
	liveColumnWidth      float32 = 120
	recordingColumnWidth float32 = 210
	autoColumnWidth      float32 = 90
	actionsColumnWidth   float32 = 208
	controlWidth         float32 = 100
)

func metroTheme() *ui.Theme {
	t := ui.DarkTheme()
	t.Background = ui.Hex("#000000")
	t.Surface = ui.Hex("#121212")
	t.SurfaceHover = ui.Hex("#1d1d1d")
	t.SurfacePressed = ui.Hex("#282828")
	t.Border = ui.Hex("#2c2c2c")
	t.Text = ui.Hex("#ffffff")
	t.TextMuted = ui.Hex("#8f8f8f")
	t.Accent = ui.Hex("#0078d7")
	t.AccentHover = ui.Hex("#1b88e0")
	t.AccentPressed = ui.Hex("#0063b1")
	t.AccentText = ui.Hex("#ffffff")
	t.Danger = ui.Hex("#e81123")
	t.Warning = ui.Hex("#ffb900")
	t.Success = ui.Hex("#3fd65a")
	t.Selection = ui.RGBA(0, 120, 215, 0.35)
	t.Focus = ui.RGBA(255, 255, 255, 0.7)
	t.FocusRingWidth = 2
	t.FocusRingOffset = 1
	t.Scrollbar = ui.RGBA(255, 255, 255, 0.28)
	t.Radius = 0
	t.Spacing = 4
	t.FontSize = 14
	t.InactiveAccent = t.Accent
	t.Inverse = ui.Hex("#f4f4f4")
	t.InverseText = ui.Hex("#000000")
	return t
}

func (a *App) view(c *ui.Context) {
	c.SetTheme(a.theme)
	t := a.theme

	ui.Column(c).Fill().Children(func() {
		a.header(c, t)
		a.tabs(c, t)
		a.noticeBar(c, t)

		ui.Column(c).Grow(1).Padding(14, 18, 12, 18).Gap(10).Children(func() {
			if a.tab == 0 {
				a.usersTab(c, t)
			} else {
				a.settingsTab(c, t)
			}
		})

		a.footer(c, t)
	})
}

func (a *App) header(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Padding(12, 18).Gap(12).AlignItems(ui.Center).
		Background(ui.Hex("#080808")).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Children(func() {
			ui.Box(c).Size(4, 24).Background(t.Accent)
			ui.Column(c).Gap(0).Children(func() {
				ui.Text(c, "LIVEMONITOR").FontSize(17).Bold().LetterSpacing(1)
				ui.Text(c, "Grabador de directos de Douyin").FontSize(11).TextColor(t.TextMuted)
			})
			ui.Spacer(c)
			ui.Box(c).Size(8, 8).Background(a.backendColor(t))
			ui.Text(c, a.backendLabel()).FontSize(12).TextColor(a.backendColor(t))
		})
}

func (a *App) backendLabel() string {
	switch a.status.State {
	case domain.BackendStateStarting:
		return "INICIANDO"
	case domain.BackendStateRunning:
		return "EN EJECUCIÓN"
	case domain.BackendStateStopping:
		return "DETENIENDO"
	case domain.BackendStateStopped:
		return "DETENIDO"
	default:
		return string(a.status.State)
	}
}

func (a *App) backendColor(t *ui.Theme) ui.Color {
	switch a.status.State {
	case domain.BackendStateRunning:
		return t.Success
	case domain.BackendStateStarting, domain.BackendStateStopping:
		return t.Warning
	default:
		return t.TextMuted
	}
}

func (a *App) tabs(c *ui.Context, t *ui.Theme) {
	ui.Row(c).PaddingX(18).Background(ui.Hex("#080808")).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Children(func() {
			ui.Tabs(c, &a.tab, "Usuarios", "Configuración")
		})
}

func (a *App) noticeBar(c *ui.Context, t *ui.Theme) {
	if a.notice == "" {
		return
	}
	color := t.Success
	if a.noticeErr {
		color = t.Danger
	}
	ui.Row(c).Margin(8, 18, 0, 18).Padding(8, 12).Gap(10).AlignItems(ui.Center).
		Background(color.Alpha(0.12)).
		BorderWidth(0, 0, 0, 3).BorderColor(color).
		Children(func() {
			ui.Text(c, a.notice).Grow(1).FontSize(12)
			if ui.Button(c, "Cerrar").FontSize(12).Clicked() {
				a.notice = ""
			}
		})
}

func (a *App) usersTab(c *ui.Context, t *ui.Theme) {
	users := a.status.Users

	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Textf(c, "USUARIOS (%d)", len(users)).FontSize(14).Bold()
		ui.Spacer(c)
		if ui.PrimaryButton(c, "+ Añadir usuario").Disabled(a.adding).Clicked() {
			a.addOpen = true
			a.addErr = ""
		}
	})

	if a.addOpen {
		a.addUserForm(c, t)
	}

	if len(users) == 0 {
		ui.Column(c).Grow(1).Center().Gap(6).Children(func() {
			ui.Text(c, "No hay usuarios configurados.").TextColor(t.TextMuted)
			ui.Text(c, "Usa «Añadir usuario» para registrar el primero.").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}

	a.usersHeader(c, t)

	a.userList.Key = func(i int) any { return a.status.Users[i].ID }
	a.userList.Label = func(i int) string { return a.status.Users[i].Alias }
	ui.List(c, &a.userList, len(users), func(i int) {
		a.userRow(c, t, i)
	}).Grow(1).Gap(1)
}

func (a *App) addUserForm(c *ui.Context, t *ui.Theme) {
	ui.Column(c).FillWidth().Padding(12).Gap(8).
		Background(ui.Hex("#0b0b0b")).
		BorderWidth(0, 0, 0, 3).BorderColor(t.Accent).
		Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "NUEVO USUARIO").Width(120).FontSize(12).Bold()
				ui.TextInput(c.Key("add-alias"), &a.addAlias).
					Placeholder("Alias").Label("Alias").Width(240).
					OnSubmit(a.submitNewUser)
				ui.TextInput(c.Key("add-id"), &a.addUserID).
					Placeholder("User ID").Label("User ID").Width(240).
					OnSubmit(a.submitNewUser)
				ui.Spacer(c)
				ui.Button(c, "Cancelar").Disabled(a.adding).OnClick(a.cancelNewUser)
				label := "Agregar"
				if a.adding {
					label = "Agregando…"
				}
				ui.PrimaryButton(c, label).Disabled(a.adding).OnClick(a.submitNewUser)
			})
			if a.addErr != "" {
				ui.Text(c, a.addErr).FontSize(12).TextColor(t.Danger)
			}
		})
}

func (a *App) usersHeader(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Padding(0, 16, 2, 19).Gap(12).Children(func() {
		ui.Text(c, "ALIAS").Grow(1).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Text(c, "ESTADO").Width(liveColumnWidth).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Text(c, "GRABACIÓN").Width(recordingColumnWidth).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Text(c, "AUTO").Width(autoColumnWidth).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Text(c, "ACCIONES").Width(actionsColumnWidth).FontSize(11).Bold().TextColor(t.TextMuted)
	})
}

func (a *App) userRow(c *ui.Context, t *ui.Theme, i int) {
	user := a.status.Users[i]
	state := a.recordingStateFor(user)

	row := ui.Row(c).Padding(10, 16).Gap(12).AlignItems(ui.Center).
		Background(ui.Hex("#0d0d0d")).
		BorderWidth(0, 0, 0, 3).BorderColor(ui.Transparent)
	if user.LiveState == domain.LiveStateLive {
		row.BorderColor(t.Accent)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).Gap(0).Children(func() {
			ui.Text(c, user.Alias).Bold().SingleLine()
			ui.Textf(c, "ID %s", user.ID).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})

		a.liveCell(c, t, user)

		a.recordingCell(c, t, user, state)

		enabled := a.autoRecordFor(user)
		_, autoPending := a.pendingAuto[user.ID]
		ui.Row(c).Width(autoColumnWidth).Children(func() {
			ui.Checkbox(c.Key("auto"), &enabled, "Auto").
				Disabled(autoPending).
				OnChange(func() {
					a.setAutoRecord(user.ID, enabled)
				})
		})

		a.actionCell(c, t, user, state)
	})
}

func (a *App) liveCell(c *ui.Context, t *ui.Theme, user domain.UserStatus) {
	color := t.TextMuted
	label := "Desconocido"
	switch user.LiveState {
	case domain.LiveStateLive:
		color = t.Success
		label = "En directo"
	case domain.LiveStateOffline:
		label = "Sin directo"
	}
	ui.Row(c).Width(liveColumnWidth).Gap(7).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(8, 8).Background(color)
		ui.Text(c, label).FontSize(12).TextColor(color)
	})
}

func (a *App) recordingCell(c *ui.Context, t *ui.Theme, user domain.UserStatus, state domain.RecordingState) {
	ui.Row(c).Width(recordingColumnWidth).Gap(7).AlignItems(ui.Center).Children(func() {
		switch state {
		case domain.RecordingStateRecording:
			ui.Box(c).Size(8, 8).Background(t.Danger)
			ui.Text(c, "GRABANDO").FontSize(11).Bold().TextColor(t.Danger)
			ui.Text(c, a.recordingClock(user.ID)).Font("monospace").FontFeatures("tnum").
				FontSize(12).TextColor(t.Text)
		case domain.RecordingStateStarting:
			ui.Box(c).Size(8, 8).Background(t.Warning)
			ui.Text(c, "INICIANDO…").FontSize(12).TextColor(t.Warning)
		case domain.RecordingStateStopping:
			ui.Box(c).Size(8, 8).Background(t.Warning)
			ui.Text(c, "DETENIENDO…").FontSize(12).TextColor(t.Warning)
		case domain.RecordingStateFailed:
			ui.Box(c).Size(8, 8).Background(t.Danger)
			ui.Text(c, "ERROR").FontSize(12).TextColor(t.Danger)
		default:
			ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
		}
	})
}

func (a *App) recordingClock(userID string) string {
	session, ok := a.sessionFor(userID)
	if !ok {
		return "00:00:00"
	}
	start := session.CreatedAt
	if session.StartedAt != nil {
		start = *session.StartedAt
	}
	return formatClock(time.Since(start))
}

func formatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int64(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}

func (a *App) actionCell(c *ui.Context, t *ui.Theme, user domain.UserStatus, state domain.RecordingState) {
	live := user.LiveState == domain.LiveStateLive
	starting := state == domain.RecordingStateStarting
	stopping := state == domain.RecordingStateStopping
	recording := state == domain.RecordingStateRecording
	_, busy := a.pendingRecording[user.ID]

	recordLabel := "Grabar"
	if starting {
		recordLabel = "Iniciando…"
	}
	stopLabel := "Detener"
	if stopping {
		stopLabel = "Deteniendo…"
	}
	canRecord := live && !starting && !stopping && !recording && !busy
	canStop := recording && !busy

	ui.Row(c).Width(actionsColumnWidth).Gap(8).Children(func() {
		record := ui.PrimaryButton(c.Key("record"), recordLabel).Width(controlWidth).
			Disabled(!canRecord)
		if !canRecord {
			record.Background(t.Surface).TextColor(t.TextMuted).Border(1, t.Border)
		}
		record.OnClick(func() { a.startRecording(user.ID) })

		stop := ui.Button(c.Key("stop"), stopLabel).Width(controlWidth).Disabled(!canStop)
		if canStop {
			stop.Background(t.Danger).TextColor(t.AccentText).BorderWidth(0)
		}
		stop.OnClick(func() { a.stopRecording(user.ID) })
	})
}

func (a *App) footer(c *ui.Context, t *ui.Theme) {
	users := len(a.status.Users)
	recordings := len(a.status.ActiveRecordings)
	live := 0
	for _, user := range a.status.Users {
		if user.LiveState == domain.LiveStateLive {
			live++
		}
	}

	ui.Row(c).Padding(9, 18).Gap(18).AlignItems(ui.Center).
		Background(ui.Hex("#080808")).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).
		Children(func() {
			a.stat(c, t, "Usuarios", users, t.Text)
			ui.Box(c).Size(1, 16).Background(t.Border)
			recordingColor := t.Text
			if recordings > 0 {
				recordingColor = t.Danger
			}
			a.stat(c, t, "Grabaciones activas", recordings, recordingColor)
			ui.Box(c).Size(1, 16).Background(t.Border)
			liveColor := t.Text
			if live > 0 {
				liveColor = t.Success
			}
			a.stat(c, t, "En directo", live, liveColor)
			ui.Spacer(c)
			if ui.Button(c, "Abrir carpeta de grabaciones").Clicked() {
				a.openOutputDirectory()
			}
		})
}

func (a *App) stat(c *ui.Context, t *ui.Theme, label string, value int, color ui.Color) {
	ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, fmt.Sprint(value)).FontSize(13).Bold().TextColor(color)
	})
}

func (a *App) settingsTab(c *ui.Context, t *ui.Theme) {
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Padding(0, 0, 4, 0).Children(func() {
			ui.Text(c, "CONFIGURACIÓN").FontSize(14).Bold()

			a.section(c, t, "Carpeta de grabaciones",
				"Los nuevos vídeos se guardarán en esta ruta.", func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, a.settings.OutputDirectory).Grow(1).Selectable().
							Padding(8, 10).FontSize(12).
							Background(ui.Hex("#090909")).Border(1, t.Border).SingleLine()
						ui.Button(c, "Cambiar…").Disabled(a.savingSettings).
							OnClick(a.chooseOutputDirectory)
					})
				})

			a.section(c, t, "Archivo de usuarios",
				"Formato: «id alias true|false». Por defecto: ~/user_list.txt.", func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						input := ui.TextInput(c.Key("user-list"), &a.userListDraft).
							Label("Archivo de usuarios").Placeholder("~/user_list.txt").Grow(1)
						if input.Submitted() {
							a.saveUserListPath()
						}
						ui.Button(c, "Examinar…").Disabled(a.savingSettings).
							OnClick(a.chooseUserListFile)
						disabled := a.savingSettings ||
							strings.TrimSpace(a.userListDraft) == "" ||
							strings.TrimSpace(a.userListDraft) == a.settings.DefaultUserListFile
						save := ui.PrimaryButton(c, "Guardar ruta").Disabled(disabled)
						if disabled {
							save.Background(t.Surface).TextColor(t.TextMuted).Border(1, t.Border)
						}
						save.OnClick(a.saveUserListPath)
					})
				})

			a.section(c, t, "Calidad por defecto",
				"Se aplica a las grabaciones nuevas (por ejemplo sd1, sd2, best).", func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						input := ui.TextInput(c.Key("quality"), &a.qualityDraft).
							Label("Calidad por defecto").Placeholder("sd1").Width(220)
						if input.Submitted() {
							a.saveQuality()
						}
						disabled := a.savingSettings || a.qualityDraft == a.settings.Quality
						save := ui.PrimaryButton(c, "Guardar calidad").Disabled(disabled)
						if disabled {
							save.Background(t.Surface).TextColor(t.TextMuted).Border(1, t.Border)
						}
						save.OnClick(a.saveQuality)
						ui.Textf(c, "Calidad actual: %s", a.settings.Quality).FontSize(12).TextColor(t.TextMuted)
					})
				})

			a.section(c, t, "Ventana",
				"Comportamiento al cerrar la ventana principal.", func() {
					ui.Checkbox(c.Key("minimize-to-tray"), &a.minimizeToTray, "Minimizar al cerrar").
						Disabled(a.savingSettings).
						OnChange(a.saveMinimizeToTray)
					ui.Text(c, "Si está marcada, la aplicación sigue ejecutándose en la bandeja del sistema. Desde allí puede abrirse de nuevo o cerrarse definitivamente.").
						FontSize(12).TextColor(t.TextMuted)
				})
		})
	})
}

func (a *App) section(c *ui.Context, t *ui.Theme, title, description string, body func()) {
	ui.Column(c).FillWidth().Padding(16).Gap(10).
		Background(t.Surface).Border(1, t.Border).
		Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(3, 16).Background(t.Accent)
				ui.Text(c, title).Bold()
			})
			if description != "" {
				ui.Text(c, description).FontSize(12).TextColor(t.TextMuted)
			}
			body()
		})
}
