package hooks

import "time"

const (
	// later these can be tunables:
	risorRunners       = 10
	risorScriptTimeout = 30 * time.Second
)

var risorExecutor *RisorExecutor

func GetRisorExecutor() *RisorExecutor {
	if risorExecutor == nil {
		risorExecutor = NewRisorExecutor(risorRunners, risorScriptTimeout)
	}
	return risorExecutor
}

// Keeps track of all the global variables that are available to
// hook code. This is necessary b/c scripts are pre-compiled (so the globals
// must be define) and then the same globals names should be used during
// the execution.
type TemplateGlobalsForHooks struct {
	template map[string]any
}

func NewTemplateGlobalsForHooks() *TemplateGlobalsForHooks {
	return &TemplateGlobalsForHooks{
		template: map[string]any{},
	}
}

func (tmp *TemplateGlobalsForHooks) AddTemplateGlobals(some map[string]any) {
	for k, v := range some {
		tmp.template[k] = v
	}
}

// Takes a list of global variables and mixes them in with this
// template's globals. If there are any conflicts, the globals passed
// in will override the template's globals.
// then a new map is returned.
func (tmp *TemplateGlobalsForHooks) MixInGlobals(globals ...map[string]any) (out map[string]any) {
	out = map[string]any{}
	for _, m := range globals {
		for k, v := range m {
			out[k] = v
		}
	}
	for k, v := range tmp.template {
		out[k] = v
	}
	return

}
