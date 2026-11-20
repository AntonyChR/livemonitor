package logging

import (
	"context"
	"fmt"
	"log/slog"

	"live-recorder/internal/app"
	"live-recorder/internal/domain"
	"live-recorder/internal/platform/desktop"
)

// EventLogger writes domain events to the configured slog logger and,
// optionally, sends desktop notifications for live starts and recordings.
type EventLogger struct {
	logger        *slog.Logger
	notifier      *desktop.Notifier
	notifyEnabled bool
}

// NewEventLogger creates the logger used by the runtime event loop.
func NewEventLogger(notifier *desktop.Notifier, notifyEnabled bool) *EventLogger {
	return &EventLogger{
		logger:        Logger(),
		notifier:      notifier,
		notifyEnabled: notifyEnabled,
	}
}

// Run consumes domain events until the context is canceled or the channel
// is closed.
func (l *EventLogger) Run(ctx context.Context, events <-chan app.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			l.handle(ev)
		}
	}
}

func (l *EventLogger) handle(ev app.Event) {
	switch e := ev.(type) {
	case app.UserStatusChangedEvent:
		if e.Current.LiveState == domain.LiveStateLive && e.Previous.LiveState != domain.LiveStateLive {
			l.logger.Info("Usuario inició transmisión en directo",
				"alias", e.Alias,
				"id", e.UserID,
				"room_id", e.Current.RoomID,
			)
			if l.notifyEnabled && l.notifier != nil && e.Current.RecordingState != domain.RecordingStateRecording {
				l.notifier.Notify(
					fmt.Sprintf("%s inició live", e.Alias),
					fmt.Sprintf("El usuario %s (%s) está en directo.", e.Alias, e.UserID),
				)
			}
		}
	case app.RecordingStartedEvent:
		l.logger.Info("Grabación iniciada",
			"alias", e.Session.Alias,
			"output", e.Session.OutputPath,
		)
		if l.notifyEnabled && l.notifier != nil {
			l.notifier.Notify(
				fmt.Sprintf("Inicio de GRABACIÓN: %s", e.Session.Alias),
				fmt.Sprintf("Se inició la grabación del live de %s", e.Session.Alias),
			)
		}
	case app.RecordingStoppingEvent:
		l.logger.Info("Grabación finalizando", "alias", e.Session.Alias)
	case app.RecordingStoppedEvent:
		l.logger.Info("Grabación finalizada correctamente",
			"alias", e.Session.Alias,
			"output", e.Session.OutputPath,
		)
	case app.RecordingFailedEvent:
		l.logger.Error("Grabación fallida o interrumpida",
			"alias", e.Session.Alias,
			"reason", e.Reason,
		)
	case app.MonitorErrorEvent:
		l.logger.Warn("Error de consulta de monitor",
			"alias", e.Alias,
			"error", e.Error,
		)
	}
}

// LogRoundSummary writes the periodic summary of users and recordings.
func LogRoundSummary(iteration int, service app.Service) {
	status := service.GetGlobalStatus(context.Background())

	var activeUsers []string
	queryErrors := 0

	for _, u := range status.Users {
		if u.LiveState == domain.LiveStateLive {
			activeUsers = append(activeUsers, u.Alias)
		}
		if u.LastQueryError != "" {
			queryErrors++
		}
	}

	Logger().Info(fmt.Sprintf("resumen %d", iteration),
		"usuarios_activos_total", len(activeUsers),
		"usuarios_activos", activeUsers,
		"consultas_fallidas", queryErrors,
		"grabaciones", service.Summary(),
	)
}
