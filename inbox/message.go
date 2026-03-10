// Package inbox implements the inbox pattern for reliable event consumption.
//
// When an event arrives from the broker, it is first written to a durable
// inbox store before any processing occurs. A background Processor then reads
// pending messages and delivers them to the registered handlers (typically via
// the bus.Bus). This ensures that:
//
//   - Events are not lost if the handler panics or the process restarts.
//   - Metrics and alerts can be derived from inbox state (pending, failed, etc.).
//   - Processing is idempotent when handlers are idempotent.
//
// Message status lifecycle:
//
//	Pending -> Processed  (success)
//	Pending -> Failed     (exceeded MaxAttempts)
package inbox

import (
	"time"

	events "github.com/nengberg/ddd-events"
)

// Status represents the processing state of an inbox message.
type Status string

const (
	StatusPending   Status = "pending"
	StatusProcessed Status = "processed"
	StatusFailed    Status = "failed"
)

// Message is a durable record of a received event pending local processing.
type Message struct {
	ID          string
	Event       events.Event
	Status      Status
	Attempts    int
	ReceivedAt  time.Time
	ProcessedAt *time.Time
	FailedAt    *time.Time
	LastError   string
}
