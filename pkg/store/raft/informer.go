package raft

import (
	"strings"
	"sync"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/common"
)

var informerLogger = log.GetTaggedLogger("informer", "Raft informer factory")

// InformerFactory creates and manages informers for watching key changes
type InformerFactory struct {
	raftNode  *RaftNode
	informers map[string]*RaftInformer
	mu        sync.RWMutex
	logger    *log.TaggedLogger
	running   bool
}

// NewInformerFactory creates a new informer factory
func NewInformerFactory(raftNode *RaftNode) *InformerFactory {
	return &InformerFactory{
		raftNode:  raftNode,
		informers: make(map[string]*RaftInformer),
		logger:    informerLogger,
		running:   false,
	}
}

// MakeInformer creates a new informer for the given prefix
func (f *InformerFactory) MakeInformer(prefix string) common.Informer {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Check if informer already exists
	if informer, exists := f.informers[prefix]; exists {
		f.logger.Debugf("Returning existing informer for prefix: %s", prefix)
		return informer
	}

	// Create new informer
	informer := &RaftInformer{
		prefix:    prefix,
		updateCh:  make(chan *common.Update, 100),
		errorCh:   make(chan error, 10),
		stopCh:    make(chan struct{}),
		logger:    f.logger,
		observers: make(map[chan *common.Update]chan error),
	}

	f.informers[prefix] = informer
	f.logger.Infof("Created new informer for prefix: %s", prefix)

	return informer
}

// Start starts the informer factory
func (f *InformerFactory) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.running {
		f.logger.Warnf("Informer factory already running")
		return nil
	}

	f.logger.Infof("Starting informer factory")

	// Add observer to Raft node
	f.raftNode.AddObserver(f.updateCh())

	f.running = true

	// Start distribution goroutine
	go f.distributeUpdates()

	return nil
}

// Stop stops the informer factory
func (f *InformerFactory) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.running {
		return nil
	}

	f.logger.Infof("Stopping informer factory")

	// Remove observer from Raft node
	f.raftNode.RemoveObserver(f.updateCh())

	// Stop all informers
	for prefix, informer := range f.informers {
		close(informer.stopCh)
		f.logger.Debugf("Stopped informer for prefix: %s", prefix)
	}

	f.running = false
	return nil
}

// updateCh returns a channel for receiving updates from Raft
func (f *InformerFactory) updateCh() chan<- *common.Update {
	ch := make(chan *common.Update, 100)

	go func() {
		for update := range ch {
			f.distributeUpdate(update)
		}
	}()

	return ch
}

// distributeUpdates distributes updates to relevant informers
func (f *InformerFactory) distributeUpdates() {
	f.logger.Debugf("Starting update distribution")

	// This function is called when we receive updates from the Raft node
	// and need to distribute them to the appropriate informers
}

// distributeUpdate sends an update to all matching informers
func (f *InformerFactory) distributeUpdate(update *common.Update) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	for prefix, informer := range f.informers {
		if strings.HasPrefix(update.Key, prefix) {
			select {
			case informer.updateCh <- update:
			default:
				f.logger.Warnf("Informer for prefix %s is slow, dropping update", prefix)
			}
		}
	}
}

// RaftInformer implements the Informer interface for watching key changes
type RaftInformer struct {
	prefix    string
	updateCh  chan *common.Update
	errorCh   chan error
	stopCh    chan struct{}
	logger    *log.TaggedLogger
	mu        sync.RWMutex
	observers map[chan *common.Update]chan error
}

// Watch starts watching for changes to keys with the given prefix
func (i *RaftInformer) Watch(prefix string) (chan *common.Update, chan error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	updateCh := make(chan *common.Update, 100)
	errCh := make(chan error, 10)

	// Store the observer
	i.observers[updateCh] = errCh

	// Start forwarding updates
	go i.forwardUpdates(prefix, updateCh, errCh)

	i.logger.Debugf("Started watch for prefix: %s", prefix)
	return updateCh, errCh
}

// forwardUpdates forwards updates from the main channel to the observer
func (i *RaftInformer) forwardUpdates(prefix string, updateCh chan *common.Update, errCh chan error) {
	defer func() {
		i.mu.Lock()
		delete(i.observers, updateCh)
		i.mu.Unlock()
		close(updateCh)
		close(errCh)
	}()

	for {
		select {
		case update, ok := <-i.updateCh:
			if !ok {
				i.logger.Debugf("Update channel closed for prefix: %s", prefix)
				return
			}

			// Only forward updates matching the watch prefix
			if strings.HasPrefix(update.Key, prefix) {
				select {
				case updateCh <- update:
				case <-i.stopCh:
					return
				}
			}

		case err, ok := <-i.errorCh:
			if !ok {
				return
			}

			select {
			case errCh <- err:
			case <-i.stopCh:
				return
			}

		case <-i.stopCh:
			i.logger.Debugf("Watch stopped for prefix: %s", prefix)
			return
		}
	}
}
