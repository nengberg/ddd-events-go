// Package bus provides an EventBus that fans out events to multiple registered
// handlers. It implements events.Handler so it can be used as the delivery
// target for the inbox.Processor.
//
// Usage:
//
//	b := bus.New()
//	b.Use(bus.LoggingMiddleware(logger))
//	b.Subscribe("order.created", handleOrderCreated)
//	b.Subscribe("order.created", sendOrderConfirmationEmail)
//
//	// Pass b.Handle to inbox.NewProcessor so all handlers fire for each event.
//	processor := inbox.NewProcessor(store, b.Handle, cfg, logger)
package bus

import (
	"context"
	"fmt"
	"sync"

	events "github.com/nengberg/ddd-events"
)

// Middleware wraps a Handler to add cross-cutting behaviour such as logging,
// tracing, or metrics. Middlewares registered with Use are applied to every
// handler on every Handle call, in registration order (first registered is
// outermost wrapper).
type Middleware func(events.Handler) events.Handler

// Bus fans out each event to all handlers registered for its type.
// It is safe for concurrent use.
type Bus struct {
	mu          sync.RWMutex
	handlers    map[string][]events.Handler
	middlewares []Middleware
}

// New returns an empty Bus.
func New() *Bus {
	return &Bus{
		handlers: make(map[string][]events.Handler),
	}
}

// Use appends mws to the middleware chain. Middleware is applied at Handle
// time, so it always wraps every handler regardless of when Subscribe was
// called relative to Use.
func (b *Bus) Use(mws ...Middleware) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.middlewares = append(b.middlewares, mws...)
}

// Subscribe registers handler to be called whenever an event of eventType is
// handled. Multiple handlers may be registered for the same event type; they
// are called in registration order.
func (b *Bus) Subscribe(eventType string, handler events.Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// Handle delivers event to all handlers registered for event.Type, wrapping
// each one with the full middleware chain.
//
// All handlers are called even if an earlier one returns an error; errors are
// collected and returned as a combined error so no handler is silently skipped.
func (b *Bus) Handle(ctx context.Context, event events.Event) error {
	b.mu.RLock()
	hs := make([]events.Handler, len(b.handlers[event.Type]))
	copy(hs, b.handlers[event.Type])
	mws := make([]Middleware, len(b.middlewares))
	copy(mws, b.middlewares)
	b.mu.RUnlock()

	var errs []error
	for _, h := range hs {
		if err := applyMiddleware(h, mws)(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("bus: %d handler(s) failed for event type %q: %w", len(errs), event.Type, errs[0])
	}
	return nil
}

// applyMiddleware wraps h with mws so that mws[0] is the outermost layer.
func applyMiddleware(h events.Handler, mws []Middleware) events.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
