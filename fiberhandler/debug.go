//go:build fiberhandlerdebug
// +build fiberhandlerdebug

package fiberhandler

import (
	"fmt"

	"github.com/tlalocweb/hulation/utils"
)

func fiberhandler_debugf(format string, args ...any) {
	fmt.Printf(fmt.Sprintf(utils.Grey("fiberhandler_debug: ")+"%s\n", format), args...)
}

func fiberhandler_attn_debugf(format string, args ...any) {
	fmt.Printf(fmt.Sprintf(utils.Red("fiberhandler_debug: !! ")+"%s\n", format), args...)
}
