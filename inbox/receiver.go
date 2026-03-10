package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	events "github.com/nengberg/ddd-events"
)

// Receiver is called by broker adapters (Kafka, RabbitMQ, etc.) when an event
// arrives. It writes the event to the inbox store so the Processor can handle
// it durably. Use Receive as the delivery target in your subscriber adapter.
type Receiver struct {
	store Store
}

// NewReceiver returns a Receiver backed by the given store.
func NewReceiver(store Store) *Receiver {
	return &Receiver{store: store}
}

// Receive stores the incoming event in the inbox. It implements events.Handler
// so it can be passed directly as the delivery callback to a subscriber adapter.
func (r *Receiver) Receive(ctx context.Context, event events.Event) error {
	msg := Message{
		ID:         uuid.New().String(),
		Event:      event,
		Status:     StatusPending,
		ReceivedAt: time.Now().UTC(),
	}

	if err := r.store.Save(ctx, msg); err != nil {
		return fmt.Errorf("inbox: save message: %w", err)
	}
	return nil
}
