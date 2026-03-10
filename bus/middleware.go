package bus

import (
	"context"
	"log/slog"
	"time"

	events "github.com/nengberg/ddd-events"
)

// LoggingMiddleware returns a Middleware that logs each handler invocation
// with its event ID, event type, outcome, and wall-clock duration.
//
// Successful invocations are logged at Info level; failures at Error level.
func LoggingMiddleware(logger *slog.Logger) Middleware {
	return func(next events.Handler) events.Handler {
		return func(ctx context.Context, event events.Event) error {
			start := time.Now()
			err := next(ctx, event)

			level := slog.LevelInfo
			if err != nil {
				level = slog.LevelError
			}

			logger.Log(ctx, level, "event handled",
				"event_id", event.ID,
				"event_type", event.Type,
				"duration_ms", time.Since(start).Milliseconds(),
				"error", err,
			)
			return err
		}
	}
}
