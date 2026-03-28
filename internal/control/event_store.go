package control

import (
	"encoding/binary"
	"fmt"
	"slices"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	bbolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

type eventStore interface {
	Append(string, *agboxv1.SandboxEvent) error
	LoadEvents(string) ([]*agboxv1.SandboxEvent, error)
	LoadAllSandboxIDs() ([]string, error)
	MaxSequence(string) (uint64, error)
	DeletedAt(string) (time.Time, bool, error)
	MarkDeleted(string, time.Time) error
	Cleanup(time.Duration) ([]string, error)
}

type memoryEventStore struct {
	mu        sync.Mutex
	events    map[string][]*agboxv1.SandboxEvent
	deletedAt map[string]time.Time
}

type persistentEventStore struct {
	db *bbolt.DB
}

var eventMetaBucket = []byte("sandbox-deleted-at")

const eventsBucketPrefix = "events:"

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

func (store *memoryEventStore) DeletedAt(sandboxID string) (time.Time, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	deletedAt, ok := store.deletedAt[sandboxID]
	return deletedAt, ok, nil
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

func newPersistentEventStore(db *bbolt.DB) *persistentEventStore {
	return &persistentEventStore{db: db}
}

func (store *persistentEventStore) Append(sandboxID string, event *agboxv1.SandboxEvent) error {
	payload, err := proto.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal sandbox event: %w", err)
	}
	return store.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(eventsBucketName(sandboxID))
		if err != nil {
			return fmt.Errorf("create events bucket for %s: %w", sandboxID, err)
		}
		return bucket.Put(encodeUint64(event.GetSequence()), payload)
	})
}

func (store *persistentEventStore) LoadEvents(sandboxID string) ([]*agboxv1.SandboxEvent, error) {
	var events []*agboxv1.SandboxEvent
	err := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(eventsBucketName(sandboxID))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			event := &agboxv1.SandboxEvent{}
			if err := proto.Unmarshal(value, event); err != nil {
				return fmt.Errorf("unmarshal sandbox event for %s: %w", sandboxID, err)
			}
			events = append(events, event)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (store *persistentEventStore) LoadAllSandboxIDs() ([]string, error) {
	var sandboxIDs []string
	err := store.db.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(bucketName []byte, _ *bbolt.Bucket) error {
			name := string(bucketName)
			if !isEventsBucketName(name) {
				return nil
			}
			sandboxIDs = append(sandboxIDs, name[len(eventsBucketPrefix):])
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(sandboxIDs)
	return sandboxIDs, nil
}

func (store *persistentEventStore) MaxSequence(sandboxID string) (uint64, error) {
	var maxSequence uint64
	err := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(eventsBucketName(sandboxID))
		if bucket == nil {
			return nil
		}
		key, _ := bucket.Cursor().Last()
		if key == nil {
			return nil
		}
		maxSequence = binary.BigEndian.Uint64(key)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return maxSequence, nil
}

func (store *persistentEventStore) MarkDeleted(sandboxID string, deletedAt time.Time) error {
	return store.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(eventMetaBucket)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", eventMetaBucket)
		}
		return bucket.Put([]byte(sandboxID), encodeInt64(deletedAt.UTC().UnixNano()))
	})
}

func (store *persistentEventStore) DeletedAt(sandboxID string) (time.Time, bool, error) {
	var (
		deletedAt time.Time
		ok        bool
	)
	err := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(eventMetaBucket)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", eventMetaBucket)
		}
		value := bucket.Get([]byte(sandboxID))
		if value == nil {
			return nil
		}
		if len(value) != 8 {
			return fmt.Errorf("invalid deleted_at metadata for sandbox %s", sandboxID)
		}
		deletedAt = time.Unix(0, decodeInt64(value)).UTC()
		ok = true
		return nil
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return deletedAt, ok, nil
}

func (store *persistentEventStore) Cleanup(retentionTTL time.Duration) ([]string, error) {
	var removed []string
	err := store.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(eventMetaBucket)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", eventMetaBucket)
		}
		now := time.Now().UTC()
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if len(value) != 8 {
				return fmt.Errorf("invalid deleted_at metadata for sandbox %s", string(key))
			}
			deletedAt := time.Unix(0, decodeInt64(value)).UTC()
			if now.Sub(deletedAt) < retentionTTL {
				continue
			}
			sandboxID := string(key)
			if err := tx.DeleteBucket(eventsBucketName(sandboxID)); err != nil && err != bbolt.ErrBucketNotFound {
				return fmt.Errorf("delete events bucket for %s: %w", sandboxID, err)
			}
			removed = append(removed, sandboxID)
		}
		for _, sandboxID := range removed {
			if err := bucket.Delete([]byte(sandboxID)); err != nil {
				return fmt.Errorf("delete event metadata for %s: %w", sandboxID, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(removed)
	return removed, nil
}

func eventsBucketName(sandboxID string) []byte {
	return []byte(eventsBucketPrefix + sandboxID)
}

func isEventsBucketName(bucketName string) bool {
	return len(bucketName) > len(eventsBucketPrefix) && bucketName[:len(eventsBucketPrefix)] == eventsBucketPrefix
}

func encodeUint64(value uint64) []byte {
	bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, value)
	return bytes
}

func encodeInt64(value int64) []byte {
	return encodeUint64(uint64(value))
}

func decodeInt64(value []byte) int64 {
	return int64(binary.BigEndian.Uint64(value))
}
