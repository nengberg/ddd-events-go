package events

import (
	"context"
	"encoding/json"
	"fmt"
)

// Wrap serializes data as the JSON payload of a new Event, combining the
// convenience of events.New (auto ID + timestamp) with automatic marshalling.
//
// Typical use in the application / domain layer:
//
//	evt, err := events.Wrap("order.created", OrderCreatedEvent{...}, nil)
//	outboxDispatcher.Dispatch(ctx, evt)
func Wrap[T any](eventType string, data T, metadata map[string]string) (Event, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("events.Wrap %q: %w", eventType, err)
	}
	return New(eventType, payload, metadata), nil
}

// Unwrap deserializes the JSON payload of e into T.
// Use this when you need the raw Event alongside the typed data, or when
// you are not using TypedHandler.
//
//	data, err := events.Unwrap[OrderCreatedEvent](e)
func Unwrap[T any](e Event) (T, error) {
	var data T
	if err := json.Unmarshal(e.Payload, &data); err != nil {
		return data, fmt.Errorf("events.Unwrap %q: %w", e.Type, err)
	}
	return data, nil
}

// TypedHandler wraps a domain-typed handler function as an events.Handler.
// The payload is automatically deserialized from JSON before fn is called,
// so handlers work directly with their domain type without any marshal boilerplate.
//
//	b.Subscribe("order.created", events.TypedHandler(func(ctx context.Context, e events.Event, data OrderCreatedEvent) error {
//	    // data is fully typed here
//	    return nil
//	}))
func TypedHandler[T any](fn func(ctx context.Context, event Event, data T) error) Handler {
	return func(ctx context.Context, event Event) error {
		data, err := Unwrap[T](event)
		if err != nil {
			return err
		}
		return fn(ctx, event, data)
	}
}
