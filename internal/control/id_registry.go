package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	bbolt "go.etcd.io/bbolt"
)

var (
	errSandboxIDAlreadyExists = errors.New("sandbox id already exists")
	errExecIDAlreadyExists    = errors.New("exec id already exists")
)

var (
	sandboxIDBucket = []byte("sandbox-ids")
	execIDBucket    = []byte("exec-ids")
)

type idRegistry interface {
	io.Closer
	ReserveSandboxID(string, time.Time) error
	ReserveExecID(string, time.Time) error
	ReleaseSandboxID(string) error
	ReleaseExecID(string) error
}

type memoryIDRegistry struct {
	mu         sync.Mutex
	sandboxIDs map[string]int64
	execIDs    map[string]int64
}

func newMemoryIDRegistry() *memoryIDRegistry {
	return &memoryIDRegistry{
		sandboxIDs: make(map[string]int64),
		execIDs:    make(map[string]int64),
	}
}

func (*memoryIDRegistry) Close() error { return nil }

func (registry *memoryIDRegistry) ReserveSandboxID(id string, createdAt time.Time) error {
	return registry.reserve(registry.sandboxIDs, id, createdAt, errSandboxIDAlreadyExists)
}

func (registry *memoryIDRegistry) ReserveExecID(id string, createdAt time.Time) error {
	return registry.reserve(registry.execIDs, id, createdAt, errExecIDAlreadyExists)
}

func (registry *memoryIDRegistry) ReleaseSandboxID(id string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.sandboxIDs, id)
	return nil
}

func (registry *memoryIDRegistry) ReleaseExecID(id string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.execIDs, id)
	return nil
}

func (registry *memoryIDRegistry) reserve(ids map[string]int64, id string, createdAt time.Time, duplicateErr error) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := ids[id]; exists {
		return duplicateErr
	}
	ids[id] = createdAt.UTC().UnixNano()
	return nil
}

type persistentIDRegistry struct {
	db      *bbolt.DB
	closeDB bool
}

type combinedServiceCloser struct {
	runtime  io.Closer
	registry io.Closer
}

func (closer combinedServiceCloser) Close() error {
	return errors.Join(
		closeCloser(closer.runtime),
		closeCloser(closer.registry),
	)
}

func closeCloser(closer io.Closer) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func joinServiceClosers(runtimeCloser io.Closer, registryCloser io.Closer) io.Closer {
	if runtimeCloser == nil {
		return registryCloser
	}
	if registryCloser == nil {
		return runtimeCloser
	}
	return combinedServiceCloser{
		runtime:  runtimeCloser,
		registry: registryCloser,
	}
}

func openBoltDB(path string) (*bbolt.DB, error) {
	if path == "" {
		return nil, errors.New("id store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create id store directory: %w", err)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 250 * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("open bbolt database: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(sandboxIDBucket); err != nil {
			return fmt.Errorf("create bucket %q: %w", sandboxIDBucket, err)
		}
		if _, err := tx.CreateBucketIfNotExists(execIDBucket); err != nil {
			return fmt.Errorf("create bucket %q: %w", execIDBucket, err)
		}
		if _, err := tx.CreateBucketIfNotExists(eventMetaBucket); err != nil {
			return fmt.Errorf("create bucket %q: %w", eventMetaBucket, err)
		}
		if _, err := tx.CreateBucketIfNotExists(sandboxConfigBucket); err != nil {
			return fmt.Errorf("create bucket %q: %w", sandboxConfigBucket, err)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func newPersistentIDRegistry(db *bbolt.DB) *persistentIDRegistry {
	return &persistentIDRegistry{db: db}
}

func openPersistentIDRegistry(path string) (*persistentIDRegistry, error) {
	db, err := openBoltDB(path)
	if err != nil {
		return nil, err
	}
	return &persistentIDRegistry{db: db, closeDB: true}, nil
}

func (registry *persistentIDRegistry) Close() error {
	if !registry.closeDB {
		return nil
	}
	return registry.db.Close()
}

func (registry *persistentIDRegistry) ReserveSandboxID(id string, createdAt time.Time) error {
	return registry.reserve(sandboxIDBucket, id, createdAt, errSandboxIDAlreadyExists)
}

func (registry *persistentIDRegistry) ReserveExecID(id string, createdAt time.Time) error {
	return registry.reserve(execIDBucket, id, createdAt, errExecIDAlreadyExists)
}

func (registry *persistentIDRegistry) ReleaseSandboxID(id string) error {
	return registry.release(sandboxIDBucket, id)
}

func (registry *persistentIDRegistry) ReleaseExecID(id string) error {
	return registry.release(execIDBucket, id)
}

func (registry *persistentIDRegistry) reserve(bucketName []byte, id string, createdAt time.Time, duplicateErr error) error {
	return registry.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketName)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", bucketName)
		}
		key := []byte(id)
		if bucket.Get(key) != nil {
			return duplicateErr
		}
		return bucket.Put(key, encodeInt64(createdAt.UTC().UnixNano()))
	})
}

func (registry *persistentIDRegistry) release(bucketName []byte, id string) error {
	return registry.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketName)
		if bucket == nil {
			return fmt.Errorf("bucket %q is missing", bucketName)
		}
		return bucket.Delete([]byte(id))
	})
}

// NewServiceWithPersistentIDStore builds a service backed by persistent ID and event stores.
func NewServiceWithPersistentIDStore(ctx context.Context, config ServiceConfig, path string) (*Service, io.Closer, error) {
	db, err := openBoltDB(path)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize id store %s: %w", path, err)
	}
	config.idRegistry = newPersistentIDRegistry(db)
	config.eventStore = newPersistentEventStore(db)
	service, runtimeCloser, err := NewService(config)
	if err != nil {
		return nil, nil, errors.Join(err, db.Close())
	}
	restoreCtx := ctx
	if restoreCtx == nil {
		restoreCtx = context.Background()
	}
	if err := service.restorePersistedSandboxes(restoreCtx); err != nil {
		return nil, nil, errors.Join(err, closeCloser(runtimeCloser), db.Close())
	}
	if err := service.cleanupExpiredEvents(); err != nil {
		return nil, nil, errors.Join(err, closeCloser(runtimeCloser), db.Close())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	watcher := newDockerEventWatcher(service, config.Logger)
	go watcher.run(ctx)
	go service.cleanupLoop(ctx)
	return service, joinServiceClosers(runtimeCloser, db), nil
}
