# Events

A generic event publishing and subscribing framework for Go, built on hexagonal architecture.
It abstracts the underlying broker (Kafka, RabbitMQ, NATS, …) behind simple ports (interfaces)
and uses the **outbox** and **inbox** patterns to guarantee at-least-once delivery.

---

## Architecture

```
Application code
      │
      ▼
outbox.Dispatcher  ──► OutboxStore (durable pending queue)
                              │
                      outbox.Processor (background)
                              │
                              ▼
                     EventDispatcher port  ◄── adapter/memory.Dispatcher
                     (Kafka, RabbitMQ …)       (or Kafka/RabbitMQ adapter)
                              │
                              ▼
                      inbox.Receiver  ──► InboxStore (durable pending queue)
                                                │
                                        inbox.Processor (background)
                                                │
                                                ▼
                                           bus.Bus
                                          /   |   \
                                    handlerA  B   C  (fan-out)
```

### Packages

| Package | Responsibility |
|---|---|
| `events` (root) | Core `Event` type, `Handler` func type, `EventDispatcher` port, `New` / `Wrap` / `Unwrap` / `TypedHandler` helpers |
| `outbox` | Outbox message, store port, outbox-backed `Dispatcher`, polling `Processor` |
| `inbox` | Inbox message, store port, `Receiver` (writes to inbox), polling `Processor` |
| `bus` | `Bus` — fan-out to multiple `Handler` functions per event type, `Middleware` support |
| `adapter/memory` | In-memory implementations of all stores and the broker dispatcher |

---

## Inbox / Outbox Pattern

### Outbox

The **Transactional Outbox** pattern solves the dual-write problem: how to atomically update
your database *and* publish an event to a broker without using distributed transactions (2PC).

Instead of calling the broker directly, your application writes the event into an **outbox table**
as part of the same database transaction as your business data. A background `outbox.Processor`
polls the table and forwards pending messages to the real broker, then marks them as processed.

**Benefits:**
- No event is lost if the broker is temporarily down.
- No event is published if your DB transaction rolls back.
- Outbox state (pending / failed / attempts) can drive metrics and alerts.

Reference: [Transactional Outbox — microservices.io](https://microservices.io/patterns/data/transactional-outbox.html)

### Inbox

The **Inbox** pattern mirrors the outbox on the consumer side. When an event arrives from the
broker, the `inbox.Receiver` stores it in an **inbox table** before any handler runs. The
background `inbox.Processor` then delivers each pending message to the handler (typically
`bus.Bus.Handle`), and marks it processed on success.

**Benefits:**
- Events survive process restarts — unprocessed messages stay pending.
- Retry with `MaxAttempts` guards against infinite loops; failures are recorded for alerting.
- Inbox state gives full observability into received-but-unprocessed events.

The inbox pattern also enables **idempotent consumption**: by checking whether an event ID has
already been processed before handling it, duplicate deliveries (which at-least-once brokers
can produce) are silently discarded.

Reference: [Idempotent Consumer — microservices.io](https://microservices.io/patterns/communication-style/idempotent-consumer.html)

### Related reading

- [Polling Publisher](https://microservices.io/patterns/data/polling-publisher.html) — the mechanism this framework uses to drain the outbox
- [Transaction Log Tailing](https://microservices.io/patterns/data/transaction-log-tailing.html) — an alternative outbox drain strategy (e.g. Debezium / CDC)
- [Messaging](https://microservices.io/patterns/communication-style/messaging.html) — the broader messaging pattern the outbox / inbox enable
- [Saga](https://microservices.io/patterns/data/saga.html) — orchestrating multi-step workflows where each step publishes via outbox

---

## Hexagonal Architecture

The framework separates **domain** from **infrastructure** via ports (interfaces):

- **Port (inbound):** none — callers use `outbox.Dispatcher` directly.
- **Port (outbound / publishing):** `events.EventDispatcher` — implemented by broker adapters.
- **Port (outbound / persistence):** `outbox.Store` and `inbox.Store` — implemented by DB adapters.

Adding a new broker means writing a single `EventDispatcher` adapter. Adding a new persistence
backend means implementing `outbox.Store` and `inbox.Store`. No framework code changes.

---

## Quick start

```go
// Wire infrastructure.
outboxStore  := memory.NewOutboxStore()
inboxStore   := memory.NewInboxStore()
broker       := memory.NewDispatcher()   // swap for Kafka adapter in production

// Wire consuming side.
b        := bus.New()
receiver := inbox.NewReceiver(inboxStore)
broker.Subscribe("order.created", receiver.Receive)

// Register domain handlers (plain or typed).
b.Subscribe("order.created", func(ctx context.Context, e events.Event) error {
    // handle the raw event ...
    return nil
})

// Publishing side (what application code calls).
publisher := outbox.NewDispatcher(outboxStore)

// In your business logic (ideally inside the same DB transaction):
publisher.Dispatch(ctx, events.New("order.created", payload, nil))

// Start background processors.
outboxProc := outbox.NewProcessor(outboxStore, broker,
    outbox.ProcessorConfig{Interval: 5 * time.Second, MaxAttempts: 5}, nil)
inboxProc  := inbox.NewProcessor(inboxStore, b.Handle,
    inbox.ProcessorConfig{Interval: 5 * time.Second, MaxAttempts: 5}, nil)

go outboxProc.Run(ctx)
go inboxProc.Run(ctx)
```

---

## Typed domain events

The framework keeps `Event.Payload` as `[]byte` so it stays broker-agnostic. Your application
defines its own domain event structs, and the framework provides three helpers in the root
package to remove the marshal/unmarshal boilerplate.

```go
// Your application defines its own event types — the framework never sees these.
type OrderCreatedEvent struct {
    OrderID    string `json:"order_id"`
    CustomerID string `json:"customer_id"`
    Amount     int64  `json:"amount"`
}
```

**Publishing** — `events.Wrap` marshals the struct and calls `events.New` in one step:

```go
evt, err := events.Wrap("order.created", OrderCreatedEvent{
    OrderID:    "ord-1",
    CustomerID: "cust-A",
    Amount:     9900,
}, nil)
publisher.Dispatch(ctx, evt)
```

**Subscribing** — `events.TypedHandler` deserializes the payload before calling your handler,
so the handler body works with the concrete type directly:

```go
b.Subscribe("order.created", events.TypedHandler(func(ctx context.Context, e events.Event, data OrderCreatedEvent) error {
    // data is fully typed — no json.Unmarshal in sight
    fmt.Printf("order %s placed by %s\n", data.OrderID, data.CustomerID)
    return nil
}))
```

If you need the typed data outside a handler (e.g. in a test or a manual consumer), use
`events.Unwrap` directly:

```go
data, err := events.Unwrap[OrderCreatedEvent](e)
```

---

## Implementing a real broker adapter

Implement `events.EventDispatcher` for publishing:

```go
type KafkaDispatcher struct { producer *kafka.Producer }

func (k *KafkaDispatcher) Dispatch(ctx context.Context, e events.Event) error {
    // serialize and produce to Kafka topic based on e.Type
}
```

Wire your Kafka consumer to call `inbox.Receiver.Receive` for each consumed message — the inbox
then takes over durable delivery to the bus.

---

## Running tests

```
go test ./...
```
