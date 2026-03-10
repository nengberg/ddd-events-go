package outbox

import "context"

// Store is the persistence port for outbox messages.
// Implementations might use PostgreSQL, MySQL, MongoDB, or in-memory storage.
type Store interface {
	// Save persists a new outbox message. Typically called within the same
	// database transaction as the business operation that emits the event.
	Save(ctx context.Context, msg Message) error

	// FindPending returns all messages with StatusPending that have not
	// exceeded MaxAttempts. Results should be ordered by CreatedAt ascending.
	FindPending(ctx context.Context) ([]Message, error)

	// MarkProcessed transitions a message to StatusProcessed.
	MarkProcessed(ctx context.Context, id string) error

	// MarkFailed transitions a message to StatusFailed and records the error.
	MarkFailed(ctx context.Context, id string, reason error) error

	// IncrementAttempts increments the attempt counter for a message.
	IncrementAttempts(ctx context.Context, id string) error
}
