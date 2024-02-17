//go:build model_debug
// +build model_debug

package model

import (
	"fmt"

	"github.com/tlalocweb/hulation/utils"
)

func model_debugf(format string, args ...any) {
	fmt.Printf(fmt.Sprintf(utils.Grey("model_debug: ")+"%s\n", format), args...)
}

func model_attn_debugf(format string, args ...any) {
	fmt.Printf(fmt.Sprintf(utils.Red("model_debug: !! ")+"%s\n", format), args...)
}
