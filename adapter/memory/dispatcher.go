// Package memory provides in-memory implementations of the framework's
// persistence and broker ports. Suitable for testing and local development.
package memory

import (
	"context"
	"sync"

	events "github.com/nengberg/ddd-events"
)

// Dispatcher is an in-memory EventDispatcher that acts as a synchronous fake
// broker. When Dispatch is called it immediately delivers the event to all
// handlers registered via Subscribe.
//
// This is the primary test double for a real broker adapter (Kafka, RabbitMQ,
// etc.). In tests it connects the outbox.Processor output to the
// inbox.Receiver input without real infrastructure.
type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[string][]events.Handler
}

// NewDispatcher returns an empty in-memory Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		handlers: make(map[string][]events.Handler),
	}
}

// Subscribe registers handler to be called synchronously whenever an event of
// eventType is dispatched. Intended for wiring the broker's consumer side
// (e.g. to forward events to inbox.Receiver.Receive).
func (d *Dispatcher) Subscribe(eventType string, handler events.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[eventType] = append(d.handlers[eventType], handler)
}

// Dispatch delivers event synchronously to all registered handlers for its
// type. It implements events.EventDispatcher.
func (d *Dispatcher) Dispatch(ctx context.Context, event events.Event) error {
	d.mu.RLock()
	hs := make([]events.Handler, len(d.handlers[event.Type]))
	copy(hs, d.handlers[event.Type])
	d.mu.RUnlock()

	for _, h := range hs {
		if err := h(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
