package events

import "context"

// EventDispatcher is the publishing port.
// Concrete adapters implement this for Kafka, RabbitMQ, NATS, etc.
// The outbox.Dispatcher also implements this interface, wrapping the real
// dispatcher with durable persistence before publishing.
type EventDispatcher interface {
	Dispatch(ctx context.Context, event Event) error
}
