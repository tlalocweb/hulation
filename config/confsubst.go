package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/tlalocweb/hulation/log"
)

var globalConfVars map[string]string

func init() {
	globalConfVars = make(map[string]string)
}

func setupGlobalConfVars() {
	hulapath, _ := os.Executable()
	huladir := filepath.Dir(hulapath)

	globalConfVars["hulaversion"] = Version
	globalConfVars["huladir"] = huladir
	// set all env vars in the map
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		globalConfVars["env:"+pair[0]] = pair[1]
	}
}

func SubstConfVars(conf string, locals map[string]string) (ret string, err error) {
	if len(globalConfVars) == 0 {
		setupGlobalConfVars()
	}
	// merge the two maps
	confmap := make(map[string]string)
	for k, v := range globalConfVars {
		confmap[k] = v
	}
	for k, v := range locals {
		confmap[k] = v
	}

	tmpl, err := mustache.ParseStringRaw(conf, true)
	if err != nil {
		return
	}

	ret, err = tmpl.Render(confmap)
	if err != nil {
		return
	}

	return
}

func SubstConfVarsLogErrorf(conf string, locals map[string]string, msg string) string {
	ret, err := SubstConfVars(conf, locals)
	if err != nil {
		log.Errorf("Error substituting conf vars - %s: %v", msg, err)
	}
	return ret
}
