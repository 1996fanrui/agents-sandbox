package control

import (
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
}

type memoryIDRegistry struct {
	mu         sync.Mutex
	sandboxIDs map[string]string
	execIDs    map[string]string
}

func newMemoryIDRegistry() *memoryIDRegistry {
	return &memoryIDRegistry{
		sandboxIDs: make(map[string]string),
		execIDs:    make(map[string]string),
	}
}

func (*memoryIDRegistry) Close() error { return nil }

func (registry *memoryIDRegistry) ReserveSandboxID(id string, createdAt time.Time) error {
	return registry.reserve(registry.sandboxIDs, id, createdAt, errSandboxIDAlreadyExists)
}

func (registry *memoryIDRegistry) ReserveExecID(id string, createdAt time.Time) error {
	return registry.reserve(registry.execIDs, id, createdAt, errExecIDAlreadyExists)
}

func (registry *memoryIDRegistry) reserve(ids map[string]string, id string, createdAt time.Time, duplicateErr error) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := ids[id]; exists {
		return duplicateErr
	}
	ids[id] = createdAt.UTC().Format(time.RFC3339Nano)
	return nil
}

type persistentIDRegistry struct {
	db *bbolt.DB
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

func openPersistentIDRegistry(path string) (*persistentIDRegistry, error) {
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
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &persistentIDRegistry{db: db}, nil
}

func (registry *persistentIDRegistry) Close() error {
	return registry.db.Close()
}

func (registry *persistentIDRegistry) ReserveSandboxID(id string, createdAt time.Time) error {
	return registry.reserve(sandboxIDBucket, id, createdAt, errSandboxIDAlreadyExists)
}

func (registry *persistentIDRegistry) ReserveExecID(id string, createdAt time.Time) error {
	return registry.reserve(execIDBucket, id, createdAt, errExecIDAlreadyExists)
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
		return bucket.Put(key, []byte(createdAt.UTC().Format(time.RFC3339Nano)))
	})
}

// NewServiceWithPersistentIDStore builds a service backed by a persistent ID registry.
func NewServiceWithPersistentIDStore(config ServiceConfig, path string) (*Service, io.Closer, error) {
	registry, err := openPersistentIDRegistry(path)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize id store %s: %w", path, err)
	}
	config.idRegistry = registry
	service, runtimeCloser, err := NewService(config)
	if err != nil {
		return nil, nil, errors.Join(err, registry.Close())
	}
	return service, joinServiceClosers(runtimeCloser, registry), nil
}
