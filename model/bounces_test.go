package model

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBounceMap1(t *testing.T) {
	bm := NewBounceMap(1000)

	mutex := sync.Mutex{}
	cnt := 0
	waiter := sync.WaitGroup{}

	cb := func(b *Bounce) (err error) {
		mutex.Lock()
		cnt++
		mutex.Unlock()
		t.Log("Bounce")
		waiter.Done()
		return nil
	}
	cb2 := func(b *Bounce) (err error) {
		mutex.Lock()
		cnt++
		mutex.Unlock()
		t.Log("Bounce Back")
		waiter.Done()
		return nil
	}

	bm.Start()
	waiter.Add(2)
	bounceid, _ := bm.NewBounce(cb)
	t.Log("BounceID: ", bounceid)
	bm.ReportBounceBack(bounceid, cb2)
	t.Log("at Wait()")
	waiter.Wait()
	if cnt != 2 {
		t.Error("Expected 2, got ", cnt)
	}
	t.Log("at WriteoutAndShutdown()")
	bm.WriteoutAndShutdown()
}

func TestBounceMapGoStale(t *testing.T) {
	bm := NewBounceMap(1000)

	mutex := sync.Mutex{}
	cnt := 0
	waiter := sync.WaitGroup{}

	cb := func(b *Bounce) (err error) {
		mutex.Lock()
		cnt++
		mutex.Unlock()
		t.Log("Bounce")
		waiter.Done()
		return nil
	}
	// cb2 := func(b *Bounce) (err error) {
	// 	mutex.Lock()
	// 	cnt++
	// 	mutex.Unlock()
	// 	t.Log("Bounce Back")
	// 	waiter.Done()
	// 	return nil
	// }

	bm.Start()
	waiter.Add(1)
	bounceid, _ := bm.NewBounce(cb)
	t.Log("BounceID: ", bounceid)
	// bm.ReportBounceBack(bounceid, cb2)
	t.Log("at Wait()")
	v := bm.Outstanding()
	assert.Equal(t, 1, v)
	waiter.Wait()
	if cnt != 1 {
		t.Error("Expected 1, got ", cnt)
	}
	v = bm.Outstanding()
	assert.Equal(t, 0, v)
	t.Log("at WriteoutAndShutdown()")
	bm.WriteoutAndShutdown()
}

func TestBounceMapShutdownNow(t *testing.T) {
	bm := NewBounceMap(1000)

	mutex := sync.Mutex{}
	cnt := 0
	waiter := sync.WaitGroup{}

	cb := func(b *Bounce) (err error) {
		mutex.Lock()
		cnt++
		mutex.Unlock()
		t.Log("Bounce")
		waiter.Done()
		return nil
	}

	bm.Start()
	waiter.Add(1)
	bounceid, _ := bm.NewBounce(cb)
	t.Log("BounceID: ", bounceid)
	// bm.ReportBounceBack(bounceid, cb2)
	t.Log("at Wait()")
	v := bm.Outstanding()
	assert.Equal(t, 1, v)
	t.Log("at ShutdownNow()")
	bm.ShutdownNow()
}

func TestBounceMap1000(t *testing.T) {
	// 2 second timeout
	bm := NewBounceMap(2000)

	mutex := sync.Mutex{}
	cnt := 0
	var waiter sync.WaitGroup
	calltwice := false
	badstringcompare := false
	doOne := func(num int) {
		var onemutex sync.Mutex
		cb1called := false
		cb2called := false

		cb := func(b *Bounce) (err error) {
			mutex.Lock()
			cnt++
			mycnt := cnt
			mutex.Unlock()
			onemutex.Lock()
			if cb1called {
				calltwice = true
				model_attn_debugf("cb1 called twice %d", mycnt)
			}
			cb1called = true
			onemutex.Unlock()

			t.Logf("Bounce (%d) %d\n", num, cnt)
			waiter.Done()
			return nil
		}
		cb2 := func(b *Bounce) (err error) {
			mutex.Lock()
			cnt++
			mycnt := cnt
			mutex.Unlock()
			onemutex.Lock()
			if cb2called {
				calltwice = true
				model_attn_debugf("cb2 called twice %d", mycnt)
			}
			cb2called = true
			onemutex.Unlock()

			s := fmt.Sprintf("teststr %s", b.Data[2])
			if s != b.Data[1] {
				model_attn_debugf("mismtach on string data %d", mycnt)
				badstringcompare = true
			}
			t.Logf("Bounce (%d) Back %d\n", num, cnt)
			waiter.Done()
			return nil
		}
		teststrnum := fmt.Sprintf("%d", num)
		teststr := fmt.Sprintf("teststr %d", num)
		bounceid, _ := bm.NewBounce(cb, map[uint32]interface{}{1: strings.Clone(teststr), 2: strings.Clone(teststrnum)})

		go func() {
			time.Sleep(time.Duration((100 + rand.Int63n(800))) * time.Millisecond)
			bm.ReportBounceBack(bounceid, cb2)
		}()

	}

	loop := 1000

	waiter.Add(1)

	bm.Start()

	for i := 0; i < loop; i++ {
		waiter.Add(2)
		doOne(i)
	}
	waiter.Done()
	t.Log("at Wait()")
	waiter.Wait()
	if cnt != 2000 {
		t.Error("Expected 2000, got ", cnt)
	}
	assert.False(t, calltwice)
	assert.False(t, badstringcompare)
	t.Log("at WriteoutAndShutdown()")
	bm.WriteoutAndShutdown()
}
