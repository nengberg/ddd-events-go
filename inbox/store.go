package inbox

import "context"

// Store is the persistence port for inbox messages.
// Implementations might use PostgreSQL, MySQL, MongoDB, or in-memory storage.
type Store interface {
	// Save persists a newly received event. Called by the Receiver when an
	// event arrives from the broker. Implementations should be idempotent on
	// Event.ID to prevent duplicate processing (deduplication).
	Save(ctx context.Context, msg Message) error

	// FindPending returns all messages with StatusPending that have not
	// exceeded MaxAttempts. Results should be ordered by ReceivedAt ascending.
	FindPending(ctx context.Context) ([]Message, error)

	// MarkProcessed transitions a message to StatusProcessed.
	MarkProcessed(ctx context.Context, id string) error

	// MarkFailed transitions a message to StatusFailed and records the error.
	MarkFailed(ctx context.Context, id string, reason error) error

	// IncrementAttempts increments the attempt counter for a message.
	IncrementAttempts(ctx context.Context, id string) error
}
