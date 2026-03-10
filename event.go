// Package events provides the core types and ports for a generic event
// publishing and subscribing framework built on hexagonal architecture.
//
// The framework separates the domain (Event, Handler) from infrastructure
// (EventDispatcher implementations like Kafka, RabbitMQ, NATS) via ports.
// The outbox and inbox patterns ensure reliable at-least-once delivery.
package events

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Event is the core domain type representing a single domain event.
type Event struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Payload    []byte            `json:"payload"`
	Metadata   map[string]string `json:"metadata"`
	OccurredAt time.Time         `json:"occurred_at"`
}

// Handler is a function that processes a single event.
// Implementations should be idempotent since the inbox pattern may
// deliver an event more than once in failure scenarios.
type Handler func(ctx context.Context, event Event) error

// New creates an Event with a generated ID and the current UTC timestamp,
// so callers don't have to set those fields manually.
func New(eventType string, payload []byte, metadata map[string]string) Event {
	return Event{
		ID:         uuid.New().String(),
		Type:       eventType,
		Payload:    payload,
		Metadata:   metadata,
		OccurredAt: time.Now().UTC(),
	}
}
