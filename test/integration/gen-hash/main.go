// gen-hash generates a random password and the corresponding argon2id
// hash for test config. Outputs:
//
//	line 1: plaintext password (for hulactl set-password / OPAQUE register)
//	line 2: argon2id hash (for hula admin.hash config — vestigial now
//	        that OPAQUE owns admin auth, but still required for config
//	        shape compat in some test fixtures)
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/tlalocweb/hulation/utils"
)

func main() {
	b := make([]byte, 16)
	rand.Read(b)
	password := hex.EncodeToString(b)

	argonHash, err := utils.Argon2GenerateFromSecretDefaults(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}

	fmt.Println(password)
	fmt.Println(argonHash)
}
