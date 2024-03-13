package hooks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"

	"github.com/risor-io/risor"
	"github.com/risor-io/risor/compiler"
	"github.com/risor-io/risor/object"
	"github.com/risor-io/risor/parser"
)

type runBaton struct {
	cancelfunc context.CancelFunc
	hook       *RisorHook
	id         string
}

type OnErrHookFunc func(id string, err error)
type OnCompleteHookFunc func(id string, ret interface{})

func newRunBaton(hook *RisorHook) *runBaton {
	return &runBaton{
		hook: hook,
		id:   utils.FastRandString(8),
	}
}

type RisorExecutor struct {
	runners []*utils.DeferredRunner
	// round robin leveler - perhaps in the future we can do something more sophisticated
	rr    int
	mutex *sync.Mutex
	// cache of compiled scripts
	cache       *utils.InMemCache
	cfg         *risor.Config
	maxexectime time.Duration
	// shutdownChan chan int
}

type RisorHook struct {
	Name       string
	Script     string `json:"script"`
	Globals    map[string]any
	hash       string
	compiled   *CompiledCode
	OnComplete OnCompleteHookFunc `json:"-"`
	OnErr      OnErrHookFunc      `json:"-"`
}

type CompiledCode struct {
	bytecode *compiler.Code
}

func NewRisorExecutor(threads int, maxexectime time.Duration) (ret *RisorExecutor) {
	ret = &RisorExecutor{
		runners:     make([]*utils.DeferredRunner, threads),
		mutex:       &sync.Mutex{},
		cache:       utils.NewInMemCache().WithCleanInterval(time.Hour * 24).Start(),
		maxexectime: maxexectime,
	}
	if ret.maxexectime < 1 {
		ret.maxexectime = time.Second * 30
	}
	for i := 0; i < threads; i++ {
		ret.runners[i] = utils.NewDeferredRunner(fmt.Sprintf("risor-executor[%d]", i))
		ret.runners[i].Start()
	}
	return ret
}

func (exec *RisorExecutor) Shutdown() {
	for _, r := range exec.runners {
		r.Shutdown(nil)
	}
}

// computes a sha256 sum based on the keys on the map of globals
// it does not look at the type - since doing so we would require reflection.
func stringifyKeys(m map[string]any) (ret string, err error) {
	// make map to json string
	//	var str string
	if m == nil {
		return
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	// you have to put them in a slice
	// and sort. B/c Go's range of keys in a map
	// does not guarantee order and will return different order per call.
	sort.Strings(keys)
	ret = strings.Join(keys, ":")
	log.Tracef("stringifyKeys: %s", ret)
	//	ret = utils.GenSha256Hash(str)
	return
}

func getRisorScriptHash(script string, globals map[string]any) (ret string) {
	globalshash, err := stringifyKeys(globals)
	if err != nil {
		log.Errorf("Error computing globals hash for risor script. Error: %s", err.Error())
		globalshash = utils.FastRandString(8)
	}
	//	ret = fmt.Sprintf("%s:%s", utils.GenSha256Hash(script), globalshash)
	ret = utils.GenSha256Hash(strings.Join([]string{globalshash, script}, " "))
	log.Tracef("risor script hash: %s", ret)
	return
}

func (exec *RisorExecutor) Compile(hook *RisorHook) (code *CompiledCode, err error) {
	if len(hook.hash) < 1 {
		hook.hash = getRisorScriptHash(hook.Script, hook.Globals)
	}
	if codei, ok := exec.cache.Get(hook.hash); ok {
		log.Debugf("risor script found in cache")
		code = codei.(*CompiledCode)
		hook.compiled = code
		return
	}
	//cfg := risor.NewConfig(options...)
	exec.cfg = risor.NewConfig(risor.WithGlobals(hook.Globals))
	// Parse the source code to create the AST
	ast, err := parser.Parse(context.Background(), hook.Script)
	if err != nil {
		err = fmt.Errorf("error parsing risor script (hook %s): %s", hook.Name, err.Error())
		return
	}

	code = &CompiledCode{}

	// Compile the AST to bytecode, appending these new instructions after any
	// instructions that were previously compiled
	code.bytecode, err = compiler.Compile(ast, exec.cfg.CompilerOpts()...)
	if err != nil {
		err = fmt.Errorf("error compiling risor script (hook %s): %s", hook.Name, err.Error())
		return
	}
	hook.compiled = code
	// cache so we dont need to recompile
	exec.cache.Set(hook.hash, code)
	return
}

type batonType struct{}

var batonKey = batonType{}

// Submit a risor script for execution. Name is simply used to indentify the script in logs.
func (exec *RisorExecutor) SubmitForExec(hook *RisorHook, globals map[string]any) (id string) {
	exec.mutex.Lock()
	// baton := newHookRun(nil)
	// id = baton.id
	id = utils.FastRandString(8)
	baton := newRunBaton(hook)
	var gotError atomic.Bool
	exec.runners[exec.rr].Run(func() (err error) {
		log.Tracef("risor.Run() %p", hook)
		if hook.compiled == nil {
			log.Debugf("hook not compiled.. attempting compile")
			_, err = exec.Compile(hook)
			if err != nil {
				return
			}
			//			return fmt.Errorf("hook not compiled")
		}
		var ret object.Object
		done := make(chan *runBaton, 1)
		//		var gotError atomic.Bool
		// create the context timeout once the run thread has come up in queue
		ctx, cancelfunc := context.WithTimeout(context.WithValue(context.Background(), batonKey, baton), exec.maxexectime)
		baton.cancelfunc = cancelfunc
		go func() {
			select {
			case b := <-done:
				log.Debugf("risor script execution done")
				b.cancelfunc()
				if b.hook.OnComplete != nil {
					b.hook.OnComplete(b.id, ret.Interface())
				}
			case <-ctx.Done():
				b := ctx.Value(batonKey).(*runBaton)
				log.Tracef("ctx.Done() triggered")
				if ctx.Err() != nil {
					if gotError.CompareAndSwap(false, true) {
						if ctx.Err() == context.DeadlineExceeded {
							log.Tracef("context.DeadlineExceeded")
							if b.hook.OnErr != nil {
								b.hook.OnErr(b.id, fmt.Errorf("timeout risor script"))
							}
						} else {
							log.Tracef("context error: %s", ctx.Err().Error())
							if b.hook.OnErr != nil {
								b.hook.OnErr(b.id, fmt.Errorf("context error: %s", ctx.Err().Error()))
							}
						}
					} else {
						log.Tracef("gotError already set")
					}
				}
			}
		}()
		if globals == nil {
			ret, err = risor.EvalCode(ctx, hook.compiled.bytecode)
		} else {
			log.Tracef("hook = %v", hook.Name)
			log.Tracef("hook.compiled = %v", hook.compiled)
			ret, err = risor.EvalCode(ctx, hook.compiled.bytecode, risor.WithGlobals(globals))
		}
		if err != nil {
			if gotError.CompareAndSwap(false, true) {
				log.Tracef("risor.EvalCode error (hook %s): %s", hook.Name, err.Error())
				if hook.OnErr != nil {
					go hook.OnErr(id, err)
				}
				return
			} else {
				log.Tracef("gotError already set (2)")
			}
		} else {
			log.Tracef("risor.EvalCode done")
			done <- baton
			log.Tracef("past <-done")
		}
		return
	})
	exec.rr++
	if exec.rr >= len(exec.runners) {
		exec.rr = 0
	}
	exec.mutex.Unlock()
	return baton.id
}
