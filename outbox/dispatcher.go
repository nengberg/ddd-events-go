package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	events "github.com/nengberg/ddd-events"
)

// Dispatcher implements events.EventDispatcher by writing events to the outbox
// store instead of publishing directly to the broker. The Processor then
// forwards them asynchronously, guaranteeing at-least-once delivery.
type Dispatcher struct {
	store Store
}

// NewDispatcher returns a Dispatcher backed by the given store.
func NewDispatcher(store Store) *Dispatcher {
	return &Dispatcher{store: store}
}

// Dispatch writes the event to the outbox. This is safe to call within a
// database transaction alongside your business logic writes.
func (d *Dispatcher) Dispatch(ctx context.Context, event events.Event) error {
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}

	msg := Message{
		ID:        uuid.New().String(),
		Event:     event,
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}

	if err := d.store.Save(ctx, msg); err != nil {
		return fmt.Errorf("outbox: save message: %w", err)
	}
	return nil
}
