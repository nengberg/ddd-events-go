package inbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	events "github.com/nengberg/ddd-events"
)

// ProcessorConfig controls the behaviour of the Processor.
type ProcessorConfig struct {
	// Interval between polling cycles. Defaults to 5 seconds if zero.
	Interval time.Duration
	// MaxAttempts is the maximum number of handler invocations before a message
	// is marked as failed. Defaults to 3 if zero.
	MaxAttempts int
	// ShutdownTimeout is the maximum time allowed for the final drain batch
	// that runs after ctx is cancelled. Defaults to 30 seconds if zero.
	ShutdownTimeout time.Duration
}

func (c *ProcessorConfig) setDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 30 * time.Second
	}
}

// Processor polls the inbox store and delivers pending messages to the handler.
// The handler is typically a bus.Bus, which fans out to all registered
// subscribers. Run it as a background goroutine.
type Processor struct {
	store   Store
	handler events.Handler
	cfg     ProcessorConfig
	logger  *slog.Logger
}

// NewProcessor creates a Processor. handler is called for each pending message
// (e.g. bus.Bus.Handle or a single events.Handler).
func NewProcessor(store Store, handler events.Handler, cfg ProcessorConfig, logger *slog.Logger) *Processor {
	cfg.setDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &Processor{
		store:   store,
		handler: handler,
		cfg:     cfg,
		logger:  logger,
	}
}

// Run starts the polling loop. It blocks until ctx is cancelled, then performs
// one final drain batch before returning so that in-flight messages are not
// abandoned on a clean shutdown.
func (p *Processor) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	// Initial batch: use WithoutCancel so it completes even if ctx is already
	// cancelled (e.g. during a pre-cancelled graceful-shutdown test).
	if err := p.processBatch(context.WithoutCancel(ctx)); err != nil {
		p.logger.Error("inbox: initial batch failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			p.drain(ctx)
			return ctx.Err()
		case <-ticker.C:
			// WithoutCancel lets the batch finish even if ctx is cancelled
			// between the tick and the end of processing.
			if err := p.processBatch(context.WithoutCancel(ctx)); err != nil {
				p.logger.Error("inbox: batch failed", "error", err)
			}
		}
	}
}

// drain runs one final batch with a fresh context so pending messages written
// just before shutdown are not left in a pending state.
func (p *Processor) drain(ctx context.Context) {
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.ShutdownTimeout)
	defer cancel()
	if err := p.processBatch(drainCtx); err != nil {
		p.logger.Error("inbox: drain failed", "error", err)
	}
}

func (p *Processor) processBatch(ctx context.Context) error {
	messages, err := p.store.FindPending(ctx)
	if err != nil {
		return fmt.Errorf("inbox: find pending: %w", err)
	}

	for _, msg := range messages {
		p.processOne(ctx, msg)
	}
	return nil
}

func (p *Processor) processOne(ctx context.Context, msg Message) {
	if msg.Attempts >= p.cfg.MaxAttempts {
		if err := p.store.MarkFailed(ctx, msg.ID, fmt.Errorf("exceeded max attempts (%d)", p.cfg.MaxAttempts)); err != nil {
			p.logger.Error("inbox: mark failed", "id", msg.ID, "error", err)
		}
		return
	}

	if err := p.store.IncrementAttempts(ctx, msg.ID); err != nil {
		p.logger.Error("inbox: increment attempts", "id", msg.ID, "error", err)
		return
	}

	if err := p.handler(ctx, msg.Event); err != nil {
		p.logger.Error("inbox: handler failed", "id", msg.ID, "event_type", msg.Event.Type, "error", err)
		return
	}

	if err := p.store.MarkProcessed(ctx, msg.ID); err != nil {
		p.logger.Error("inbox: mark processed", "id", msg.ID, "error", err)
	}
}
