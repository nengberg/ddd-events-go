package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nengberg/ddd-events/outbox"
)

// OutboxStore is an in-memory implementation of outbox.Store.
type OutboxStore struct {
	mu       sync.Mutex
	messages map[string]*outbox.Message
	order    []string // insertion order for FindPending
}

// NewOutboxStore returns an empty OutboxStore.
func NewOutboxStore() *OutboxStore {
	return &OutboxStore{
		messages: make(map[string]*outbox.Message),
	}
}

func (s *OutboxStore) Save(_ context.Context, msg outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := msg
	s.messages[msg.ID] = &cp
	s.order = append(s.order, msg.ID)
	return nil
}

func (s *OutboxStore) FindPending(_ context.Context) ([]outbox.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []outbox.Message
	for _, id := range s.order {
		msg := s.messages[id]
		if msg.Status == outbox.StatusPending {
			out = append(out, *msg)
		}
	}
	return out, nil
}

func (s *OutboxStore) MarkProcessed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return fmt.Errorf("outbox memory store: message %q not found", id)
	}
	now := time.Now().UTC()
	msg.Status = outbox.StatusProcessed
	msg.ProcessedAt = &now
	return nil
}

func (s *OutboxStore) MarkFailed(_ context.Context, id string, reason error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return fmt.Errorf("outbox memory store: message %q not found", id)
	}
	now := time.Now().UTC()
	msg.Status = outbox.StatusFailed
	msg.FailedAt = &now
	if reason != nil {
		msg.LastError = reason.Error()
	}
	return nil
}

func (s *OutboxStore) IncrementAttempts(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return fmt.Errorf("outbox memory store: message %q not found", id)
	}
	msg.Attempts++
	return nil
}

// All returns a snapshot of all messages (useful in tests for assertions).
func (s *OutboxStore) All() []outbox.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]outbox.Message, 0, len(s.messages))
	for _, id := range s.order {
		out = append(out, *s.messages[id])
	}
	return out
}
