package hooks

// type HookChain struct {
// 	risorHooks []string
// }

// func Execute(hook *RisorHook) (err error) {
// 	exec := GetRisorExecutor()
// 	_, err = exec.Compile(hook)
// 	if err != nil {
// 		return
// 	}
// }

func CompileVisitorHook(globals map[string]any, name string, code string) (templatehook *RisorHook, err error) {
	exec := GetRisorExecutor()
	templatehook = &RisorHook{
		Name:    name,
		Script:  code,
		Globals: globals,
	}
	var compiled *CompiledCode
	compiled, err = exec.Compile(templatehook)
	templatehook.compiled = compiled
	return
}
func ExecuteVisitorHook(globals map[string]any, templ *RisorHook, onOk OnCompleteHookFunc, onErr OnErrHookFunc) {
	exec := GetRisorExecutor()
	rethook := &RisorHook{
		Name:       templ.Name,
		Script:     templ.Script,
		compiled:   templ.compiled,
		Globals:    globals,
		OnErr:      onErr,
		OnComplete: onOk,
	}
	exec.SubmitForExec(rethook, globals)
}
