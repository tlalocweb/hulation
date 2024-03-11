package hooks

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRisorExecutor(t *testing.T) {
	exec := NewRisorExecutor(1, time.Second*30)

	if exec == nil {
		t.Errorf("NewRisorExecutor returned nil")
	}

	wg := &sync.WaitGroup{}

	var valinnext int64

	newhook := &RisorHook{
		Script:  "a + 1",
		Globals: map[string]interface{}{"a": 1},
		OnErr: func(id string, err error) {
			t.Errorf("Error in hook: %s", err.Error())
			wg.Done()
		},
		OnComplete: func(id string, ret interface{}) {
			valinnext = ret.(int64)
			wg.Done()
		},
	}

	_, err := exec.Compile(newhook)
	if err != nil {
		t.Errorf("Compile returned error: %s", err.Error())
	}

	wg.Add(1)
	execglobals := map[string]interface{}{"a": int64(30)}
	exec.SubmitForExec(newhook, execglobals)

	wg.Wait()

	assert.Equal(t, int64(31), valinnext, "Next function did not return expected value")
	assert.Equal(t, int64(30), execglobals["a"], "Next function did not return expected value")
	exec.Shutdown()
}

func TestRisorHookTimeout(t *testing.T) {
	exec := NewRisorExecutor(1, time.Second*1)

	if exec == nil {
		t.Errorf("NewRisorExecutor returned nil")
	}

	wg := &sync.WaitGroup{}
	var timeout bool
	var calledcomplete bool
	newhook := &RisorHook{
		Script: `time.sleep(30)
		a + 1`,
		Globals: map[string]interface{}{"a": 1},
		OnErr: func(id string, err error) {
			timeout = true
			//			t.Errorf("Error in hook: %s", err.Error())
			wg.Done()
		},
		OnComplete: func(id string, ret interface{}) {
			calledcomplete = true
			t.Errorf("Completed hook should not have happened - should have timed out: %v", ret)
			wg.Done()
		},
	}

	_, err := exec.Compile(newhook)
	if err != nil {
		t.Errorf("Compile returned error: %s", err.Error())
	}

	wg.Add(1)
	execglobals := map[string]interface{}{"a": int64(30)}
	exec.SubmitForExec(newhook, execglobals)

	wg.Wait()

	exec.Shutdown()

	assert.True(t, timeout, "Timeout did not happen")
	assert.False(t, calledcomplete, "Completed hook should not have happened")

}

func TestRisor10x100(t *testing.T) {
	exec := NewRisorExecutor(10, time.Second*10)

	if exec == nil {
		t.Errorf("NewRisorExecutor returned nil")
	}
	var errcount atomic.Int64
	var completecnt atomic.Int64
	wg := &sync.WaitGroup{}
	newhook := &RisorHook{
		Script: `time.sleep(1)
		a + 1`,
		Globals: map[string]interface{}{"a": 1},
		OnErr: func(id string, err error) {
			errcount.Add(1)
			//			t.Errorf("Error in hook: %s", err.Error())
			wg.Done()
		},
		OnComplete: func(id string, ret interface{}) {
			completecnt.Add(1)
			wg.Done()
		},
	}

	_, err := exec.Compile(newhook)
	if err != nil {
		t.Errorf("Compile returned error: %s", err.Error())
	}

	for n := 0; n < 100; n++ {
		wg.Add(1)
		execglobals := map[string]interface{}{"a": int64(30)}
		exec.SubmitForExec(newhook, execglobals)
	}

	wg.Wait()

	exec.Shutdown()

	assert.Equal(t, int64(0), errcount.Load(), "Error count should be 0")
	assert.Equal(t, int64(100), completecnt.Load(), "Complete count should be 100")
}

func TestRisor10x100Error(t *testing.T) {
	exec := NewRisorExecutor(10, time.Second*10)

	if exec == nil {
		t.Errorf("NewRisorExecutor returned nil")
	}
	var errcount atomic.Int64
	var completecnt atomic.Int64
	wg := &sync.WaitGroup{}
	newhook := &RisorHook{
		Script: `time.sleep(1)
		a + 1`,
		Globals: map[string]interface{}{"a": 1},
		OnErr: func(id string, err error) {
			errcount.Add(1)
			//			t.Errorf("Error in hook: %s", err.Error())
			wg.Done()
		},
		OnComplete: func(id string, ret interface{}) {
			completecnt.Add(1)
			wg.Done()
		},
	}

	_, err := exec.Compile(newhook)
	if err != nil {
		t.Errorf("Compile returned error: %s", err.Error())
	}

	for n := 0; n < 100; n++ {
		wg.Add(1)
		//		execglobals := map[string]interface{}{"a": int64(30)}
		// 'a' will not be in the globals, so it will error
		exec.SubmitForExec(newhook, nil)
	}

	wg.Wait()

	exec.Shutdown()

	assert.Equal(t, int64(100), errcount.Load(), "Error count should be 0")
	assert.Equal(t, int64(0), completecnt.Load(), "Complete count should be 100")
}
