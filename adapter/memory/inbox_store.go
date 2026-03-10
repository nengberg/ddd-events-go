package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nengberg/ddd-events/inbox"
)

// InboxStore is an in-memory implementation of inbox.Store.
type InboxStore struct {
	mu       sync.Mutex
	messages map[string]*inbox.Message
	order    []string // insertion order for FindPending
}

// NewInboxStore returns an empty InboxStore.
func NewInboxStore() *InboxStore {
	return &InboxStore{
		messages: make(map[string]*inbox.Message),
	}
}

func (s *InboxStore) Save(_ context.Context, msg inbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := msg
	s.messages[msg.ID] = &cp
	s.order = append(s.order, msg.ID)
	return nil
}

func (s *InboxStore) FindPending(_ context.Context) ([]inbox.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []inbox.Message
	for _, id := range s.order {
		msg := s.messages[id]
		if msg.Status == inbox.StatusPending {
			out = append(out, *msg)
		}
	}
	return out, nil
}

func (s *InboxStore) MarkProcessed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return fmt.Errorf("inbox memory store: message %q not found", id)
	}
	now := time.Now().UTC()
	msg.Status = inbox.StatusProcessed
	msg.ProcessedAt = &now
	return nil
}

func (s *InboxStore) MarkFailed(_ context.Context, id string, reason error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return fmt.Errorf("inbox memory store: message %q not found", id)
	}
	now := time.Now().UTC()
	msg.Status = inbox.StatusFailed
	msg.FailedAt = &now
	if reason != nil {
		msg.LastError = reason.Error()
	}
	return nil
}

func (s *InboxStore) IncrementAttempts(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return fmt.Errorf("inbox memory store: message %q not found", id)
	}
	msg.Attempts++
	return nil
}

// All returns a snapshot of all messages (useful in tests for assertions).
func (s *InboxStore) All() []inbox.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]inbox.Message, 0, len(s.messages))
	for _, id := range s.order {
		out = append(out, *s.messages[id])
	}
	return out
}
