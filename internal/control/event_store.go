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
	SaveSandboxConfig(sandboxID string, spec *agboxv1.CreateSpec) error
	LoadSandboxConfig(sandboxID string) (*agboxv1.CreateSpec, error)
	LoadAllSandboxConfigs() (map[string]*agboxv1.CreateSpec, error)
	DeleteSandboxConfig(sandboxID string) error
	SaveExecConfig(sandboxID string, req *agboxv1.CreateExecRequest) error
	LoadExecConfigs(sandboxID string) ([]*agboxv1.CreateExecRequest, error)
}

type memoryEventStore struct {
	mu             sync.Mutex
	events         map[string][]*agboxv1.SandboxEvent
	deletedAt      map[string]time.Time
	sandboxConfigs map[string]*agboxv1.CreateSpec
	execConfigs    map[string]map[string]*agboxv1.CreateExecRequest // sandboxID -> execID -> req
}

type persistentEventStore struct {
	db *bbolt.DB
}

var eventMetaBucket = []byte("sandbox-deleted-at")
var sandboxConfigBucket = []byte("sandbox-config")

const eventsBucketPrefix = "events:"
const execConfigBucketPrefix = "exec-config:"

func newMemoryEventStore() *memoryEventStore {
	return &memoryEventStore{
		events:         make(map[string][]*agboxv1.SandboxEvent),
		deletedAt:      make(map[string]time.Time),
		sandboxConfigs: make(map[string]*agboxv1.CreateSpec),
		execConfigs:    make(map[string]map[string]*agboxv1.CreateExecRequest),
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

	idSet := make(map[string]bool)
	for sandboxID := range store.sandboxConfigs {
		idSet[sandboxID] = true
	}
	for sandboxID := range store.events {
		idSet[sandboxID] = true
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
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
		delete(store.sandboxConfigs, sandboxID)
		delete(store.execConfigs, sandboxID)
		removed = append(removed, sandboxID)
	}
	slices.Sort(removed)
	return removed, nil
}

func (store *memoryEventStore) SaveSandboxConfig(sandboxID string, spec *agboxv1.CreateSpec) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sandboxConfigs[sandboxID] = spec
	return nil
}

func (store *memoryEventStore) LoadSandboxConfig(sandboxID string) (*agboxv1.CreateSpec, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.sandboxConfigs[sandboxID], nil
}

func (store *memoryEventStore) LoadAllSandboxConfigs() (map[string]*agboxv1.CreateSpec, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make(map[string]*agboxv1.CreateSpec, len(store.sandboxConfigs))
	for id, spec := range store.sandboxConfigs {
		result[id] = spec
	}
	return result, nil
}

func (store *memoryEventStore) DeleteSandboxConfig(sandboxID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.sandboxConfigs, sandboxID)
	return nil
}

func (store *memoryEventStore) SaveExecConfig(sandboxID string, req *agboxv1.CreateExecRequest) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.execConfigs[sandboxID] == nil {
		store.execConfigs[sandboxID] = make(map[string]*agboxv1.CreateExecRequest)
	}
	store.execConfigs[sandboxID][req.GetExecId()] = req
	return nil
}

func (store *memoryEventStore) LoadExecConfigs(sandboxID string) ([]*agboxv1.CreateExecRequest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	execMap := store.execConfigs[sandboxID]
	result := make([]*agboxv1.CreateExecRequest, 0, len(execMap))
	for _, req := range execMap {
		result = append(result, req)
	}
	return result, nil
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
		// Use sandbox-config bucket as the authoritative source
		configBucket := tx.Bucket(sandboxConfigBucket)
		if configBucket != nil {
			if err := configBucket.ForEach(func(key, _ []byte) error {
				sandboxIDs = append(sandboxIDs, string(key))
				return nil
			}); err != nil {
				return err
			}
		}
		// Also scan event buckets for any sandbox that has events but no config
		// (should not happen since config is written before events, but handle gracefully)
		configSet := make(map[string]bool, len(sandboxIDs))
		for _, id := range sandboxIDs {
			configSet[id] = true
		}
		return tx.ForEach(func(bucketName []byte, _ *bbolt.Bucket) error {
			name := string(bucketName)
			if !isEventsBucketName(name) {
				return nil
			}
			id := name[len(eventsBucketPrefix):]
			if !configSet[id] {
				sandboxIDs = append(sandboxIDs, id)
			}
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
			// Clean up sandbox config entry.
			if configBucket := tx.Bucket(sandboxConfigBucket); configBucket != nil {
				if err := configBucket.Delete([]byte(sandboxID)); err != nil {
					return fmt.Errorf("delete sandbox config for %s: %w", sandboxID, err)
				}
			}
			// Clean up exec config bucket
			if err := tx.DeleteBucket(execConfigBucketName(sandboxID)); err != nil && err != bbolt.ErrBucketNotFound {
				return fmt.Errorf("delete exec config bucket for %s: %w", sandboxID, err)
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

func (store *persistentEventStore) SaveSandboxConfig(sandboxID string, spec *agboxv1.CreateSpec) error {
	payload, err := proto.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal sandbox config: %w", err)
	}
	return store.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sandboxConfigBucket)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", sandboxConfigBucket)
		}
		return bucket.Put([]byte(sandboxID), payload)
	})
}

func (store *persistentEventStore) LoadSandboxConfig(sandboxID string) (*agboxv1.CreateSpec, error) {
	var spec *agboxv1.CreateSpec
	err := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sandboxConfigBucket)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", sandboxConfigBucket)
		}
		value := bucket.Get([]byte(sandboxID))
		if value == nil {
			return nil
		}
		spec = &agboxv1.CreateSpec{}
		if err := proto.Unmarshal(value, spec); err != nil {
			return fmt.Errorf("unmarshal sandbox config for %s: %w", sandboxID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return spec, nil
}

func (store *persistentEventStore) LoadAllSandboxConfigs() (map[string]*agboxv1.CreateSpec, error) {
	result := make(map[string]*agboxv1.CreateSpec)
	err := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sandboxConfigBucket)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(key, value []byte) error {
			spec := &agboxv1.CreateSpec{}
			if err := proto.Unmarshal(value, spec); err != nil {
				return fmt.Errorf("unmarshal sandbox config for %s: %w", string(key), err)
			}
			result[string(key)] = spec
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (store *persistentEventStore) DeleteSandboxConfig(sandboxID string) error {
	return store.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sandboxConfigBucket)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", sandboxConfigBucket)
		}
		return bucket.Delete([]byte(sandboxID))
	})
}

func (store *persistentEventStore) SaveExecConfig(sandboxID string, req *agboxv1.CreateExecRequest) error {
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal exec config: %w", err)
	}
	return store.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(execConfigBucketName(sandboxID))
		if err != nil {
			return fmt.Errorf("create exec config bucket for %s: %w", sandboxID, err)
		}
		return bucket.Put([]byte(req.GetExecId()), payload)
	})
}

func (store *persistentEventStore) LoadExecConfigs(sandboxID string) ([]*agboxv1.CreateExecRequest, error) {
	var configs []*agboxv1.CreateExecRequest
	err := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(execConfigBucketName(sandboxID))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			req := &agboxv1.CreateExecRequest{}
			if err := proto.Unmarshal(value, req); err != nil {
				return fmt.Errorf("unmarshal exec config for %s: %w", sandboxID, err)
			}
			configs = append(configs, req)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return configs, nil
}

func eventsBucketName(sandboxID string) []byte {
	return []byte(eventsBucketPrefix + sandboxID)
}

func isEventsBucketName(bucketName string) bool {
	return len(bucketName) > len(eventsBucketPrefix) && bucketName[:len(eventsBucketPrefix)] == eventsBucketPrefix
}

func execConfigBucketName(sandboxID string) []byte {
	return []byte(execConfigBucketPrefix + sandboxID)
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
