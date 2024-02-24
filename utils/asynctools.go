package utils

import (
	"sync"
	"time"

	"github.com/tlalocweb/hulation/log"
)

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
