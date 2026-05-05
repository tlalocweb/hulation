//go:build ignore
// +build ignore

// gen-hash-from-password reads PASSWORD from the environment and prints
// the argon2id hash of the plaintext on stdout. Used by
// test/e2e/lib/setup.sh to populate the (now-vestigial) admin.hash
// field in test hula config — actual auth runs through OPAQUE PAKE.

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
	argonHash, err := utils.Argon2GenerateFromSecretDefaults(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	fmt.Println(argonHash)
}
