package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	app "live-recorder/internal/app"
	config "live-recorder/internal/config"
	gui "live-recorder/internal/gui"
	logging "live-recorder/internal/logging"
	desktop "live-recorder/internal/platform/desktop"
	douyin "live-recorder/internal/platform/douyin"
	recorder "live-recorder/internal/recorder"
)

const (
	modeUI  = "ui"
	modeCLI = "cli"
)

func main() {
	// Configure logging strictly to os.Stderr (stdout reserved for IPC).
	logger := logging.SetupLogger(os.Stderr, slog.LevelInfo)

	var modeFlag string
	var intervalFlag int
	var userListFlag string
	var outputDirFlag string
	var qualityFlag string
	var enableDesktopNotify bool

	flag.StringVar(&modeFlag, "mode", modeUI, "Modo de ejecución: ui o cli")
	flag.IntVar(&intervalFlag, "interval", 0, "Pausa en segundos después de cada ronda (por defecto 120)")
	flag.StringVar(&userListFlag, "users", "", "Ruta al archivo user_list.txt")
	flag.StringVar(&outputDirFlag, "output", "", "Directorio de salida para los vídeos")
	flag.StringVar(&qualityFlag, "quality", "", "Calidad de grabación (por defecto 'sd1')")
	flag.BoolVar(&enableDesktopNotify, "notify", true, "Habilitar notificaciones de escritorio (notify-send)")

	flag.Parse()

	if modeFlag != modeUI && modeFlag != modeCLI {
		logger.Error("Modo de ejecución inválido", "mode", modeFlag, "valores", "ui, cli")
		os.Exit(1)
	}

	// Parse positional arguments if provided (matching TS behavior: cmd [interval] [user_list])
	args := flag.Args()
	if len(args) > 0 && intervalFlag == 0 {
		parsedInterval, err := config.ParseInterval(args[0])
		if err != nil {
			logger.Error("Intervalo inválido", "arg", args[0], "error", err)
			os.Exit(1)
		}
		intervalFlag = parsedInterval
	}
	if len(args) > 1 && userListFlag == "" {
		userListFlag = args[1]
	}

	settingsPath := config.DefaultSettingsPath()
	settings, settingsExist, err := config.LoadSettings(settingsPath)
	if err != nil {
		logger.Warn("No se pudo leer la configuración; se usan los valores por defecto",
			"path", settingsPath, "error", err)
		settings = config.DefaultSettings()
		settingsExist = false
	}
	settings.SettingsFile = settingsPath
	if intervalFlag > 0 {
		settings.DefaultCheckLiveStatusInterval = intervalFlag
	}
	if outputDirFlag != "" {
		settings.OutputDirectory = config.ExpandHome(outputDirFlag)
	}
	if qualityFlag != "" {
		settings.Quality = qualityFlag
	}

	if userListFlag != "" {
		userListFlag = config.ExpandHome(userListFlag)
		if _, err := os.Stat(userListFlag); err == nil {
			settings.DefaultUserListFile = userListFlag
		}
	}

	logger.Info("Iniciando monitor de directos",
		"modo", modeFlag,
		"intervalo_segundos", settings.DefaultCheckLiveStatusInterval,
		"lista_usuarios", settings.DefaultUserListFile,
		"directorio_grabaciones", settings.OutputDirectory,
		"calidad", settings.Quality,
		"archivo_configuracion", settingsPath,
		"configuracion_existente", settingsExist,
	)

	// Validate required external tools in PATH
	for _, bin := range []string{"streamlink", "ffmpeg"} {
		if _, err := exec.LookPath(bin); err != nil {
			logger.Error("Herramienta externa requerida no encontrada en PATH", "herramienta", bin)
			os.Exit(1)
		}
	}

	// Prepare and verify output directory
	if err := os.MkdirAll(settings.OutputDirectory, 0755); err != nil {
		logger.Error("No se pudo crear el directorio de salida", "path", settings.OutputDirectory, "error", err)
		os.Exit(1)
	}

	// Validate user list file exists at startup. The UI creates an empty one
	// so the first user can be added from the window; the CLI keeps its
	// strict behavior.
	if _, err := os.Stat(settings.DefaultUserListFile); err != nil {
		if modeFlag == modeUI && errors.Is(err, os.ErrNotExist) {
			if mkErr := os.MkdirAll(filepath.Dir(settings.DefaultUserListFile), 0755); mkErr != nil {
				logger.Error("No se pudo crear el directorio del archivo de usuarios",
					"path", settings.DefaultUserListFile, "error", mkErr)
				os.Exit(1)
			}
			if writeErr := os.WriteFile(settings.DefaultUserListFile, []byte("# id alias record\n"), 0644); writeErr != nil {
				logger.Error("No se pudo crear el archivo de usuarios",
					"path", settings.DefaultUserListFile, "error", writeErr)
				os.Exit(1)
			}
			logger.Info("Se creó un archivo de usuarios vacío", "path", settings.DefaultUserListFile)
		} else {
			logger.Error("No se encontró el archivo de usuarios", "path", settings.DefaultUserListFile, "error", err)
			os.Exit(1)
		}
	}

	// Read initial users
	users, err := config.ReadUsersFromFile(settings.DefaultUserListFile)
	if err != nil {
		logger.Error("Error al validar el archivo de usuarios al arrancar", "path", settings.DefaultUserListFile, "error", err)
		os.Exit(1)
	}

	// Context for graceful OS signal shutdown
	rootCtx, rootCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer rootCancel()

	// Initialize platform client, runner, desktop notifier
	client := douyin.NewClient(settings.RequestTimeout)
	runner := recorder.NewRealProcessRunner()
	notifier := desktop.NewNotifier(settings.NotificationTimeout)

	// Create and start application service
	service := app.NewApplicationService(settings, client, runner, users)

	// Domain event logs run in both modes.
	eventCh, unsub, _ := service.Subscribe(rootCtx, 200)
	defer unsub()
	eventLogger := logging.NewEventLogger(notifier, enableDesktopNotify)
	go eventLogger.Run(rootCtx, eventCh)

	if err := service.StartMonitoring(rootCtx); err != nil {
		logger.Error("Fallo al iniciar el monitoreo", "error", err)
		os.Exit(1)
	}

	ok := true
	if modeFlag == modeUI {
		if err := gui.Run(rootCtx, service); err != nil {
			logger.Error("La interfaz gráfica finalizó con error", "error", err)
			ok = false
		}
	} else {
		runCLI(rootCtx, service, settings, logger)
	}

	if !shutdownService(service, logger) {
		ok = false
	}
	if !ok {
		os.Exit(1)
	}
}

// runCLI keeps the original headless loop: it reloads the user list on every
// round and logs a summary.
func runCLI(ctx context.Context, service app.Service, settings config.Settings, logger *slog.Logger) {
	ticker := time.NewTicker(120 * time.Second)
	defer ticker.Stop()

	iteration := 1
	logging.LogRoundSummary(iteration, service)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			iteration++
			// Re-read user list file: on error, retain last valid configuration
			reloadedUsers, err := config.ReadUsersFromFile(settings.DefaultUserListFile)
			if err != nil {
				logger.Warn("Lista inválida o ilegible; se conserva la última configuración válida", "error", err)
			} else {
				service.ReloadUsers(ctx, reloadedUsers)
			}

			logging.LogRoundSummary(iteration, service)
		}
	}
}

// shutdownService stops monitors and recordings with a bounded deadline.
func shutdownService(service app.Service, logger *slog.Logger) bool {
	logger.Info("Cerrando grabaciones y monitores...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer shutdownCancel()

	if err := service.Shutdown(shutdownCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Error("El apagado excedió el plazo máximo de espera")
		} else {
			logger.Error("Error durante el apagado", "error", err)
		}
		return false
	}

	logger.Info("Procesos de grabación y monitoreo finalizados con éxito.")
	return true
}
