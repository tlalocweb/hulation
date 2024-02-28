package utils

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tlalocweb/hulation/log"
)

type Shutdownable interface {
	ShutdownNow()
	Shutdown()
}

// TODO - need a way to track all Shutdownable objects and call their ShutdownNow() method
// on app shutdown

type RunOnceFunc func() error

type RunOnceMaxInterval struct {
	mutex     *sync.Mutex
	nextmutex *sync.Mutex
	Interval  time.Duration
	LastRun   time.Time
	runf      RunOnceFunc
	runnext   RunOnceFunc
}

func NewRunOnceMaxInterval(interval time.Duration) (r *RunOnceMaxInterval) {
	r = &RunOnceMaxInterval{
		mutex:     &sync.Mutex{},
		nextmutex: &sync.Mutex{},
		Interval:  interval,
	}
	return
}

func (r *RunOnceMaxInterval) runit() {
	defer r.mutex.Unlock()
	// if r.LastRun.IsZero() || time.Since(r.LastRun) > r.Interval {
	r.LastRun = time.Now()
	err := r.runf()
	if err != nil {
		log.Errorf("RunOnceMaxInterval: error running function: %s", err.Error())
	}
	// }
}

// Always returns immediately.
// It attempts to run the function in a new go routine, but if it is already running, it skips it
// or if it has been run within the interval, it skips it. If it skips running it, it will run the
// function once (and only once) after the interval has passed.
func (r *RunOnceMaxInterval) Run(f func() error) (err error) {
	ok := r.mutex.TryLock()
	if !ok {
		//		log.Debugf("skipping run (mutex)")
		r.nextmutex.Lock()
		if r.runnext == nil {
			//			log.Debugf("flagging for future run")
			r.runnext = f
		}
		r.nextmutex.Unlock()
		// flag for a run
		// clearly we are still running the func
		return
	}

	if r.LastRun.IsZero() || time.Since(r.LastRun) > r.Interval {
		//		log.Debugf("starting go run now")
		r.runf = f
		go r.runit()
	} else {
		//		log.Debugf("skipping run (interval)")
		r.mutex.Unlock()
		r.nextmutex.Lock()
		defer r.nextmutex.Unlock()
		if r.runnext == nil {
			//			log.Debugf("flagging for future run")
			r.runnext = f
			go func() {
				time.Sleep(r.Interval + 5)
				r.nextmutex.Lock()
				r.runf = r.runnext
				r.runnext = nil
				r.nextmutex.Unlock()
				r.mutex.Lock()
				//				log.Debugf("starting go run next")
				go r.runit()
			}()
		}
	}
	return
}

const (
	shutdownDeferredRunnerNow    = 1
	shutdownDeferredRunnerAtZero = 0
)

type DeferredRunner struct {
	name        string
	queue       chan RunOnceFunc
	shutdown    chan int
	onExit      func()
	shutdownNow *atomic.Bool
	// nextmutex *sync.Mutex
	// Interval  time.Duration
	// LastRun   time.Time
	// runf      RunOnceFunc
	// runnext   RunOnceFunc
}

func NewDeferredRunner(name string) (r *DeferredRunner) {
	r = &DeferredRunner{
		queue:       make(chan RunOnceFunc, 100),
		shutdown:    make(chan int, 1),
		name:        name,
		shutdownNow: &atomic.Bool{},
	}
	return
}

// Always returns immediately.
// It attempts to run the function in a new go routine, but if it is already running, it skips it
// or if it has been run within the interval, it skips it. If it skips running it, it will run the
// function once (and only once) after the interval has passed.
func (r *DeferredRunner) Run(f func() error) (err error) {
	r.queue <- f
	return
}

func (r *DeferredRunner) runner() {
	defer func() {
		if r.onExit != nil {
			r.onExit()
		}
	}()
	var requestShutdown bool
	for {
		fmt.Printf("DeferredRunner: top of runner loop\n")
		if len(r.queue) < 1 && requestShutdown {
			return
		}
		select {
		case shutdown := <-r.shutdown:
			fmt.Printf("DeferredRunner: shutdown signal received\n")
			if shutdown == shutdownDeferredRunnerNow {
				return
			} else {
				requestShutdown = true
			}
		case f := <-r.queue:
			// why is this shutdownNow thing needed?
			// despite many examples showing that channels will work for this type use case
			// it seems that the channel implementation does not always _devlier_ the message
			// to the channel immediately. This makes the 'shutdown' channel seem "slow"
			// The atomic bool set is immediate.
			done := r.shutdownNow.Load()
			if done {
				return
			}
			fmt.Printf("DeferredRunner: running function\n")
			err := f()
			if err != nil {
				log.Errorf("DeferredRunner %s: error running function: %s", r.name, err.Error())
			}
		}
	}
}

func (r *DeferredRunner) Start() {
	go r.runner()
}

func (r *DeferredRunner) ShutdownNow(onexit func()) {
	r.onExit = onexit
	r.shutdownNow.Store(true)
	r.shutdown <- shutdownDeferredRunnerNow
}

func (r *DeferredRunner) Shutdown(onexit func()) {
	r.onExit = onexit
	r.shutdown <- shutdownDeferredRunnerAtZero
}
