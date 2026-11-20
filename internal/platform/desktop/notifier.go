package desktop

import (
	"context"
	"log/slog"
	"os/exec"
	"time"
)

// Notifier sends desktop notifications via notify-send and paplay.
type Notifier struct {
	timeout time.Duration
}

// NewNotifier creates a desktop notification sender.
func NewNotifier(timeout time.Duration) *Notifier {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Notifier{timeout: timeout}
}

// Notify triggers a desktop notification and sound asynchronously.
func (n *Notifier) Notify(title, message string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "notify-send", title, message)
		if err := cmd.Run(); err != nil {
			slog.Debug("No se pudo enviar notificación de escritorio", "error", err)
			return
		}

		// Play alert sound if paplay is present
		soundCtx, soundCancel := context.WithTimeout(context.Background(), n.timeout)
		defer soundCancel()

		soundCmd := exec.CommandContext(soundCtx, "paplay", "/usr/share/sounds/freedesktop/stereo/complete.oga")
		_ = soundCmd.Run()
	}()
}
