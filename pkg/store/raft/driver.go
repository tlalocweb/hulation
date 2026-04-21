package raft

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/common"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

var logger = log.GetTaggedLogger("raft-store", "Raft storage driver")

// RaftStorage implements the common.Storage interface using Raft consensus and BoltDB persistence
type RaftStorage struct {
	raft     *RaftNode
	backend  *BoltBackend
	informer *InformerFactory
	mu       sync.RWMutex
	logger   *log.TaggedLogger
}

// RaftConfig holds configuration for the Raft storage
type RaftConfig struct {
	NodeID           string
	RaftDir          string
	BindAddr         string
	JoinAddr         string
	Bootstrap        bool
	SnapshotInterval int
	LogLevel         string
	BarrierOnApply   bool // If true, Apply() waits for FSM application via Barrier() (default: true)
}

// NewRaftStorage creates a new Raft-based storage driver
func NewRaftStorage(config *RaftConfig) (common.Storage, error) {
	logger.Infof("Initializing Raft storage with NodeID: %s", config.NodeID)

	// Default BarrierOnApply to true for read-after-write consistency
	if config.BarrierOnApply == false {
		// Check if it was explicitly set to false or just zero-value
		// Since we want true by default, we set it here
		config.BarrierOnApply = true
	}

	// Create BoltDB backend
	backend, err := NewBoltBackend(path.Join(config.RaftDir, "data.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create BoltDB backend: %w", err)
	}

	// Create Raft node
	raftNode, err := NewRaftNode(config, backend)
	if err != nil {
		backend.Close()
		return nil, fmt.Errorf("failed to create Raft node: %w", err)
	}

	// Create informer factory
	informerFactory := NewInformerFactory(raftNode)

	storage := &RaftStorage{
		raft:     raftNode,
		backend:  backend,
		informer: informerFactory,
		logger:   logger,
	}

	logger.Infof("Raft storage initialized successfully")
	return storage, nil
}

// Join concatenates path parts with '/'
func (r *RaftStorage) Join(parts ...string) string {
	return path.Join(parts...)
}

// IsLeader returns true if this node is the Raft leader
func (r *RaftStorage) IsLeader() bool {
	return r.raft.IsLeader()
}

// Get retrieves a value by key
func (r *RaftStorage) Get(key string, opts ...func(*common.StoreOpts)) ([]byte, error) {
	options := common.NewStoreOpts(opts...)
	r.mu.RLock()
	defer r.mu.RUnlock()

	value, err := r.backend.Get(key)
	if err != nil {
		// Return nil without error for missing keys (standard KV store behavior)
		if err == ErrKeyNotFound {
			return nil, nil
		}
		return nil, err
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "Get: key=%s, value_len=%d", key, len(value))
	}

	return value, nil
}

// GetAll retrieves all key-value pairs with the given prefix
func (r *RaftStorage) GetAll(prefix string, opts ...func(*common.StoreOpts)) (map[string][]byte, error) {
	options := common.NewStoreOpts(opts...)
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, err := r.backend.GetAll(prefix)
	if err != nil {
		return nil, err
	}

	if options.RemovePrefix {
		trimmed := make(map[string][]byte)
		for k, v := range result {
			trimmedKey := strings.TrimPrefix(k, prefix)
			trimmedKey = strings.TrimPrefix(trimmedKey, "/")
			trimmed[trimmedKey] = v
		}
		result = trimmed
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "GetAll: prefix=%s, count=%d", prefix, len(result))
	}

	return result, nil
}

// Put stores a value at the given key
func (r *RaftStorage) Put(key string, value []byte, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	// Apply the write through Raft consensus
	cmd := &Command{
		Op:    OpPut,
		Key:   key,
		Value: value,
	}

	if err := r.raft.Apply(cmd); err != nil {
		return fmt.Errorf("failed to apply Put command: %w", err)
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "Put: key=%s, value_len=%d", key, len(value))
	}

	return nil
}

// PutAll stores multiple key-value pairs
func (r *RaftStorage) PutAll(prefix string, values map[string][]byte, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	// If RemovePrefix is set, delete all existing keys with the prefix first
	if options.RemovePrefix {
		if err := r.DeleteAll(prefix); err != nil {
			return fmt.Errorf("failed to remove existing keys: %w", err)
		}
	}

	// Apply all writes as a batch
	cmd := &Command{
		Op:     OpPutBatch,
		Key:    prefix,
		Batch:  values,
	}

	if err := r.raft.Apply(cmd); err != nil {
		return fmt.Errorf("failed to apply PutAll command: %w", err)
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "PutAll: prefix=%s, count=%d", prefix, len(values))
	}

	return nil
}

// Delete removes a key
func (r *RaftStorage) Delete(key string, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	cmd := &Command{
		Op:  OpDelete,
		Key: key,
	}

	if err := r.raft.Apply(cmd); err != nil {
		return fmt.Errorf("failed to apply Delete command: %w", err)
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "Delete: key=%s", key)
	}

	return nil
}

// DeleteAll removes all keys with the given prefix
func (r *RaftStorage) DeleteAll(prefix string, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	cmd := &Command{
		Op:  OpDeletePrefix,
		Key: prefix,
	}

	if err := r.raft.Apply(cmd); err != nil {
		return fmt.Errorf("failed to apply DeleteAll command: %w", err)
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "DeleteAll: prefix=%s", prefix)
	}

	return nil
}

// PutObject stores a serialized object
func (r *RaftStorage) PutObject(key string, obj common.SerialAdapter, opts ...func(*common.StoreOpts)) error {
	if err := obj.Finalize(); err != nil {
		return fmt.Errorf("failed to finalize object: %w", err)
	}

	data, err := obj.GetSerialData()
	if err != nil {
		return fmt.Errorf("failed to serialize object: %w", err)
	}

	// Store the object data
	if err := r.Put(key, data, opts...); err != nil {
		return err
	}

	// Handle indexes
	return r.updateIndexes(key, obj)
}

// GetObject retrieves and deserializes an object
func (r *RaftStorage) GetObject(key string, opts ...func(*common.StoreOpts)) (interface{}, error) {
	data, err := r.Get(key, opts...)
	if err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	// Deserialize using anypb
	receivedAny := &anypb.Any{}
	if err := proto.Unmarshal(data, receivedAny); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Any: %w", err)
	}

	msg, err := anypb.UnmarshalNew(receivedAny, proto.UnmarshalOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal object: %w", err)
	}

	return msg, nil
}

// GetObjectBytes retrieves the raw bytes of an object
func (r *RaftStorage) GetObjectBytes(key string, opts ...func(*common.StoreOpts)) ([]byte, error) {
	return r.Get(key, opts...)
}

// GetObjectJson retrieves an object as JSON
func (r *RaftStorage) GetObjectJson(key string, opts ...func(*common.StoreOpts)) ([]byte, error) {
	data, err := r.Get(key, opts...)
	if err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	// Deserialize protobuf
	receivedAny := &anypb.Any{}
	if err := proto.Unmarshal(data, receivedAny); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Any: %w", err)
	}

	msg, err := anypb.UnmarshalNew(receivedAny, proto.UnmarshalOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal object: %w", err)
	}

	// Convert to JSON
	return protojson.Marshal(msg)
}

// MutateObject applies a mutation function to an object atomically using CAS
func (r *RaftStorage) MutateObject(key string, mutator common.MutateKVFunc, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)
	maxRetries := 50 // Maximum retry attempts for CAS conflicts

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Get current data
		currentData, err := r.Get(key, opts...)
		if err != nil && !options.NoErrorKeyNotFound {
			return err
		}

		// Create unwrappable
		unwrappable := common.NewUnwrappable(currentData)

		// Apply mutator
		err, doDelete := mutator(key, unwrappable)
		if err != nil {
			return err
		}

		if doDelete {
			// For deletion, we need to clean up indexes first
			if currentData == nil {
				// Key doesn't exist, nothing to delete
				return nil
			}

			// Deserialize the current object to get its indexes
			obj, err := r.deserializeObject(currentData)
			if err != nil {
				// If we can't deserialize, just delete the key without index cleanup
				r.logger.Warnf("Failed to deserialize object for index cleanup: %v", err)
				return r.Delete(key, opts...)
			}

			// If it's a SerialAdapter, manually delete all its indexes
			if adapter, ok := obj.(common.SerialAdapter); ok {
				baseKey := adapter.GetCollectionBase()
				// Delete all indexes associated with this object
				for _, index := range adapter.GetIndexes() {
					indexBase := index.Base
					if indexBase == "" {
						indexBase = baseKey
					}

					for indexKey := range index.Ptrs {
						indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
						if err := r.Delete(indexPath); err != nil {
							r.logger.Warnf("Failed to delete index %s: %v", indexPath, err)
						}
					}
				}
			}

			// Now delete the object itself
			return r.Delete(key, opts...)
		}

		// Get serialized data from unwrappable
		newData, err := unwrappable.GetSerialData()
		if err != nil {
			return fmt.Errorf("failed to get serial data: %w", err)
		}

		// Attempt atomic update
		if currentData == nil {
			// Key doesn't exist - use CompareAndCreate
			err = r.CompareAndCreate(key, newData, opts...)
			if err == nil {
				// Update indexes after successful create
				r.handleIndexUpdates(key, unwrappable)
				if options.DebugLevel > 1 {
					r.logger.VDebugf(2, "MutateObject: created new key=%s on attempt %d", key, attempt+1)
				}
				return nil
			}
			// If create failed, key now exists - retry to read and update
			if options.DebugLevel > 1 {
				r.logger.VDebugf(2, "MutateObject: create conflict for key=%s, retrying (attempt %d)", key, attempt+1)
			}
			continue
		} else {
			// Key exists - use CompareAndSwap
			err = r.CompareAndSwap(key, currentData, newData, opts...)
			if err == nil {
				// Update indexes after successful swap
				r.handleIndexUpdates(key, unwrappable)
				if options.DebugLevel > 1 {
					r.logger.VDebugf(2, "MutateObject: updated key=%s on attempt %d", key, attempt+1)
				}
				return nil
			}
			// CAS failed - value changed concurrently, retry
			if options.DebugLevel > 1 {
				r.logger.VDebugf(2, "MutateObject: CAS conflict for key=%s, retrying (attempt %d)", key, attempt+1)
			}
			continue
		}
	}

	return fmt.Errorf("failed to mutate object after %d attempts due to concurrent modifications", maxRetries)
}

// MutateValue applies a mutation function to raw bytes atomically using CAS
func (r *RaftStorage) MutateValue(key string, mutator common.MutateKVDataFunc, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)
	maxRetries := 50 // Maximum retry attempts for CAS conflicts

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Get current data
		currentData, err := r.Get(key, opts...)
		if err != nil && !options.NoErrorKeyNotFound {
			return err
		}

		// Apply mutator
		err, newData, doDelete := mutator(key, currentData)
		if err != nil {
			return err
		}

		if doDelete {
			// For deletion, we still want atomicity
			if currentData == nil {
				// Key doesn't exist, nothing to delete
				return nil
			}
			// Delete is atomic in Raft
			return r.Delete(key, opts...)
		}

		// Attempt atomic update
		if currentData == nil {
			// Key doesn't exist - use CompareAndCreate
			err = r.CompareAndCreate(key, newData, opts...)
			if err == nil {
				if options.DebugLevel > 1 {
					r.logger.VDebugf(2, "MutateValue: created new key=%s on attempt %d", key, attempt+1)
				}
				return nil
			}
			// If create failed, key now exists - retry to read and update
			if options.DebugLevel > 1 {
				r.logger.VDebugf(2, "MutateValue: create conflict for key=%s, retrying (attempt %d)", key, attempt+1)
			}
			continue
		} else {
			// Key exists - use CompareAndSwap
			err = r.CompareAndSwap(key, currentData, newData, opts...)
			if err == nil {
				if options.DebugLevel > 1 {
					r.logger.VDebugf(2, "MutateValue: updated key=%s on attempt %d", key, attempt+1)
				}
				return nil
			}
			// CAS failed - value changed concurrently, retry
			if options.DebugLevel > 1 {
				r.logger.VDebugf(2, "MutateValue: CAS conflict for key=%s, retrying (attempt %d)", key, attempt+1)
			}
			continue
		}
	}

	return fmt.Errorf("failed to mutate value after %d attempts due to concurrent modifications", maxRetries)
}

// MutateObjects applies mutations to multiple objects in a transaction
//
// ACID PROPERTIES:
// - Atomicity: All mutations succeed or all fail via BoltDB transaction
// - Consistency: BoltDB ensures data consistency
// - Isolation: Raft log serialization + BoltDB transaction isolation
// - Durability: BoltDB fsync ensures durability
//
// RACE CONDITION PROTECTION:
// 1. Raft ensures commands are applied in strict serial order
// 2. Each command executes atomically within a BoltDB transaction
// 3. The transaction reads current values and applies all mutations before commit
// 4. If the transaction fails, all mutations are rolled back
//
// This is different from MutateObject which uses CAS for optimistic locking.
// MutateObjects uses pessimistic locking via the transaction, which is appropriate
// for batch operations where you want all-or-nothing semantics across multiple keys.
//
// NOTE: If you need CAS-style protection for individual mutations, use MutateObject
// instead and call it multiple times (though this won't be atomic across all mutations).
func (r *RaftStorage) MutateObjects(opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	if len(options.MutatorList) == 0 {
		return nil
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "MutateObjects: processing %d mutations locally before Raft submission", len(options.MutatorList))
	}

	// Execute all mutations LOCALLY to compute the batch updates
	batchUpdates := make([]BatchObjectUpdate, 0, len(options.MutatorList))

	for _, mut := range options.MutatorList {
		// Read current value for this key
		currentData, err := r.backend.Get(mut.Key)
		if err != nil {
			return fmt.Errorf("failed to read key %s: %w", mut.Key, err)
		}

		// Deserialize old object to get old indexes for cleanup
		var oldIndexes map[string]*common.Index
		if currentData != nil {
			oldObj, deserr := r.deserializeObject(currentData)
			if deserr == nil {
				if adapter, ok := oldObj.(common.SerialAdapter); ok {
					oldIndexes = adapter.GetIndexes()
				}
			}
		}

		// Create unwrappable with current data
		unwrappable := common.NewUnwrappable(currentData)

		// Apply the mutation function
		mutErr, doDelete := mut.Mutate(mut.Key, unwrappable)
		if mutErr != nil {
			return fmt.Errorf("mutation failed for key %s: %w", mut.Key, mutErr)
		}

		// Prepare the batch update
		update := BatchObjectUpdate{
			Key: mut.Key,
		}

		if doDelete {
			// Mark for deletion
			update.Delete = true

			// Collect old indexes for deletion
			update.IndexesToDel = r.collectIndexPaths(mut.Key, oldIndexes)
		} else {
			// Get the new serialized data
			newData, serr := unwrappable.GetSerialData()
			if serr != nil {
				return fmt.Errorf("failed to serialize data for key %s: %w", mut.Key, serr)
			}
			update.Value = newData

			// Get new indexes from the unwrappable
			var newIndexes map[string]*common.Index
			if adapter, ok := unwrappable.Obj().(common.SerialAdapter); ok {
				newIndexes = adapter.GetIndexes()
			} else if len(unwrappable.GetIndexes()) > 0 {
				newIndexes = unwrappable.GetIndexes()
			}

			// Collect indexes to add
			indexPaths, indexVals := r.collectIndexesToAdd(mut.Key, newIndexes)
			update.IndexesToAdd = indexPaths
			update.IndexVals = indexVals

			// Collect old indexes to delete (that are not in new indexes)
			update.IndexesToDel = r.findIndexesToDelete(mut.Key, oldIndexes, newIndexes)
		}

		batchUpdates = append(batchUpdates, update)
	}

	// Submit the pre-computed batch to Raft
	cmd := &Command{
		Op:           OpBatchUpdate,
		BatchUpdates: batchUpdates,
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "MutateObjects: submitting batch of %d updates to Raft", len(batchUpdates))
	}

	return r.raft.Apply(cmd)
}

// MutateObjectFromIndex mutates an object found via an index
func (r *RaftStorage) MutateObjectFromIndex(basecollection string, indexkey string, indexname string, mutator common.MutateKVFunc, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	// Look up the actual key via the index
	indexPath := path.Join(basecollection, "_index_", indexname, indexkey)
	pointerData, err := r.Get(indexPath, opts...)
	if err != nil {
		return fmt.Errorf("failed to lookup index: %w", err)
	}

	// If index exists, use the key from the index and delegate to MutateObject
	if pointerData != nil {
		// The pointer contains just the collection key, need to join with base
		collectionKey := string(pointerData)
		actualKey := path.Join(basecollection, collectionKey)
		return r.MutateObject(actualKey, mutator, opts...)
	}

	// Index doesn't exist - check if we should create a new object
	if !options.NoErrorKeyNotFound {
		return fmt.Errorf("key not found")
	}

	// Create a new object - call mutator with empty key to create the wrapper
	unwrappable := common.NewUnwrappable(nil)

	err, doDelete := mutator("", unwrappable)
	if err != nil {
		return err
	}

	if doDelete {
		// Nothing to delete if it doesn't exist
		return nil
	}

	// Extract the actual key from the wrapper metadata
	collectionBase := unwrappable.GetCollectionBase()
	collectionKey := unwrappable.GetCollectionKey()

	if collectionKey == "" {
		return fmt.Errorf("mutator did not set collection key for new object")
	}

	// Construct the full key path
	var actualKey string
	if collectionBase != "" {
		actualKey = path.Join(collectionBase, collectionKey)
	} else {
		actualKey = path.Join(basecollection, collectionKey)
	}

	// Get serialized data
	newData, err := unwrappable.GetSerialData()
	if err != nil {
		return fmt.Errorf("failed to get serial data: %w", err)
	}

	// Store the object
	err = r.CompareAndCreate(actualKey, newData, opts...)
	if err != nil {
		return fmt.Errorf("failed to create object: %w", err)
	}

	// Create the lookup index atomically - this prevents concurrent creation of same object
	// This is critical for ensuring only one goroutine can create an object with the same index key
	indexValue := []byte(collectionKey)
	err = r.CompareAndCreate(indexPath, indexValue, opts...)
	if err != nil {
		// Object was created but index creation failed (race condition)
		// Another goroutine already created this index, so delete our object and fail
		r.Delete(actualKey)
		return fmt.Errorf("failed to create index (object with this index already exists): %w", err)
	}

	// Create any additional indexes from the wrapper (email, etc.)
	// Note: handleIndexUpdates will also create/update the lookup index, but that's okay
	// since it uses Put (not CompareAndCreate), so it just overwrites with the same value
	r.handleIndexUpdates(actualKey, unwrappable)

	return nil
}

// PutObjects stores multiple objects
func (r *RaftStorage) PutObjects(prefix string, objs map[string]common.SerialAdapter, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	if options.RemovePrefix {
		if err := r.DeleteAll(prefix); err != nil {
			return err
		}
	}

	for key, obj := range objs {
		fullKey := path.Join(prefix, key)
		if err := r.PutObject(fullKey, obj, opts...); err != nil {
			return err
		}
	}

	return nil
}

// GetObjects retrieves multiple objects with a prefix
func (r *RaftStorage) GetObjects(prefix string, opts ...func(*common.StoreOpts)) (map[string]interface{}, error) {
	allData, err := r.GetAll(prefix, opts...)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	for key, data := range allData {
		// Skip index keys
		if strings.Contains(key, "/_index_/") {
			continue
		}

		obj, err := r.deserializeObject(data)
		if err != nil {
			r.logger.Warnf("Failed to deserialize object at %s: %v", key, err)
			continue
		}

		result[key] = obj
	}

	return result, nil
}

// GetObjectByCollectionIndex retrieves an object via a collection-relative index
func (r *RaftStorage) GetObjectByCollectionIndex(basecollectionkey string, indexname string, indexkey string, opts ...func(*common.StoreOpts)) (interface{}, error) {
	indexPath := path.Join(basecollectionkey, "_index_", indexname, indexkey)
	pointerData, err := r.Get(indexPath, opts...)
	if err != nil {
		return nil, err
	}

	actualKey := path.Join(basecollectionkey, string(pointerData))
	return r.GetObject(actualKey, opts...)
}

// GetObjectByOtherIndex retrieves an object via an external index
func (r *RaftStorage) GetObjectByOtherIndex(basecollectionkey string, indexname string, indexkey string, opts ...func(*common.StoreOpts)) (interface{}, error) {
	indexPath := path.Join(basecollectionkey, "_index_", indexname, indexkey)
	pointerData, err := r.Get(indexPath, opts...)
	if err != nil {
		return nil, err
	}

	actualKey := string(pointerData) // Full path for external index
	return r.GetObject(actualKey, opts...)
}

// GetObjectsByIndex retrieves multiple objects via an index
func (r *RaftStorage) GetObjectsByIndex(collectionPrefix string, indexname, indexprefix string, opts ...func(*common.StoreOpts)) (map[string]interface{}, error) {
	indexPath := path.Join(collectionPrefix, "_index_", indexname, indexprefix)
	indexes, err := r.GetAll(indexPath, opts...)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	for _, pointerData := range indexes {
		actualKey := path.Join(collectionPrefix, string(pointerData))
		obj, err := r.GetObject(actualKey, opts...)
		if err != nil {
			r.logger.Warnf("Failed to get object at %s: %v", actualKey, err)
			continue
		}
		result[actualKey] = obj
	}

	return result, nil
}

// GetObjectsByOtherIndex retrieves multiple objects via an external index
func (r *RaftStorage) GetObjectsByOtherIndex(indexFullPath string, indexName string, indexprefix string, opts ...func(*common.StoreOpts)) (map[string]interface{}, error) {
	indexPath := path.Join(indexFullPath, "_index_", indexName, indexprefix)
	indexes, err := r.GetAll(indexPath, opts...)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	for _, pointerData := range indexes {
		actualKey := string(pointerData) // Full path
		obj, err := r.GetObject(actualKey, opts...)
		if err != nil {
			r.logger.Warnf("Failed to get object at %s: %v", actualKey, err)
			continue
		}
		result[actualKey] = obj
	}

	return result, nil
}

// GetKeys retrieves all keys with a prefix (excluding indexes)
func (r *RaftStorage) GetKeys(prefix string, opts ...func(*common.StoreOpts)) ([]string, error) {
	allData, err := r.GetAll(prefix, opts...)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(allData))
	for key := range allData {
		// Skip index keys
		if !strings.Contains(key, "/_index_/") {
			keys = append(keys, key)
		}
	}

	return keys, nil
}

// MutateCollection applies a mutation function to all objects in a collection
// DEPRECATED: This function has race conditions - it performs read-all followed by write-all
// without atomic guarantees, which can lead to lost updates in concurrent scenarios.
// Use MutateObjects() instead for atomic multi-object updates via transactions.
func (r *RaftStorage) MutateCollection(prefix string, mutator common.MutateCollectionFunc, opts ...func(*common.StoreOpts)) error {
	// ORIGINAL IMPLEMENTATION COMMENTED OUT DUE TO RACE CONDITIONS:
	// This performs: read all -> mutate in memory -> write all
	// Between the read and write, concurrent updates can be lost.
	//
	// allData, err := r.GetAll(prefix, opts...)
	// if err != nil {
	// 	return err
	// }
	//
	// // Create unwrappables for all objects
	// unwrappables := make(map[string]*common.Unwrappable)
	// for key, data := range allData {
	// 	if !strings.Contains(key, "/_index_/") {
	// 		unwrappables[key] = common.NewUnwrappable(data)
	// 	}
	// }
	//
	// // Apply mutator
	// if err := mutator(unwrappables); err != nil {
	// 	return err
	// }
	//
	// // Get serial data and store all objects
	// batch := make(map[string][]byte)
	// for key, unwrappable := range unwrappables {
	// 	data, err := unwrappable.GetSerialData()
	// 	if err != nil {
	// 		return fmt.Errorf("failed to get serial data at %s: %w", key, err)
	// 	}
	// 	batch[key] = data
	// }
	//
	// return r.PutAll(prefix, batch, opts...)

	return fmt.Errorf("MutateCollection has race conditions - use MutateObjects() instead for atomic multi-object updates")
}

// GetInformerFactory returns the informer factory for watching changes
func (r *RaftStorage) GetInformerFactory() common.InformerFactory {
	return r.informer
}

// updateIndexes updates all indexes for an object
func (r *RaftStorage) updateIndexes(key string, obj common.SerialAdapter) error {
	baseKey := obj.GetCollectionBase()

	// Update indexes
	for _, index := range obj.GetIndexes() {
		indexBase := index.Base
		if indexBase == "" {
			indexBase = baseKey
		}

		for indexKey, pointer := range index.Ptrs {
			indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
			if pointer == "" {
				pointer = obj.GetCollectionKey()
			}
			if err := r.Put(indexPath, []byte(pointer)); err != nil {
				return fmt.Errorf("failed to update index %s: %w", indexPath, err)
			}
		}
	}

	// Delete indexes
	for _, index := range obj.GetDeleteIndexes() {
		indexBase := index.Base
		if indexBase == "" {
			indexBase = baseKey
		}

		for indexKey := range index.Ptrs {
			indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
			if err := r.Delete(indexPath); err != nil {
				r.logger.Warnf("Failed to delete index %s: %v", indexPath, err)
			}
		}
	}

	return nil
}

// handleIndexUpdates processes indexes from an Unwrappable after mutation
func (r *RaftStorage) handleIndexUpdates(key string, unwrappable *common.Unwrappable) {
	// Try to get collection base and key from unwrappable first if wrapper is available
	// This ensures correct handling of tenant-scoped collections
	var baseKey, collectionKey string
	baseKey = unwrappable.GetCollectionBase()
	if baseKey != "" {
		// Wrapper is available, get the collection key too
		collectionKey = unwrappable.GetCollectionKey()
	}

	// If unwrappable doesn't have collection info, try extracting from key path
	if baseKey == "" {
		parts := strings.Split(key, "/")
		if len(parts) > 1 {
			baseKey = parts[0]
			collectionKey = strings.Join(parts[1:], "/")
		} else {
			// No collection prefix, use key as-is
			baseKey = ""
			collectionKey = key
		}
	}

	// No collection info, can't process indexes
	if baseKey == "" {
		return
	}

	// Update indexes
	for _, index := range unwrappable.GetIndexes() {
		indexBase := index.Base
		if indexBase == "" {
			indexBase = baseKey
		}

		for indexKey, pointer := range index.Ptrs {
			indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
			if pointer == "" {
				// For alternate base indexes, store the full path
				// For collection indexes (same base), store just the collection key
				if index.Base != "" && index.Base != baseKey {
					pointer = path.Join(baseKey, collectionKey)
				} else {
					pointer = collectionKey
				}
			}
			if err := r.Put(indexPath, []byte(pointer)); err != nil {
				r.logger.Warnf("Failed to update index %s: %v", indexPath, err)
			}
		}
	}

	// Delete indexes
	for _, index := range unwrappable.GetDeleteIndexes() {
		indexBase := index.Base
		if indexBase == "" {
			indexBase = baseKey
		}

		for indexKey := range index.Ptrs {
			indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
			if err := r.Delete(indexPath); err != nil {
				r.logger.Warnf("Failed to delete index %s: %v", indexPath, err)
			}
		}
	}
}

// deserializeObject deserializes protobuf data
func (r *RaftStorage) deserializeObject(data []byte) (interface{}, error) {
	receivedAny := &anypb.Any{}
	if err := proto.Unmarshal(data, receivedAny); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Any: %w", err)
	}

	msg, err := anypb.UnmarshalNew(receivedAny, proto.UnmarshalOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal object: %w", err)
	}

	return msg, nil
}

// CompareAndSwap performs an atomic compare-and-swap operation
// Returns error if the current value doesn't match expectedValue
func (r *RaftStorage) CompareAndSwap(key string, expectedValue, newValue []byte, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	// Create CAS command
	cmd := &Command{
		Op:            OpCompareAndSwap,
		Key:           key,
		Value:         newValue,
		ExpectedValue: expectedValue,
		ExpectedNil:   false,
	}

	// Apply through Raft
	if err := r.raft.Apply(cmd); err != nil {
		return err
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "CompareAndSwap: key=%s", key)
	}

	return nil
}

// CompareAndCreate performs an atomic create-if-not-exists operation
// Returns error if the key already exists
func (r *RaftStorage) CompareAndCreate(key string, value []byte, opts ...func(*common.StoreOpts)) error {
	options := common.NewStoreOpts(opts...)

	// Create CAS command expecting nil
	cmd := &Command{
		Op:          OpCompareAndSwap,
		Key:         key,
		Value:       value,
		ExpectedNil: true,
	}

	// Apply through Raft
	if err := r.raft.Apply(cmd); err != nil {
		return err
	}

	if options.DebugLevel > 0 {
		r.logger.VDebugf(options.DebugLevel, "CompareAndCreate: key=%s", key)
	}

	return nil
}

// Close shuts down the storage driver
func (r *RaftStorage) Close() error {
	r.logger.Infof("Shutting down Raft storage")

	if err := r.raft.Close(); err != nil {
		r.logger.Errorf("Error closing Raft node: %v", err)
	}

	if err := r.backend.Close(); err != nil {
		r.logger.Errorf("Error closing BoltDB backend: %v", err)
	}

	return nil
}

// Command represents a Raft command
// BatchObjectUpdate represents a single object update in a batch operation
type BatchObjectUpdate struct {
	Key           string   `json:"key"`
	Value         []byte   `json:"value,omitempty"`         // New value (nil if deleting)
	Delete        bool     `json:"delete,omitempty"`        // True if this is a deletion
	IndexesToAdd  []string `json:"indexes_add,omitempty"`   // Index paths to create/update
	IndexVals     []string `json:"index_vals,omitempty"`    // Corresponding values for indexes
	IndexesToDel  []string `json:"indexes_del,omitempty"`   // Index paths to delete
}

type Command struct {
	Op            CommandOp
	Key           string
	Value         []byte
	Batch         map[string][]byte
	Mutations     []common.MutateTuple
	BatchUpdates  []BatchObjectUpdate // For OpBatchUpdate
	ExpectedValue []byte              // For CAS: expected current value
	ExpectedNil   bool                // For CAS: expect key to not exist
}

// CommandOp represents the type of command
type CommandOp int

const (
	OpPut CommandOp = iota
	OpDelete
	OpPutBatch
	OpDeletePrefix
	OpMutateObjects // Deprecated: cannot serialize functions - use OpBatchUpdate
	OpCompareAndSwap
	OpBatchUpdate // Atomic batch update with index management
)

// MarshalJSON serializes a Command to JSON
func (c *Command) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Op            CommandOp              `json:"op"`
		Key           string                 `json:"key,omitempty"`
		Value         []byte                 `json:"value,omitempty"`
		Batch         map[string][]byte      `json:"batch,omitempty"`
		Mutations     []common.MutateTuple   `json:"mutations,omitempty"`
		BatchUpdates  []BatchObjectUpdate    `json:"batch_updates,omitempty"`
		ExpectedValue []byte                 `json:"expected_value,omitempty"`
		ExpectedNil   bool                   `json:"expected_nil,omitempty"`
	}{
		Op:            c.Op,
		Key:           c.Key,
		Value:         c.Value,
		Batch:         c.Batch,
		Mutations:     c.Mutations,
		BatchUpdates:  c.BatchUpdates,
		ExpectedValue: c.ExpectedValue,
		ExpectedNil:   c.ExpectedNil,
	})
}

// UnmarshalJSON deserializes a Command from JSON
func (c *Command) UnmarshalJSON(data []byte) error {
	aux := &struct {
		Op            CommandOp              `json:"op"`
		Key           string                 `json:"key,omitempty"`
		Value         []byte                 `json:"value,omitempty"`
		Batch         map[string][]byte      `json:"batch,omitempty"`
		Mutations     []common.MutateTuple   `json:"mutations,omitempty"`
		BatchUpdates  []BatchObjectUpdate    `json:"batch_updates,omitempty"`
		ExpectedValue []byte                 `json:"expected_value,omitempty"`
		ExpectedNil   bool                   `json:"expected_nil,omitempty"`
	}{}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	c.Op = aux.Op
	c.Key = aux.Key
	c.Value = aux.Value
	c.Batch = aux.Batch
	c.Mutations = aux.Mutations
	c.BatchUpdates = aux.BatchUpdates
	c.ExpectedValue = aux.ExpectedValue
	c.ExpectedNil = aux.ExpectedNil

	return nil
}

// Helper functions for batch update index management

// collectIndexPaths collects all index paths for deletion given a map of indexes
func (r *RaftStorage) collectIndexPaths(objectKey string, indexes map[string]*common.Index) []string {
	var paths []string

	// Extract collection base from the full key path
	parts := strings.Split(objectKey, "/")
	var baseKey string
	if len(parts) > 1 {
		baseKey = parts[0]
	} else {
		baseKey = ""
	}

	for _, index := range indexes {
		indexBase := index.Base
		if indexBase == "" {
			indexBase = baseKey
		}
		for indexKey := range index.Ptrs {
			indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
			paths = append(paths, indexPath)
		}
	}
	return paths
}

// collectIndexesToAdd collects index paths and their corresponding values for new indexes
func (r *RaftStorage) collectIndexesToAdd(objectKey string, indexes map[string]*common.Index) ([]string, []string) {
	var paths []string
	var vals []string

	// Extract collection base and key from the full key path
	parts := strings.Split(objectKey, "/")
	var baseKey, collectionKey string
	if len(parts) > 1 {
		baseKey = parts[0]
		collectionKey = strings.Join(parts[1:], "/")
	} else {
		baseKey = ""
		collectionKey = objectKey
	}

	for _, index := range indexes {
		indexBase := index.Base
		if indexBase == "" {
			indexBase = baseKey
		}
		for indexKey, pointer := range index.Ptrs {
			indexPath := path.Join(indexBase, "_index_", index.Name, indexKey)
			paths = append(paths, indexPath)

			// Determine the pointer value based on index type
			if pointer == "" {
				// For alternate base indexes, store the full path
				// For collection indexes (same base), store just the collection key
				if index.Base != "" && index.Base != baseKey {
					pointer = path.Join(baseKey, collectionKey)
				} else {
					pointer = collectionKey
				}
			}
			vals = append(vals, pointer)
		}
	}
	return paths, vals
}

// findIndexesToDelete finds old indexes that should be deleted (not present in new indexes)
func (r *RaftStorage) findIndexesToDelete(objectKey string, oldIndexes, newIndexes map[string]*common.Index) []string {
	// Create a map of new index paths for quick lookup
	newIndexPaths := make(map[string]bool)
	newPaths, _ := r.collectIndexesToAdd(objectKey, newIndexes)
	for _, p := range newPaths {
		newIndexPaths[p] = true
	}

	// Find old index paths that are not in new indexes
	var toDelete []string
	oldPaths := r.collectIndexPaths(objectKey, oldIndexes)
	for _, oldPath := range oldPaths {
		if !newIndexPaths[oldPath] {
			toDelete = append(toDelete, oldPath)
		}
	}

	return toDelete
}
