package config

import (
	"os"
	"path/filepath"
	"reflect"
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

// SubstConfVarsForAllStrings recursively walks the given struct and applies
// {{env:VAR}}, {{huladir}}, etc. substitution to every string field.
// This ensures any string in the config can reference environment variables
// without needing per-field substitution calls.
func SubstConfVarsForAllStrings(i interface{}, locals map[string]string) {
	walkAndSubst(reflect.ValueOf(i), locals)
}

func walkAndSubst(val reflect.Value, locals map[string]string) {
	for val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return
		}
		val = val.Elem()
	}

	switch val.Kind() {
	case reflect.Struct:
		for i := 0; i < val.NumField(); i++ {
			field := val.Field(i)
			if !field.CanSet() {
				continue
			}
			walkAndSubst(field, locals)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < val.Len(); i++ {
			walkAndSubst(val.Index(i), locals)
		}
	case reflect.Map:
		for _, key := range val.MapKeys() {
			elem := val.MapIndex(key)
			if elem.Kind() == reflect.String {
				substed, err := SubstConfVars(elem.String(), locals)
				if err == nil && substed != elem.String() {
					val.SetMapIndex(key, reflect.ValueOf(substed))
				}
			}
		}
	case reflect.String:
		if val.CanSet() {
			original := val.String()
			if strings.Contains(original, "{{") {
				substed, err := SubstConfVars(original, locals)
				if err == nil {
					val.SetString(substed)
				} else {
					log.Errorf("Error substituting conf vars in string %q: %v", original, err)
				}
			}
		}
	}
}
