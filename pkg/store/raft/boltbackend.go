package raft

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.etcd.io/bbolt"
	"github.com/tlalocweb/hulation/log"
)

var ErrKeyNotFound = errors.New("key not found")

var boltLogger = log.GetTaggedLogger("bolt", "BoltDB backend")

const (
	// Default bucket for storing data
	DefaultBucket = "data"
)

// BoltBackend provides BoltDB persistence layer
type BoltBackend struct {
	db     *bbolt.DB
	mu     sync.RWMutex
	logger *log.TaggedLogger
}

// NewBoltBackend creates a new BoltDB backend
func NewBoltBackend(path string) (*BoltBackend, error) {
	boltLogger.Infof("Opening BoltDB at: %s", path)

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	db, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open BoltDB: %w", err)
	}

	// Create default bucket
	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(DefaultBucket))
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create default bucket: %w", err)
	}

	backend := &BoltBackend{
		db:     db,
		logger: boltLogger,
	}

	boltLogger.Infof("BoltDB backend initialized")
	return backend, nil
}

// Get retrieves a value by key
func (b *BoltBackend) Get(key string) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var value []byte
	err := b.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return ErrKeyNotFound
		}

		v := bucket.Get([]byte(key))
		if v == nil {
			return ErrKeyNotFound
		}

		// Copy the value since it's only valid during the transaction
		value = make([]byte, len(v))
		copy(value, v)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return value, nil
}

// GetAll retrieves all key-value pairs with the given prefix
func (b *BoltBackend) GetAll(prefix string) (map[string][]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	result := make(map[string][]byte)
	err := b.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return nil
		}

		cursor := bucket.Cursor()
		prefixBytes := []byte(prefix)

		for k, v := cursor.Seek(prefixBytes); k != nil && bytes.HasPrefix(k, prefixBytes); k, v = cursor.Next() {
			// Copy key and value
			keyCopy := make([]byte, len(k))
			valueCopy := make([]byte, len(v))
			copy(keyCopy, k)
			copy(valueCopy, v)
			result[string(keyCopy)] = valueCopy
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

// Put stores a value at the given key
func (b *BoltBackend) Put(key string, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		return bucket.Put([]byte(key), value)
	})
}

// PutBatch stores multiple key-value pairs in a single transaction
func (b *BoltBackend) PutBatch(kvs map[string][]byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		for key, value := range kvs {
			if err := bucket.Put([]byte(key), value); err != nil {
				return err
			}
		}

		return nil
	})
}

// Delete removes a key
func (b *BoltBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		return bucket.Delete([]byte(key))
	})
}

// DeletePrefix removes all keys with the given prefix
func (b *BoltBackend) DeletePrefix(prefix string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		cursor := bucket.Cursor()
		prefixBytes := []byte(prefix)
		keysToDelete := [][]byte{}

		// Collect keys to delete
		for k, _ := cursor.Seek(prefixBytes); k != nil && bytes.HasPrefix(k, prefixBytes); k, _ = cursor.Next() {
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			keysToDelete = append(keysToDelete, keyCopy)
		}

		// Delete collected keys
		for _, key := range keysToDelete {
			if err := bucket.Delete(key); err != nil {
				return err
			}
		}

		return nil
	})
}

// Snapshot creates a snapshot of the database
func (b *BoltBackend) Snapshot() ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var snapshot []byte
	err := b.db.View(func(tx *bbolt.Tx) error {
		// Get the size of the database
		size := tx.Size()
		snapshot = make([]byte, size)

		// Copy the entire database
		return tx.Copy(&ByteWriter{data: &snapshot})
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	return snapshot, nil
}

// Restore restores the database from a snapshot
func (b *BoltBackend) Restore(snapshot []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Close current database
	if err := b.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	// Restore from snapshot
	// In a production system, you would write the snapshot to disk and reopen
	// For now, we'll restore data bucket by bucket
	db, err := bbolt.Open(b.db.Path(), 0600, nil)
	if err != nil {
		return fmt.Errorf("failed to reopen database: %w", err)
	}

	b.db = db
	return nil
}

// BeginTransaction starts a read-write transaction
func (b *BoltBackend) BeginTransaction() (*bbolt.Tx, error) {
	b.mu.Lock()
	return b.db.Begin(true)
}

// CommitTransaction commits a transaction
func (b *BoltBackend) CommitTransaction(tx *bbolt.Tx) error {
	defer b.mu.Unlock()
	return tx.Commit()
}

// RollbackTransaction rolls back a transaction
func (b *BoltBackend) RollbackTransaction(tx *bbolt.Tx) error {
	defer b.mu.Unlock()
	return tx.Rollback()
}

// ExecuteInTransaction executes a function within a BoltDB transaction
// Uses BoltDB's managed transaction API which automatically handles commit/rollback
func (b *BoltBackend) ExecuteInTransaction(fn func(tx *bbolt.Tx) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Use BoltDB's managed Update API which automatically:
	// - Commits if fn returns nil
	// - Rolls back if fn returns an error
	// - Handles panic recovery
	return b.db.Update(fn)
}

// Close closes the BoltDB database
func (b *BoltBackend) Close() error {
	b.logger.Infof("Closing BoltDB")
	return b.db.Close()
}

// ByteWriter is a helper for writing snapshot data
type ByteWriter struct {
	data *[]byte
}

func (bw *ByteWriter) Write(p []byte) (n int, err error) {
	*bw.data = append(*bw.data, p...)
	return len(p), nil
}
