package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	events "github.com/nengberg/ddd-events"
	"github.com/nengberg/ddd-events/adapter/memory"
	"github.com/nengberg/ddd-events/bus"
	"github.com/nengberg/ddd-events/inbox"
	"github.com/nengberg/ddd-events/outbox"
)

type orderCreated struct {
	OrderID    string `json:"order_id"`
	CustomerID string `json:"customer_id"`
	Amount     int64  `json:"amount"`
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// runOutboxProcessor runs the outbox processor for one batch cycle (processes
// whatever is in the store right now) without starting the polling loop.
func runOutboxOnce(ctx context.Context, t *testing.T, store *memory.OutboxStore, dispatcher events.EventDispatcher) {
	t.Helper()
	msgs, err := store.FindPending(ctx)
	if err != nil {
		t.Fatalf("outbox find pending: %v", err)
	}
	for _, msg := range msgs {
		if err := store.IncrementAttempts(ctx, msg.ID); err != nil {
			t.Fatalf("outbox increment attempts: %v", err)
		}
		if err := dispatcher.Dispatch(ctx, msg.Event); err != nil {
			t.Fatalf("outbox dispatch: %v", err)
		}
		if err := store.MarkProcessed(ctx, msg.ID); err != nil {
			t.Fatalf("outbox mark processed: %v", err)
		}
	}
}

// runInboxOnce processes whatever is currently pending in the inbox store.
func runInboxOnce(ctx context.Context, t *testing.T, store *memory.InboxStore, handler events.Handler) {
	t.Helper()
	msgs, err := store.FindPending(ctx)
	if err != nil {
		t.Fatalf("inbox find pending: %v", err)
	}
	for _, msg := range msgs {
		if err := store.IncrementAttempts(ctx, msg.ID); err != nil {
			t.Fatalf("inbox increment attempts: %v", err)
		}
		if err := handler(ctx, msg.Event); err != nil {
			t.Fatalf("inbox handler: %v", err)
		}
		if err := store.MarkProcessed(ctx, msg.ID); err != nil {
			t.Fatalf("inbox mark processed: %v", err)
		}
	}
}

// ---- tests ------------------------------------------------------------------

// TestBus_FanOut verifies that multiple subscribers registered for the same
// event type all receive the event.
func TestBus_FanOut(t *testing.T) {
	b := bus.New()

	var mu sync.Mutex
	var received []string

	collect := func(name string) events.Handler {
		return func(_ context.Context, e events.Event) error {
			mu.Lock()
			received = append(received, name)
			mu.Unlock()
			return nil
		}
	}

	b.Subscribe("order.created", collect("handler-A"))
	b.Subscribe("order.created", collect("handler-B"))
	b.Subscribe("order.created", collect("handler-C"))
	// A different event type — should NOT fire for order.created.
	b.Subscribe("order.cancelled", collect("handler-D"))

	evt := events.Event{
		ID:         "evt-1",
		Type:       "order.created",
		Payload:    mustMarshal(t, orderCreated{OrderID: "ord-1"}),
		OccurredAt: time.Now().UTC(),
	}

	if err := b.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Errorf("expected 3 handlers called, got %d: %v", len(received), received)
	}
	for _, name := range []string{"handler-A", "handler-B", "handler-C"} {
		if !slices.Contains(received, name) {
			t.Errorf("handler %q was not called", name)
		}
	}
}

// TestBus_NoHandlers verifies that dispatching an event with no subscribers is
// a no-op and does not return an error.
func TestBus_NoHandlers(t *testing.T) {
	b := bus.New()
	evt := events.Event{ID: "evt-1", Type: "ghost.event"}
	if err := b.Handle(context.Background(), evt); err != nil {
		t.Fatalf("expected no error for unsubscribed event type, got: %v", err)
	}
}

// TestOutbox_DispatchWritesToStore verifies that calling Dispatch on an outbox
// Dispatcher persists the event as a pending outbox message.
func TestOutbox_DispatchWritesToStore(t *testing.T) {
	store := memory.NewOutboxStore()
	dispatcher := outbox.NewDispatcher(store)

	evt := events.Event{
		ID:      "evt-1",
		Type:    "order.created",
		Payload: mustMarshal(t, orderCreated{OrderID: "ord-1"}),
	}

	if err := dispatcher.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	msgs := store.All()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 outbox message, got %d", len(msgs))
	}
	if msgs[0].Status != outbox.StatusPending {
		t.Errorf("expected status pending, got %s", msgs[0].Status)
	}
	if msgs[0].Event.Type != "order.created" {
		t.Errorf("unexpected event type: %s", msgs[0].Event.Type)
	}
}

// TestOutbox_ProcessorForwardsToDispatcher verifies that the outbox Processor
// picks up pending messages and forwards them to the underlying dispatcher,
// marking them as processed afterwards.
func TestOutbox_ProcessorForwardsToDispatcher(t *testing.T) {
	ctx := context.Background()
	outboxStore := memory.NewOutboxStore()
	broker := memory.NewDispatcher()

	// Subscribe a capture handler on the broker side.
	var dispatched []events.Event
	broker.Subscribe("order.created", func(_ context.Context, e events.Event) error {
		dispatched = append(dispatched, e)
		return nil
	})

	// Write directly to the outbox store (simulating a transactional write).
	outboxDispatcher := outbox.NewDispatcher(outboxStore)
	for i := range 3 {
		_ = outboxDispatcher.Dispatch(ctx, events.Event{
			ID:   string(rune('A' + i)),
			Type: "order.created",
		})
	}

	// Run one processor cycle.
	cfg := outbox.ProcessorConfig{Interval: time.Hour, MaxAttempts: 3}
	proc := outbox.NewProcessor(outboxStore, broker, cfg, nil)

	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	// Run runs until ctx is cancelled; it processes the initial batch immediately.
	_ = proc.Run(runCtx)

	if len(dispatched) != 3 {
		t.Errorf("expected 3 events dispatched, got %d", len(dispatched))
	}

	for _, msg := range outboxStore.All() {
		if msg.Status != outbox.StatusProcessed {
			t.Errorf("message %s: expected status processed, got %s", msg.ID, msg.Status)
		}
	}
}

// TestInbox_ReceiverWritesToStore verifies that calling Receive stores the
// event as a pending inbox message.
func TestInbox_ReceiverWritesToStore(t *testing.T) {
	store := memory.NewInboxStore()
	receiver := inbox.NewReceiver(store)

	evt := events.Event{
		ID:   "evt-1",
		Type: "order.created",
	}

	if err := receiver.Receive(context.Background(), evt); err != nil {
		t.Fatalf("Receive: %v", err)
	}

	msgs := store.All()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 inbox message, got %d", len(msgs))
	}
	if msgs[0].Status != inbox.StatusPending {
		t.Errorf("expected status pending, got %s", msgs[0].Status)
	}
}

// TestInbox_ProcessorDeliversToHandler verifies that the inbox Processor
// picks up pending messages and delivers them to the handler.
func TestInbox_ProcessorDeliversToHandler(t *testing.T) {
	ctx := context.Background()
	inboxStore := memory.NewInboxStore()
	receiver := inbox.NewReceiver(inboxStore)

	b := bus.New()
	var received []string
	b.Subscribe("order.created", func(_ context.Context, e events.Event) error {
		received = append(received, e.ID)
		return nil
	})

	// Simulate events arriving from the broker.
	for _, id := range []string{"e1", "e2", "e3"} {
		if err := receiver.Receive(ctx, events.Event{ID: id, Type: "order.created"}); err != nil {
			t.Fatal(err)
		}
	}

	cfg := inbox.ProcessorConfig{Interval: time.Hour, MaxAttempts: 3}
	proc := inbox.NewProcessor(inboxStore, b.Handle, cfg, nil)

	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = proc.Run(runCtx)

	if len(received) != 3 {
		t.Errorf("expected 3 events delivered, got %d: %v", len(received), received)
	}

	for _, msg := range inboxStore.All() {
		if msg.Status != inbox.StatusProcessed {
			t.Errorf("message %s: expected status processed, got %s", msg.ID, msg.Status)
		}
	}
}

// TestInbox_MaxAttemptsMarksAsFailed verifies that a handler that always
// errors causes the inbox message to be marked as failed after MaxAttempts.
func TestInbox_MaxAttemptsMarksAsFailed(t *testing.T) {
	ctx := context.Background()
	inboxStore := memory.NewInboxStore()
	receiver := inbox.NewReceiver(inboxStore)

	var attempts int32
	alwaysFails := func(_ context.Context, _ events.Event) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("transient error")
	}

	if err := receiver.Receive(ctx, events.Event{ID: "e1", Type: "flaky.event"}); err != nil {
		t.Fatal(err)
	}

	maxAttempts := 3
	cfg := inbox.ProcessorConfig{Interval: 10 * time.Millisecond, MaxAttempts: maxAttempts}
	proc := inbox.NewProcessor(inboxStore, alwaysFails, cfg, nil)

	// Run long enough for multiple polling cycles.
	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = proc.Run(runCtx)

	msgs := inboxStore.All()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 inbox message, got %d", len(msgs))
	}
	if msgs[0].Status != inbox.StatusFailed {
		t.Errorf("expected status failed, got %s", msgs[0].Status)
	}
	if msgs[0].Attempts != maxAttempts {
		t.Errorf("expected %d attempts, got %d", maxAttempts, msgs[0].Attempts)
	}
}

// TestOutbox_MaxAttemptsMarksAsFailed mirrors the inbox retry test for outbox.
func TestOutbox_MaxAttemptsMarksAsFailed(t *testing.T) {
	ctx := context.Background()
	outboxStore := memory.NewOutboxStore()

	alwaysFails := events.EventDispatcher(dispatcherFunc(func(_ context.Context, _ events.Event) error {
		return errors.New("broker unavailable")
	}))

	outboxDispatcher := outbox.NewDispatcher(outboxStore)
	if err := outboxDispatcher.Dispatch(ctx, events.Event{ID: "e1", Type: "order.created"}); err != nil {
		t.Fatal(err)
	}

	maxAttempts := 3
	cfg := outbox.ProcessorConfig{Interval: 10 * time.Millisecond, MaxAttempts: maxAttempts}
	proc := outbox.NewProcessor(outboxStore, alwaysFails, cfg, nil)

	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = proc.Run(runCtx)

	msgs := outboxStore.All()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 outbox message, got %d", len(msgs))
	}
	if msgs[0].Status != outbox.StatusFailed {
		t.Errorf("expected status failed, got %s", msgs[0].Status)
	}
	if msgs[0].Attempts != maxAttempts {
		t.Errorf("expected %d attempts, got %d", maxAttempts, msgs[0].Attempts)
	}
}

// TestFullFlow_OutboxToBrokerToInboxToBus exercises the complete end-to-end
// path using one-shot processing steps to avoid timing races:
//
//	outbox.Dispatcher → [outbox batch] → memory.Dispatcher (fake broker)
//	→ inbox.Receiver → [inbox batch] → bus.Bus → handlers
func TestFullFlow_OutboxToBrokerToInboxToBus(t *testing.T) {
	ctx := context.Background()

	// --- Infrastructure wiring -----------------------------------------------

	outboxStore := memory.NewOutboxStore()
	inboxStore := memory.NewInboxStore()

	// The fake broker: synchronously delivers to registered consumers.
	broker := memory.NewDispatcher()

	// The event bus fans out to multiple domain handlers.
	b := bus.New()

	// The inbox receiver stores events durably as they arrive from the broker.
	receiver := inbox.NewReceiver(inboxStore)

	// Wire broker → inbox receiver (simulates broker consumer).
	broker.Subscribe("order.created", receiver.Receive)
	broker.Subscribe("order.shipped", receiver.Receive)

	// Domain handlers registered on the bus.
	var (
		handledEvents    []events.Event
		emailsSent       []string
		inventoryUpdates []string
	)

	b.Subscribe("order.created", func(_ context.Context, e events.Event) error {
		handledEvents = append(handledEvents, e)
		return nil
	})
	b.Subscribe("order.created", func(_ context.Context, e events.Event) error {
		var payload orderCreated
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return err
		}
		emailsSent = append(emailsSent, payload.CustomerID)
		return nil
	})
	b.Subscribe("order.shipped", func(_ context.Context, e events.Event) error {
		inventoryUpdates = append(inventoryUpdates, e.ID)
		return nil
	})

	// The outbox dispatcher is what application code calls.
	outboxDispatcher := outbox.NewDispatcher(outboxStore)

	// --- Application code (simulated) ----------------------------------------

	toPublish := []events.Event{
		{
			ID:      "ord-1",
			Type:    "order.created",
			Payload: mustMarshal(t, orderCreated{OrderID: "ord-1", CustomerID: "cust-A", Amount: 9900}),
		},
		{
			ID:      "ord-2",
			Type:    "order.created",
			Payload: mustMarshal(t, orderCreated{OrderID: "ord-2", CustomerID: "cust-B", Amount: 4900}),
		},
		{
			ID:   "shp-1",
			Type: "order.shipped",
		},
	}
	for _, evt := range toPublish {
		if err := outboxDispatcher.Dispatch(ctx, evt); err != nil {
			t.Fatalf("outbox dispatch %s: %v", evt.ID, err)
		}
	}

	// --- Sequential one-shot processing (avoids timing races in tests) -------
	// Step 1: outbox → broker → inbox (synchronous via memory.Dispatcher).
	runOutboxOnce(ctx, t, outboxStore, broker)
	// Step 2: inbox → bus → handlers.
	runInboxOnce(ctx, t, inboxStore, b.Handle)

	// --- Assertions ----------------------------------------------------------

	if len(handledEvents) != 2 {
		t.Errorf("expected 2 order.created events, got %d", len(handledEvents))
	}
	if len(emailsSent) != 2 {
		t.Errorf("expected 2 emails sent, got %d: %v", len(emailsSent), emailsSent)
	}
	if len(inventoryUpdates) != 1 {
		t.Errorf("expected 1 inventory update, got %d", len(inventoryUpdates))
	}

	for _, msg := range outboxStore.All() {
		if msg.Status != outbox.StatusProcessed {
			t.Errorf("outbox message %s: expected processed, got %s", msg.ID, msg.Status)
		}
	}
	for _, msg := range inboxStore.All() {
		if msg.Status != inbox.StatusProcessed {
			t.Errorf("inbox message %s: expected processed, got %s", msg.ID, msg.Status)
		}
	}
}

// TestFullFlow_MultipleSubscribersPerEventType verifies that all registered
// subscribers fire for a single event across the full outbox→inbox→bus path.
func TestFullFlow_MultipleSubscribersPerEventType(t *testing.T) {
	ctx := context.Background()

	outboxStore := memory.NewOutboxStore()
	inboxStore := memory.NewInboxStore()
	broker := memory.NewDispatcher()
	b := bus.New()
	receiver := inbox.NewReceiver(inboxStore)

	broker.Subscribe("payment.processed", receiver.Receive)

	const numSubscribers = 5
	var counter int32
	for range numSubscribers {
		b.Subscribe("payment.processed", func(_ context.Context, _ events.Event) error {
			atomic.AddInt32(&counter, 1)
			return nil
		})
	}

	outboxDispatcher := outbox.NewDispatcher(outboxStore)
	if err := outboxDispatcher.Dispatch(ctx, events.Event{ID: "pay-1", Type: "payment.processed"}); err != nil {
		t.Fatal(err)
	}

	// Sequential one-shot: outbox → broker → inbox, then inbox → bus → handlers.
	runOutboxOnce(ctx, t, outboxStore, broker)
	runInboxOnce(ctx, t, inboxStore, b.Handle)

	if got := atomic.LoadInt32(&counter); got != numSubscribers {
		t.Errorf("expected %d subscriber calls, got %d", numSubscribers, got)
	}
}

// ---- feature tests ----------------------------------------------------------

// TestWrap_Unwrap verifies the round-trip: Wrap serializes a domain type into
// an Event payload, Unwrap deserializes it back.
func TestWrap_Unwrap(t *testing.T) {
	type orderShipped struct {
		OrderID    string `json:"order_id"`
		TrackingID string `json:"tracking_id"`
	}

	original := orderShipped{OrderID: "ord-1", TrackingID: "track-42"}
	evt, err := events.Wrap("order.shipped", original, map[string]string{"region": "eu"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if evt.Type != "order.shipped" {
		t.Errorf("unexpected type: %s", evt.Type)
	}
	if evt.ID == "" || evt.OccurredAt.IsZero() {
		t.Error("expected ID and OccurredAt to be set by Wrap")
	}

	got, err := events.Unwrap[orderShipped](evt)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if got != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, original)
	}
}

// TestTypedHandler shows how the application layer registers a domain-typed
// handler without any marshal/unmarshal boilerplate in the handler body.
func TestTypedHandler(t *testing.T) {
	type paymentProcessed struct {
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount"`
	}

	var received []paymentProcessed

	b := bus.New()
	b.Subscribe("payment.processed", events.TypedHandler(func(_ context.Context, _ events.Event, data paymentProcessed) error {
		received = append(received, data)
		return nil
	}))

	// Publisher side: Wrap the domain struct into an Event.
	payments := []paymentProcessed{
		{PaymentID: "pay-1", Amount: 1000},
		{PaymentID: "pay-2", Amount: 2500},
	}
	for _, p := range payments {
		evt, err := events.Wrap("payment.processed", p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Handle(context.Background(), evt); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	for i, want := range payments {
		if received[i] != want {
			t.Errorf("[%d] got %+v, want %+v", i, received[i], want)
		}
	}
}

// TestTypedHandler_UnmarshalError verifies that a malformed payload causes
// TypedHandler to return an error rather than panic or silently drop data.
func TestTypedHandler_UnmarshalError(t *testing.T) {
	type myEvent struct{ Value int }

	b := bus.New()
	b.Subscribe("bad.event", events.TypedHandler(func(_ context.Context, _ events.Event, _ myEvent) error {
		return nil
	}))

	malformed := events.Event{ID: "e1", Type: "bad.event", Payload: []byte(`not json`)}
	if err := b.Handle(context.Background(), malformed); err == nil {
		t.Error("expected error for malformed payload, got nil")
	}
}

// TestNewEvent verifies that events.New populates ID, Type, Payload, Metadata,
// and OccurredAt without the caller having to set them manually.
func TestNewEvent(t *testing.T) {
	payload := []byte(`{"amount":100}`)
	meta := map[string]string{"source": "checkout"}

	e := events.New("order.created", payload, meta)

	if e.ID == "" {
		t.Error("expected non-empty ID")
	}
	if e.Type != "order.created" {
		t.Errorf("expected type %q, got %q", "order.created", e.Type)
	}
	if string(e.Payload) != string(payload) {
		t.Errorf("unexpected payload: %s", e.Payload)
	}
	if e.Metadata["source"] != "checkout" {
		t.Errorf("unexpected metadata: %v", e.Metadata)
	}
	if e.OccurredAt.IsZero() {
		t.Error("expected non-zero OccurredAt")
	}

	// Two calls must produce distinct IDs.
	e2 := events.New("order.created", nil, nil)
	if e.ID == e2.ID {
		t.Errorf("expected unique IDs, both got %q", e.ID)
	}
}

// TestBus_Middleware_WrapsEachHandler verifies that middleware registered with
// Use is applied to every handler individually and fires once per handler call.
func TestBus_Middleware_WrapsEachHandler(t *testing.T) {
	var callLog []string

	recording := func(label string) bus.Middleware {
		return func(next events.Handler) events.Handler {
			return func(ctx context.Context, e events.Event) error {
				callLog = append(callLog, fmt.Sprintf("%s:before", label))
				err := next(ctx, e)
				callLog = append(callLog, fmt.Sprintf("%s:after", label))
				return err
			}
		}
	}

	b := bus.New()
	b.Use(recording("mw1"), recording("mw2"))
	b.Subscribe("order.created", func(_ context.Context, _ events.Event) error { return nil })
	b.Subscribe("order.created", func(_ context.Context, _ events.Event) error { return nil })

	if err := b.Handle(context.Background(), events.Event{ID: "e1", Type: "order.created"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// 2 handlers × (mw1 before + mw2 before + mw2 after + mw1 after) = 8 entries.
	if len(callLog) != 8 {
		t.Errorf("expected 8 middleware log entries, got %d: %v", len(callLog), callLog)
	}
	// Each handler invocation should be wrapped outermost-first: mw1 → mw2 → handler.
	for i := range 2 {
		base := i * 4
		want := []string{
			"mw1:before",
			"mw2:before",
			"mw2:after",
			"mw1:after",
		}
		for j, w := range want {
			if callLog[base+j] != w {
				t.Errorf("entry[%d]=%q, want %q", base+j, callLog[base+j], w)
			}
		}
	}
}

// TestBus_LoggingMiddleware verifies that LoggingMiddleware writes event_id
// and event_type to the provided slog.Logger on successful handling.
func TestBus_LoggingMiddleware(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	b := bus.New()
	b.Use(bus.LoggingMiddleware(logger))
	b.Subscribe("payment.processed", func(_ context.Context, _ events.Event) error { return nil })

	evt := events.New("payment.processed", nil, nil)
	if err := b.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, evt.ID) {
		t.Errorf("expected log to contain event ID %q, got:\n%s", evt.ID, logged)
	}
	if !strings.Contains(logged, "payment.processed") {
		t.Errorf("expected log to contain event type, got:\n%s", logged)
	}
}

// TestBus_LoggingMiddleware_Error verifies that handler errors are logged at
// Error level and the error is still propagated to the caller.
func TestBus_LoggingMiddleware_Error(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	b := bus.New()
	b.Use(bus.LoggingMiddleware(logger))
	b.Subscribe("order.created", func(_ context.Context, _ events.Event) error {
		return errors.New("handler blew up")
	})

	err := b.Handle(context.Background(), events.Event{ID: "e1", Type: "order.created"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(buf.String(), "ERROR") {
		t.Errorf("expected ERROR level log, got:\n%s", buf.String())
	}
}

// TestProcessor_GracefulShutdown_Inbox verifies that when Run is called with
// an already-cancelled context, the initial batch still processes all pending
// messages before returning (graceful drain).
func TestProcessor_GracefulShutdown_Inbox(t *testing.T) {
	ctx := context.Background()
	store := memory.NewInboxStore()
	receiver := inbox.NewReceiver(store)

	for i := range 5 {
		if err := receiver.Receive(ctx, events.Event{
			ID:   fmt.Sprintf("e%d", i),
			Type: "test.event",
		}); err != nil {
			t.Fatal(err)
		}
	}

	var handled int32
	handler := func(_ context.Context, _ events.Event) error {
		atomic.AddInt32(&handled, 1)
		return nil
	}

	// Cancel the context before calling Run.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	cfg := inbox.ProcessorConfig{
		Interval:        time.Hour,
		MaxAttempts:     3,
		ShutdownTimeout: 5 * time.Second,
	}
	_ = inbox.NewProcessor(store, handler, cfg, nil).Run(cancelledCtx)

	if got := atomic.LoadInt32(&handled); got != 5 {
		t.Errorf("expected 5 events handled during graceful shutdown, got %d", got)
	}
	for _, msg := range store.All() {
		if msg.Status != inbox.StatusProcessed {
			t.Errorf("message %s: expected processed, got %s", msg.ID, msg.Status)
		}
	}
}

// TestProcessor_GracefulShutdown_Outbox mirrors the inbox graceful-shutdown
// test for the outbox processor.
func TestProcessor_GracefulShutdown_Outbox(t *testing.T) {
	ctx := context.Background()
	store := memory.NewOutboxStore()

	var dispatched int32
	capturingDispatcher := dispatcherFunc(func(_ context.Context, _ events.Event) error {
		atomic.AddInt32(&dispatched, 1)
		return nil
	})

	outboxDispatcher := outbox.NewDispatcher(store)
	for i := range 5 {
		if err := outboxDispatcher.Dispatch(ctx, events.Event{
			ID:   fmt.Sprintf("e%d", i),
			Type: "test.event",
		}); err != nil {
			t.Fatal(err)
		}
	}

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	cfg := outbox.ProcessorConfig{
		Interval:        time.Hour,
		MaxAttempts:     3,
		ShutdownTimeout: 5 * time.Second,
	}
	_ = outbox.NewProcessor(store, capturingDispatcher, cfg, nil).Run(cancelledCtx)

	if got := atomic.LoadInt32(&dispatched); got != 5 {
		t.Errorf("expected 5 events dispatched during graceful shutdown, got %d", got)
	}
	for _, msg := range store.All() {
		if msg.Status != outbox.StatusProcessed {
			t.Errorf("message %s: expected processed, got %s", msg.ID, msg.Status)
		}
	}
}

// dispatcherFunc is an adapter to allow use of ordinary functions as
// events.EventDispatcher (analogous to http.HandlerFunc).
type dispatcherFunc func(ctx context.Context, event events.Event) error

func (f dispatcherFunc) Dispatch(ctx context.Context, event events.Event) error {
	return f(ctx, event)
}
