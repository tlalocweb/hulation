package common

import (
	"fmt"
	"reflect"

	"github.com/tlalocweb/hulation/log"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// type StorageError struct {
// 	Err error
// }

// func (e *StorageError) Error() string {
// 	return e.Err.Error()
// }

// func NewStorageError(err error) *StorageError {
// 	return &StorageError{Err: err}
// }

type MutateCollectionFunc func(map[string]*Unwrappable) error

type MutateKVFunc func(string, *Unwrappable) (err error, dodelete bool)

type MutateKVDataFunc func(key string, currentdata []byte) (err error, outdata []byte, dodelete bool)

type Conditional struct {
	Key       string
	Val       string
	Operation string
}

type StoreOpts struct {
	DebugLevel         int
	IfCondition        *Conditional
	RemovePrefix       bool
	NoErrorKeyNotFound bool
	AlwaysSetVal       bool
	MutatorList        []MutateTuple
	NoTrailingSlash    bool
	// IndexActs          []*IndexAction
}

func OptionAlwaysSetVal() func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.AlwaysSetVal = true
	}
}

// this will create the key if it doesn't exist
// and not error if it is not found
func OptionOrCreate() func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.NoErrorKeyNotFound = true
	}
}

func OptionDebuglevel(level int) func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.DebugLevel = level
	}
}

func OptionIfCondition(cond *Conditional) func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.IfCondition = cond
	}
}

func OptionNoTrailingSlash() func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.NoTrailingSlash = true
	}
}

// // IndexAct adds an index action to the list of actions to be performed
// func MutateIndex(act ...*IndexAction) func(*StoreOpts) {
// 	return func(opts *StoreOpts) {
// 		opts.IndexActs = append(opts.IndexActs, act...)
// 	}
// }

// func OptionMutateColl(f MutateCollectionFunc) func(*StoreOpts) {
// 	return func(opts *StoreOpts) {
// 		opts.MutateCollFunc = f
// 	}
// }

// If doing a PutAll or PutObjects, this will first delete all existing keys with the prefix first
func OptionRemoveExisting() func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.RemovePrefix = true
	}
}

func NewStoreOpts(options ...func(*StoreOpts)) *StoreOpts {
	opts := &StoreOpts{}
	for _, opt := range options {
		opt(opts)
	}
	return opts
}

func Condition(key string, val string, operation string) *Conditional {
	return &Conditional{
		Key:       key,
		Val:       val,
		Operation: operation,
	}
}

func Mutate(key string, mutate MutateKVFunc) func(*StoreOpts) {
	return func(opts *StoreOpts) {
		opts.MutatorList = append(opts.MutatorList, MutateTuple{Key: key, Mutate: mutate})
	}
}

type MutateTuple struct {
	Key    string
	Mutate MutateKVFunc
}

type StorageError string

func (e StorageError) Error() string {
	return string(e)
}

const (
	ErrStorageNil = StorageError("storage not setup")
)

// Storage driver interface
type Storage interface {
	// resets the storage driver - typically causing a reset of the client connection or similar
	// should be caused if the storage driver is currently failing
	// Reset() error
	Join(parts ...string) string
	Get(key string, opts ...func(*StoreOpts)) ([]byte, error)
	GetAll(prefix string, opts ...func(*StoreOpts)) (map[string][]byte, error)
	Put(key string, value []byte, opts ...func(*StoreOpts)) error
	PutAll(prefix string, values map[string][]byte, opts ...func(*StoreOpts)) error
	PutObject(key string, obj SerialAdapter, opts ...func(*StoreOpts)) error
	// CompareAndSwap atomically updates key from expectedValue to newValue, failing if current value doesn't match
	CompareAndSwap(key string, expectedValue, newValue []byte, opts ...func(*StoreOpts)) error
	// CompareAndCreate atomically creates key with value, failing if key already exists
	CompareAndCreate(key string, value []byte, opts ...func(*StoreOpts)) error
	MutateObject(key string, mutator MutateKVFunc, opts ...func(*StoreOpts)) error
	MutateObjectFromIndex(basecollection string, indexkey string, indexname string, mutator MutateKVFunc, opts ...func(*StoreOpts)) error
	// mutate multiple objects in one transaction, so that either all objects
	// are mutated or none are. See Mutate() option for each object. Can take multiple
	// Mutate options.
	MutateObjects(opts ...func(*StoreOpts)) error
	MutateValue(key string, mutator MutateKVDataFunc, opts ...func(*StoreOpts)) error
	PutObjects(prefix string, objs map[string]SerialAdapter, opts ...func(*StoreOpts)) error
	GetObject(key string, opts ...func(*StoreOpts)) (interface{}, error)
	GetObjectBytes(key string, opts ...func(*StoreOpts)) (data []byte, err error)
	GetObjectJson(key string, opts ...func(*StoreOpts)) (outjson []byte, err error)
	// Look up an object by a index relative to its collection. The index will point to a key relative to the collection
	GetObjectByCollectionIndex(basecollectionkey string, indexname string, indexkey string, opts ...func(*StoreOpts)) (ret interface{}, err error)
	// Look up an object by an external index. The index in this case should be point to an absolute path to the key where the data is
	GetObjectByOtherIndex(basecollectionkey string, indexname string, indexkey string, opts ...func(*StoreOpts)) (ret interface{}, err error)
	// GetObjects returns a map of objects under the specified prefix. Although relative indexes are stored
	// under the same path as the object, the index keys are ignored - since they are not the stored objects.
	GetObjects(prefix string, opts ...func(*StoreOpts)) (map[string]interface{}, error)
	// GetObjectsByIndex returns a map of objects under the specified prefix. The index is relative to the collection.
	// The indexname is the name of the index, and indexprefix is an optional prefix to the index key in order to get a subset of the index.
	// The indexprefix should just be "" to retrieve all objects in the index.
	GetObjectsByIndex(collectionPrefix string, indexname, indexprefix string, opts ...func(*StoreOpts)) (map[string]interface{}, error)
	// GetObjectsByOtherIndex returns a map of objects under the specified prefix. The index is an external index.
	// The indexFullPath is the full path to the index, and indexprefix is an optional prefix to the index key in order to get a subset of the index.
	// The indexprefix should just be "" to retrieve all objects in the index.
	// Right now this function only support getting objects that are in another single collection, if the index is pointing to objects
	// in multiple collections, this function will return an error.
	GetObjectsByOtherIndex(indexFullPath string, indexName string, indexprefix string, opts ...func(*StoreOpts)) (map[string]interface{}, error)
	// GetKeys returns a list of all keys under the specified prefix. It ignore keys which are index keys.
	GetKeys(prefix string, opts ...func(*StoreOpts)) ([]string, error)
	Delete(key string, opts ...func(*StoreOpts)) error
	DeleteAll(prefix string, opts ...func(*StoreOpts)) error
	MutateCollection(prefix string, mutator MutateCollectionFunc, opts ...func(*StoreOpts)) error
	GetInformerFactory() InformerFactory
	// FindUserByEmail(email string) (string, error)
}

type Informer interface {
	Watch(prefix string) (chan *Update, chan error)
}

type InformerFactory interface {
	MakeInformer(prefix string) Informer
	Start() error
	Stop() error
}

type Update struct {
	Version int64
	Key     string
	Value   []byte
}

type IndexActionType int

const (
	IndexNoAction IndexActionType = iota
	IndexActionUpdate
	IndexActionDelete
)

// a concrete type representing an index
// the index is a key that points to a value in a collection
type IndexAction struct {
	action IndexActionType
	// the base key for the index
	base string
	// index name
	name string
	// the key to use for the index
	key string
	// what the index points to
	pointer string
}

// Used by drivers to handle indexes in the storage API
type Index struct {
	Name string
	Base string
	Ptrs map[string]string
}

func (i *IndexAction) GetBase() string {
	return i.base
}

func (i *IndexAction) GetName() string {
	return i.name
}

func (i *IndexAction) GetKey() string {
	return i.key
}

func (i *IndexAction) GetPointer() string {
	return i.pointer
}
func (i *IndexAction) GetAction() IndexActionType {
	return i.action
}

// sets the base key for the index (if needed)
// If base is set, the the index will point to the full path of the object in the collection
// versus a relative path. By default indexes are assumed to be under the same base key as the
// collection of things they are point to.
func (i *IndexAction) Base(base string) *IndexAction {
	i.base = base
	return i
}

// 'pointer' is the value that the index will point to
// by default this is the primary key (not the full path, but relative to the collection) of the object in the collection
func (i *IndexAction) Pointer(pointer string) *IndexAction {
	i.pointer = pointer
	return i
}

// return an IndexAction that will update the index. 'indexname' is the name of the index
// and 'key' is the key to use for the index.
func UpdateIndex(indexname string, key string) *IndexAction {
	return &IndexAction{
		action: IndexActionUpdate,
		name:   indexname,
		key:    key,
	}
}

func DeleteIndex(indexname string, key string) *IndexAction {
	return &IndexAction{
		action: IndexActionDelete,
		name:   indexname,
		key:    key,
	}
}

// an index used for lookup
func AnIndex(indexname string, key string, pointer string) *IndexAction {
	return &IndexAction{
		name:    indexname,
		key:     key,
		pointer: pointer,
	}
}

type SerialAdapter interface {
	// finalizes the object to be encoded
	Finalize() error
	// all object with a SerialAdapter must support a protobuf encoding
	//	GetProtoMsg() (proto.Message, bool)
	// if GetProtoMsg() returns false, then this method must be implemented
	// otherwise it can just be a noop
	GetSerialData() ([]byte, error)
	Indexes(acts ...*IndexAction)
	// used by the storage driver to know what indexes to update
	SetCollectionBase(collectionbase string)
	GetCollectionBase() string
	GetCollectionKey() string
	SetCollectionKey(collectionkey string)
	GetIndexes() map[string]*Index
	GetDeleteIndexes() map[string]*Index
	// deep copy return a true complete memory copy of the object
	// by utilizing the SerialAdapter interface
	DeepCopy() (interface{}, error)
}

// creates a new SerialAdapter instance from the original
func NewSerialAdapterInstance(original SerialAdapter) SerialAdapter {
	originalType := reflect.TypeOf(original)
	if originalType.Kind() == reflect.Ptr {
		return reflect.New(originalType.Elem()).Interface().(SerialAdapter)
	}
	return reflect.New(originalType).Elem().Interface().(SerialAdapter)
}

type DeepCopyHandler struct {
	Self SerialAdapter
}

func (h *DeepCopyHandler) DeepCopy() (interface{}, error) {
	if h.Self == nil {
		return nil, fmt.Errorf("cannot deep copy without Self assigned. Does this object support a deep copy?")
	}
	// if the object does not support a protobuf encoding, then we cannot deep copy
	data, err := h.Self.GetSerialData()
	if err != nil {
		return nil, fmt.Errorf("error getting serial data: %w", err)
	}
	// var newobj SerialAdapter
	// newobj = NewSerialAdapterInstance(h.self)

	receivedAny := &anypb.Any{}
	if err := proto.Unmarshal(data, receivedAny); err != nil {
		log.Errorf("Failed to unmarshal bytes into Any: %v", err)
		return nil, err
	}
	msg, err := anypb.UnmarshalNew(receivedAny, proto.UnmarshalOptions{})
	if err != nil {
		log.Errorf("Failed to unmarshal Any into concrete type: %v", err)
		return nil, err
	}
	// newobj.

	// //	err = proto.Unmarshal(data, &newobj)
	// if err != nil {
	// 	return nil, fmt.Errorf("error unmarshaling data: %w", err)
	// }
	return msg, nil
}

type DefaultIndexHandler struct {
	// base key for all the indexes
	base          string
	collectionkey string
	// name: key: pointer
	indexes  map[string]*Index
	delindex map[string]*Index
}

func (d *DefaultIndexHandler) Indexes(acts ...*IndexAction) {
	if d.indexes == nil {
		d.indexes = make(map[string]*Index)
	}
	if d.delindex == nil {
		d.delindex = make(map[string]*Index)
	}
	for _, act := range acts {
		if d.base != "" && act.GetBase() != "" && act.GetBase() != d.base {
			log.Errorf("while setting indexes: base key mismatch: %s vs %s - here %s", act.GetBase(), d.base, log.Loc(log.StackLevel(2)))
		}
		var usebase string
		if act.GetBase() != "" {
			usebase = act.GetBase()
			//			d.base = act.GetBase()
		} else {
			usebase = d.base
		}
		switch act.GetAction() {
		case IndexActionUpdate:
			if d.indexes[act.GetName()] == nil {
				d.indexes[act.GetName()] = &Index{
					Name: act.GetName(),
					//					Base: act.GetBase(),
					Base: usebase,
					Ptrs: make(map[string]string),
				}
			}
			d.indexes[act.GetName()].Ptrs[act.GetKey()] = act.GetPointer()
		case IndexActionDelete:
			if d.delindex[act.GetName()] == nil {
				d.delindex[act.GetName()] = &Index{
					Name: act.GetName(),
					//					Base: act.GetBase(),
					Base: usebase,
					Ptrs: make(map[string]string),
				}
			}
			d.delindex[act.GetName()].Ptrs[act.GetKey()] = "d"
		}
	}
}

func (d *DefaultIndexHandler) SetCollectionBase(collectionbase string) {
	d.base = collectionbase
}

func (d *DefaultIndexHandler) GetCollectionBase() string {
	return d.base
}
func (d *DefaultIndexHandler) GetIndexes() map[string]*Index {
	return d.indexes
}
func (d *DefaultIndexHandler) GetDeleteIndexes() map[string]*Index {
	return d.delindex
}
func (d *DefaultIndexHandler) GetCollectionKey() string {
	return d.collectionkey
}
func (d *DefaultIndexHandler) SetCollectionKey(collectionkey string) {
	d.collectionkey = collectionkey
}
