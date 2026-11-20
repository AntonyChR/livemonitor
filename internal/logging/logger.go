package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
)

var defaultLogger *slog.Logger

func init() {
	// By default, write structured logs to os.Stderr (leaving stdout reserved for future IPC).
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	defaultLogger = slog.New(handler)
	slog.SetDefault(defaultLogger)
}

// SetupLogger configures the global logger with a specific level and writer.
func SetupLogger(writer io.Writer, level slog.Level) *slog.Logger {
	if writer == nil {
		writer = os.Stderr
	}
	handler := slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				t := a.Value.Time()
				a.Value = slog.StringValue(t.Format("15:04:05"))
			}
			return a
		},
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	defaultLogger = logger
	return logger
}

// Logger returns the configured default slog.Logger.
func Logger() *slog.Logger {
	return defaultLogger
}

// WithUser returns a logger annotated with a user ID and alias.
func WithUser(ctx context.Context, userID, alias string) *slog.Logger {
	return defaultLogger.With(
		slog.String("user_id", userID),
		slog.String("alias", alias),
	)
}

// WithSession returns a logger annotated with session details.
func WithSession(ctx context.Context, sessionID, userID, alias string) *slog.Logger {
	return defaultLogger.With(
		slog.String("session_id", sessionID),
		slog.String("user_id", userID),
		slog.String("alias", alias),
	)
}
