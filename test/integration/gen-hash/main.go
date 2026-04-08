// gen-hash generates a random password + argon2id hash for test config,
// and outputs the SHA256 network hash (for login) and the argon2id hash (for config).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/tlalocweb/hulation/utils"
)

func main() {
	// Generate random password
	b := make([]byte, 16)
	rand.Read(b)
	password := hex.EncodeToString(b)

	networkHash := utils.GenerateHulaNetworkPassHash(password)
	argonHash, err := utils.Argon2GenerateFromSecretDefaults(networkHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}

	// Output: line 1 = network hash (for login), line 2 = argon2id hash (for config)
	fmt.Println(networkHash)
	fmt.Println(argonHash)
}
