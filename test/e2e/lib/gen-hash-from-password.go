//go:build ignore
// +build ignore

// gen-hash-from-password reads PASSWORD from the environment, computes the
// network hash (SHA256) and the argon2id hash of that network hash, and
// prints them on two separate stdout lines:
//
//   line 1: network hash (for hulactl auth login payload)
//   line 2: argon2id hash (for hula admin.hash config)
//
// Used by test/e2e/lib/setup.sh.

package main

import (
	"fmt"
	"os"

	"github.com/tlalocweb/hulation/utils"
)

func main() {
	password := os.Getenv("PASSWORD")
	if password == "" {
		fmt.Fprintln(os.Stderr, "PASSWORD environment variable not set")
		os.Exit(1)
	}
	networkHash := utils.GenerateHulaNetworkPassHash(password)
	argonHash, err := utils.Argon2GenerateFromSecretDefaults(networkHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	fmt.Println(networkHash)
	fmt.Println(argonHash)
}
