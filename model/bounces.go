package model

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alphadose/haxmap"
	"github.com/tlalocweb/hulation/utils"
)

// This data structure is used to prevent duplicate visitor records
// from the same unique visit.
// this is needed since we record visitors in multiple ways:
// - via a broswer based script
// - via a nonscript method using an iframe
// - other future ways

// When a recording is collected, it is store in the bounce
// after a delayed period of time, the bounce is recorded in the database

const (
	// ScriptMethod BounceMethod = 0x00000001
	// IframeMethod BounceMethod = 0x00000002
	// the logucal AND of the above
	// bounceComplete     BounceMethod = ScriptMethod & IframeMethod
	MAX_QUEUD_WRITEOUT = 2 ^ 16
)

type BounceMethod int64

type OnBounce func(bnc *Bounce) error

type Bounce struct {
	deadline int64
	//method   BounceMethod
	Visitor *Visitor
	Data    map[uint32]interface{}
	// optional use flags
	Flags uint64
	Id    string
	// Cookie   *VisitorCookie
	// SSCookie *VisitorCookie
	// set by the code creating the bounce
	writecb OnBounce
	// set by the code reporting that bounce ID coming back
	writecb2 OnBounce
}

const (
	bShutdown         int = 0x1
	bShutdownnow      int = 0x3
	ErrBounceNotFound     = "Bounce Not Found"
)

type bounceNofity struct {
	event int
}

type BounceMap struct {
	// a cookie set _ONLY_ for the bounce
	byBouncID *haxmap.Map[string, *Bounce]
	// a cookie set for the visitor - expires in a year (or whatever default
	// but may be removed by the browser or user at some abritrary time
	//	byCookieID *haxmap.Map[string, *bounce]
	// HttpOnly session cookie
	//	bySSCookieID *haxmap.Map[string, *bounce]
	// read only by worker
	// we use this "ZenQ" for supposed performance improvements when adding to the
	// queue - and possible future perf improvements later
	// (multiple weiters, etc)
	// writeQ  *zenq.ZenQ[*Bounce]
	// notifyQ *zenq.ZenQ[*bounceNofity]
	writeQ  chan *Bounce
	notifyQ chan *bounceNofity
	// use this to to notify that writeQ has stuff to write
	//	writeWakeup chan bool
	timeout int64
	// just used in waiting for shutdown
	shutdownCount *sync.WaitGroup
	// only for worker
	shutdown    chan bool
	shutdownnow chan bool
	// for writer
	staleWaker  *sync.Cond
	staleMutex  sync.Mutex
	workerCount *sync.WaitGroup
	// for writer - atomic use only
	shutdownWorkers int32
	// only for findStale
	staleInterval *time.Timer
}

func NewBounceMap(timeout int64) (b *BounceMap) {
	b = &BounceMap{}
	b.shutdown = make(chan bool)
	b.shutdownnow = make(chan bool)
	// b.writerMutex = sync.Mutex{}
	// b.writerWaker = sync.NewCond(&b.writerMutex)
	b.staleMutex = sync.Mutex{}
	b.staleWaker = sync.NewCond(&b.staleMutex)
	b.workerCount = &sync.WaitGroup{}
	b.shutdownCount = &sync.WaitGroup{}
	b.timeout = timeout
	b.writeQ = make(chan *Bounce, MAX_QUEUD_WRITEOUT)
	b.notifyQ = make(chan *bounceNofity, 5)
	// b.writeQ = zenq.New[*Bounce](MAX_QUEUD_WRITEOUT)
	// b.notifyQ = zenq.New[*bounceNofity](5)

	b.byBouncID = haxmap.New[string, *Bounce]()
	return b
}

func (b *BounceMap) Outstanding() (cnt int) {
	return int(b.byBouncID.Len())
}

func (b *BounceMap) NewBounce(cb OnBounce, data ...map[uint32]interface{}) (bounceid string, bnc *Bounce) {
	bnc = &Bounce{}
	bnc.Id = utils.FastRandString(16)
	bnc.Data = make(map[uint32]interface{})
	bnc.deadline = time.Now().UnixMilli() + b.timeout
	bnc.writecb = cb
	bounceid = utils.FastRandString(16)
	b.byBouncID.Set(bounceid, bnc)
	for _, d := range data {
		for k, v := range d {
			bnc.Data[k] = v
		}
	}
	return
}

func (b *BounceMap) NewBounceWithVisitor(v *Visitor, cb OnBounce, data ...map[uint32]interface{}) (bounceid string, bnc *Bounce) {
	bnc = &Bounce{}
	bnc.Id = utils.FastRandString(16)
	bnc.Data = make(map[uint32]interface{})
	bnc.Visitor = v
	bnc.deadline = time.Now().UnixMilli() + b.timeout
	bnc.writecb = cb
	bounceid = utils.FastRandString(16)
	b.byBouncID.Set(bounceid, bnc)
	for _, d := range data {
		for k, v := range d {
			bnc.Data[k] = v
		}
	}
	return
}

// Given a CookieID, this will return a
func (b *BounceMap) ReportBounceBack(bounceid string, cb OnBounce, data ...map[uint32]interface{}) (err error) {
	bnc, loaded := b.byBouncID.GetAndDel(bounceid)
	if loaded {
		for _, d := range data {
			for k, v := range d {
				bnc.Data[k] = v
			}
		}
		bnc.writecb2 = cb
		//		b.writeQ.Write(bnc)
		b.writeQ <- bnc
		//		b.writerWaker.Broadcast()
	} else {
		err = fmt.Errorf(ErrBounceNotFound)
	}
	return
}

const (
	shutdownWriterAtZero int32 = 1
	shutdownWriterNow    int32 = 2
)

func (b *BounceMap) writeOne() {
	var shutdown bool
writerLoop:
	for {
		model_debugf("BounceMap: top of writerLoop")
		if shutdown {
			model_debugf("BounceMap: writerLoop has shutdown")
			// if b.writeQ.Size() < 1 {
			// 	break writerLoop
			// }
			if len(b.writeQ) < 1 {
				break writerLoop
			}
			model_debugf("BounceMap: writerLoop has queued writes, but shutdown")
		}
		model_debugf("BounceMap: writerLoop at Select")
		select {
		case bnc := <-b.writeQ:
			if bnc.writecb != nil {
				err := bnc.writecb(bnc)
				if err != nil {
					model_attn_debugf("BounceMap: writerLoop writecb error: %v", err)
				}
			}
			if bnc.writecb2 != nil {
				err := bnc.writecb2(bnc)
				if err != nil {
					model_attn_debugf("BounceMap: writerLoop writecb2 error: %v", err)
				}
			}
			model_debugf("BounceMap: writerLoop executed callbacks on %s", bnc.Id)
		case ev := <-b.notifyQ:
			switch ev.event {
			case bShutdown:
				model_debugf("BounceMap: writerLoop got shutdown")
				shutdown = true
			case bShutdownnow:
				break writerLoop
			}
		}

		// if dat := zenq.Select(b.notifyQ, b.writeQ); dat != nil {
		// 	switch dat.(type) {
		// 	case *bounceNofity:
		// 		switch dat.(*bounceNofity).event {
		// 		case bShutdown:
		// 			model_debugf("BounceMap: writerLoop got shutdown")
		// 			shutdown = true
		// 		case bShutdownnow:
		// 			break writerLoop
		// 		}
		// 	case *Bounce:
		// 		bnc := dat.(*Bounce)
		// 		if bnc.writecb != nil {
		// 			bnc.writecb(bnc)
		// 		}
		// 		if bnc.writecb2 != nil {
		// 			bnc.writecb2(bnc)
		// 		}
		// 	}

	}
	model_debugf("BounceMap: writerLoop exiting")
	b.workerCount.Done()
}

// looks through the maps for bounces that are past their deadline
// and places them in the queue
func (b *BounceMap) findStale() {
findStaleLoop:
	for {
		model_debugf("BounceMap: top of findStaleLoop")
		b.staleWaker.L.Lock()
		model_debugf("BounceMap: findStaleLoop waiting")
		b.staleWaker.Wait()
		model_debugf("BounceMap: findStaleLoop past wait")
		b.staleWaker.L.Unlock()
		if atomic.CompareAndSwapInt32(&b.shutdownWorkers, shutdownWriterNow, b.shutdownWorkers) {
			break findStaleLoop
		}

		b.byBouncID.ForEach(func(k string, v *Bounce) bool {
			if v.deadline < time.Now().UnixMilli() {
				model_debugf("BounceMap: findStaleLoop found stale bounce")
				b.byBouncID.Del(k)
				b.writeQ <- v
				// if !b.writeQ.Write(v) {
				// if the queue is closed then end the thread
				// 	return false
				// }
			}
			return true
		})

		if atomic.CompareAndSwapInt32(&b.shutdownWorkers, shutdownWriterAtZero, b.shutdownWorkers) {
			// if b.writeQ.Size() < 1 {
			if len(b.writeQ) < 1 {
				break findStaleLoop
			}
		}
		b.staleInterval.Reset(time.Millisecond * time.Duration(b.timeout))
	}
	model_debugf("BounceMap: findStaleLoop exiting")
	b.workerCount.Done()
}

func (b *BounceMap) worker() {
	b.staleInterval = time.NewTimer(time.Millisecond * time.Duration(b.timeout))
	b.workerCount.Add(2)
	go b.writeOne()
	go b.findStale()
	//workLoop:
	for {
		// check for writes
		select {
		// case <-b.writeWakeup:
		// 	b.writerWaker.Broadcast()
		case <-b.shutdownnow:
			// b.notifyQ.Write(&bounceNofity{event: bShutdownnow})
			b.notifyQ <- &bounceNofity{event: bShutdownnow}
			atomic.SwapInt32(&b.shutdownWorkers, shutdownWriterNow)
			return
		case <-b.shutdown:
			// write out all bounces
			atomic.StoreInt32(&b.shutdownWorkers, shutdownWriterAtZero)
			b.notifyQ <- &bounceNofity{event: bShutdown}
			// b.notifyQ.Write(&bounceNofity{event: bShutdown})
			// required to signal the Select() call in writeOne()
			// b.writeQ.Close()
			close(b.notifyQ)
			b.staleWaker.Broadcast()

			model_debugf("BounceMap: Waiting for workers to finish")
			b.workerCount.Wait()
			model_debugf("BounceMap: workers all done")
			b.shutdownCount.Done()
			close(b.writeQ)
			return
		case <-b.staleInterval.C:
			model_debugf("BounceMap: staleInterval fired")
			// loop through interval and write out all bounces
			b.staleWaker.Broadcast()
		}
	}
}

// for SIGINT or similar
// bombs out of the worker immediately - pending writes are lost
func (b *BounceMap) ShutdownNow() {
	b.shutdownnow <- true
}

// This will block. It tells the BounceMap to shutdown
// it's worker, and then waits for the shutdown to complete
func (b *BounceMap) WriteoutAndShutdown() {
	b.shutdownCount.Add(1)
	b.shutdown <- true
	b.shutdownCount.Wait()
}

func (b *BounceMap) Start() {
	go b.worker()
}

func init() {
}
