package utils

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"crypto/sha256"
	"fmt"

	"github.com/tlalocweb/argon2id"
	"github.com/tlalocweb/hulation/log"
)

const reStrGetURLPieces = `(http[s]?)\:\/\/([^\/:]+)(?:\:([0-9]+))?(.*)`

var reGetURLPieces *regexp.Regexp

func GetURLPieces(url string) (proto string, host string, port int64, path string) {
	var portStr string
	if reGetURLPieces == nil {
		reGetURLPieces = regexp.MustCompile(reStrGetURLPieces)
	}
	res := reGetURLPieces.FindAllStringSubmatch(url, -1)
	if len(res) > 0 && len(res[0]) > 1 {
		proto = res[0][1]
		host = res[0][2]
		if len(res[0]) > 3 {
			portStr = res[0][3]
			path = res[0][4]
		}
	}
	if len(portStr) > 0 {
		port, _ = strconv.ParseInt(portStr, 10, 0)
	} else {
		port = 80
		if proto == "https" {
			port = 443
		}
	}
	return
}

// SqlStr replace ^ or ” with `
func SqlStr(s string) (ret string) {
	ret = strings.ReplaceAll(s, "^", "`")
	ret = strings.ReplaceAll(ret, "”", "`")
	return
}

func CleanShutdown(exitval int) {
	os.Stderr.Sync()
	os.Stdout.Sync()
	os.Exit(exitval)
}

func GetHostOnly(host string) string {
	parts := strings.Split(host, ":")
	return parts[0]
}

var replaceExecPathRE = regexp.MustCompile(`(?m){{\s*huladir\s*}}`)

func ReadFileFromConfigPath(confpath string, filename string) (ret []byte, err error) {
	// process path
	mypath, err := os.Executable()
	if err != nil {
		return
	}
	mydir := filepath.Dir(mypath)
	//	fpath := strings.ReplaceAll(confpath, "{{huladir}}", mydir)
	fpath := replaceExecPathRE.ReplaceAllString(confpath, mydir)
	final := filepath.Join(fpath, filename)
	//	f, err := os.Open(final)
	ret, err = os.ReadFile(final)
	if err != nil {
		err = fmt.Errorf("error opening file %s: %s", final, err.Error())
		return
	}
	//	defer f.Close()

	return
}

const (
	restrURLPathAlt = `(?:http[s]?\:\/\/)?([^\/]+)(.*)`
)

var reURLPathAlt *regexp.Regexp

func init() {
	reURLPathAlt = regexp.MustCompile(restrURLPathAlt)
}

func GetURLHostPath(path string) (host string, urlpath string) {
	res := reURLPathAlt.FindAllStringSubmatch(path, -1)
	if len(res) > 0 && len(res[0]) > 1 {
		host = res[0][1]
		if len(res[0]) > 2 {
			urlpath = res[0][2]
			// trim the last char of the string if it is a /
			if len(urlpath) > 0 && urlpath[len(urlpath)-1] == '/' {
				urlpath = urlpath[:len(urlpath)-1]
			}
		}
	}
	return
}

// func main() {
// 	// Pass the plaintext password and parameters to our generateFromPassword
// 	// helper function.
// 	hash, err := generateFromPassword("password123", p)
// 	if err != nil {
// 		log.Fatal(err)
// 	}

// 	fmt.Println(hash)
// }

// Argon2 is the password hash (key derivation) algorithm we use to store password hasesh.
// ...Establish the parameters to use for Argon2.
// See: https://www.alexedwards.net/blog/how-to-hash-and-verify-passwords-with-argon2-in-go
// Author's default below.
// we don't want to eat this much memory for a password hasher
// so we increase the iterations
// p := &params{
// 	memory:      64 * 1024,
// 	iterations:  3,
// 	parallelism: 2,
// 	saltLength:  16,
// 	keyLength:   32,
// }

var defaultArgon2Params = &argon2id.Params{
	Memory:      16 * 1024,
	Iterations:  12,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   48,
}

// Argon2GenerateFromPasswordDefaults is a helper function that uses the default parameters as determined in utils/other.go
func Argon2GenerateFromSecretDefaults(password string) (encodedHash string, err error) {
	return Argon2GenerateFromSecret(password, defaultArgon2Params)
}

// Generagtes an Argon2 hash (key) from a password/secret and parameters.
// See: https://www.alexedwards.net/blog/how-to-hash-and-verify-passwords-with-argon2-in-go
func Argon2GenerateFromSecret(password string, p *argon2id.Params) (hash string, err error) {
	hash, err = argon2id.CreateHash(password, defaultArgon2Params)
	return
}

func Argon2CompareHashAndSecret(secret, hash string) (match bool, err error) {
	match, err = argon2id.ComparePasswordAndHash(secret, hash)
	return
}

// this is a function which hashes a password for use with /login
// with Hula. It is a sha256 hash of the password.
func GenerateHulaNetworkPassHash(password string) (hash string) {
	sum := sha256.Sum256([]byte(password))
	hash = fmt.Sprintf("%x", sum)
	return
}

// This generate the hash we store in the database. It is built from the sha256 hash of the password
// which is sent to use with auth API. The user should never send their actual password
func GenerateHulaHashFromPlaintextPass(password string) (argonhash string, stringsum string, err error) {
	// we never actually should keep or know the real password - so first we hash it using sha256
	stringsum = GenerateHulaNetworkPassHash(password)
	fmt.Printf("sha256: %s\n", stringsum)
	// then we hash it using argon2
	argonhash, err = Argon2GenerateFromSecretDefaults(stringsum)
	return
}

func CamelCase(s string) string {
	// Courtesy of https://stackoverflow.com/questions/70083837/how-to-convert-a-string-to-camelcase-in-go
	// Remove all characters that are not alphanumeric or spaces or underscores
	s = regexp.MustCompile("[^a-zA-Z0-9_ ]+").ReplaceAllString(s, "")
	// Replace all underscores with spaces
	s = strings.ReplaceAll(s, "_", " ")
	// Title case s
	s = cases.Title(language.AmericanEnglish, cases.NoLower).String(s)
	// Remove all spaces
	s = strings.ReplaceAll(s, " ", "")
	// Lowercase the first letter
	if len(s) > 0 {
		s = strings.ToLower(s[:1]) + s[1:]
	}
	return s
}

func JsonifyStr(i string) string {
	b, err := json.Marshal(i)
	if err != nil {
		log.Errorf("jsonEscape: error marshalling string: %s", err.Error())
	}
	// DONT Trim the beginning and trailing " character
	return string(b) //string(b[1 : len(b)-1])
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil
}

func GetJustHost(host string) string {
	parts := strings.Split(host, ":")
	return parts[0]
}
