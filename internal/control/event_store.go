package control

import (
	"slices"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

type eventStore interface {
	Append(string, *agboxv1.SandboxEvent) error
	LoadEvents(string) ([]*agboxv1.SandboxEvent, error)
	LoadAllSandboxIDs() ([]string, error)
	MaxSequence(string) (uint64, error)
	MarkDeleted(string, time.Time) error
	Cleanup(time.Duration) ([]string, error)
}

type memoryEventStore struct {
	mu        sync.Mutex
	events    map[string][]*agboxv1.SandboxEvent
	deletedAt map[string]time.Time
}

func newMemoryEventStore() *memoryEventStore {
	return &memoryEventStore{
		events:    make(map[string][]*agboxv1.SandboxEvent),
		deletedAt: make(map[string]time.Time),
	}
}

func (store *memoryEventStore) Append(sandboxID string, event *agboxv1.SandboxEvent) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.events[sandboxID] = append(store.events[sandboxID], cloneEvent(event))
	return nil
}

func (store *memoryEventStore) LoadEvents(sandboxID string) ([]*agboxv1.SandboxEvent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	events := store.events[sandboxID]
	result := make([]*agboxv1.SandboxEvent, 0, len(events))
	for _, event := range events {
		result = append(result, cloneEvent(event))
	}
	return result, nil
}

func (store *memoryEventStore) LoadAllSandboxIDs() ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	ids := make([]string, 0, len(store.events))
	for sandboxID := range store.events {
		ids = append(ids, sandboxID)
	}
	slices.Sort(ids)
	return ids, nil
}

func (store *memoryEventStore) MaxSequence(sandboxID string) (uint64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	events := store.events[sandboxID]
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].GetSequence(), nil
}

func (store *memoryEventStore) MarkDeleted(sandboxID string, deletedAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.deletedAt[sandboxID] = deletedAt.UTC()
	return nil
}

func (store *memoryEventStore) Cleanup(retentionTTL time.Duration) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	now := time.Now().UTC()
	var removed []string
	for sandboxID, deletedAt := range store.deletedAt {
		if now.Sub(deletedAt) < retentionTTL {
			continue
		}
		delete(store.deletedAt, sandboxID)
		delete(store.events, sandboxID)
		removed = append(removed, sandboxID)
	}
	slices.Sort(removed)
	return removed, nil
}
