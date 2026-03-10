// Package outbox implements the outbox pattern for reliable event publishing.
//
// Instead of publishing directly to the broker, callers write events to a
// durable outbox store (e.g., in the same database transaction as their
// business data). A background Processor then reads pending messages and
// forwards them to the real EventDispatcher, ensuring events are never lost
// even if the broker is temporarily unavailable.
//
// Message status lifecycle:
//
//	Pending -> Processed  (success)
//	Pending -> Failed     (exceeded MaxAttempts)
package outbox

import (
	"time"

	events "github.com/nengberg/ddd-events"
)

// Status represents the processing state of an outbox message.
type Status string

const (
	StatusPending   Status = "pending"
	StatusProcessed Status = "processed"
	StatusFailed    Status = "failed"
)

// Message is a durable record of an event that needs to be dispatched.
type Message struct {
	ID          string
	Event       events.Event
	Status      Status
	Attempts    int
	CreatedAt   time.Time
	ProcessedAt *time.Time
	FailedAt    *time.Time
	LastError   string
}
