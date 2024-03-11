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

func CompileVisitorHook(globals map[string]any, name string, code string) (err error) {
	exec := GetRisorExecutor()
	rethook := &RisorHook{
		Name:    name,
		Script:  code,
		Globals: globals,
	}
	_, err = exec.Compile(rethook)
	return
}
func ExecuteVisitorHook(globals map[string]any, name string, code string, onOk OnCompleteHookFunc, onErr OnErrHookFunc) {
	exec := GetRisorExecutor()
	rethook := &RisorHook{
		Name:       name,
		Script:     code,
		Globals:    globals,
		OnErr:      onErr,
		OnComplete: onOk,
	}
	exec.SubmitForExec(rethook, globals)
}
