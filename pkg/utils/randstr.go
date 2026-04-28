package utils

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	mrand "math/rand"
	"strings"
	"time"
)

// courtesy of https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-go

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

var src = mrand.NewSource(time.Now().UnixNano())

func FastRandString(n int) string {
	sb := strings.Builder{}
	sb.Grow(n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i, cache, remain := n-1, src.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			sb.WriteByte(letterBytes[idx])
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return sb.String()
}

// https://stackoverflow.com/questions/32349807/how-can-i-generate-a-random-int-using-the-crypto-rand-package

// GenerateRandomBytes returns securely generated random bytes.
// It will return an error if the system's secure random
// number generator fails to function correctly, in which
// case the caller should not continue.
func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return b, nil
}

// GenerateBase64RandomString returns a URL-safe, base64 encoded
// securely generated random string.
func GenerateBase64RandomString(s int) (string, error) {
	b, err := GenerateRandomBytes(s)
	return base64.URLEncoding.EncodeToString(b), err
}

func GenerateBase64RandomStringNoPadding(s int) (string, error) {
	b, err := GenerateRandomBytes(s)
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), err
	// b, err := GenerateRandomBytes(s)
	// if err != nil {
	// 	return "", err
	// }
	// var ret []byte
	// n := base64.URLEncoding.EncodedLen(len(b))
	// ret = make([]byte, n)
	// base64.URLEncoding.Encode(b, ret)
	// return string(ret[:n-1]), nil
}

// this is the alphabet used by bitcoin protocol - aka base58
const Base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// EncodeBytesToAlphabet encodes src (raw bytes) into a string using the given alphabet.
// The alphabet must have at least 2 characters.
func EncodeBytesToAlphabet(src []byte, alphabet string) string {
	base := len(alphabet)
	if base < 2 {
		panic("alphabet must have at least 2 characters")
	}

	// Special case: zero-length input
	if len(src) == 0 {
		return ""
	}

	// Convert src bytes into a big integer
	num := new(big.Int).SetBytes(src)

	// Build the encoded result in reverse order
	var result []byte
	for num.Sign() > 0 {
		// remainder = num % base
		remainder := new(big.Int)
		remainder.Mod(num, big.NewInt(int64(base)))

		// Prepend the corresponding character
		r := remainder.Int64()
		result = append(result, alphabet[r])

		// num = num / base
		num.Div(num, big.NewInt(int64(base)))
	}

	// Reverse result (because we collected in LIFO order)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

// DecodeStringFromAlphabet is the reverse operation, for completeness:
// Decodes the given string s (in the custom base) back into raw bytes.
//
// Returns nil + error if the string contains invalid characters.
func DecodeStringFromAlphabet(s, alphabet string) ([]byte, error) {
	base := len(alphabet)
	if base < 2 {
		return nil, fmt.Errorf("alphabet must have at least 2 characters")
	}

	// Map each character to its value
	charIndex := make(map[rune]int, len(alphabet))
	for i, r := range alphabet {
		charIndex[r] = i
	}

	num := big.NewInt(0)
	for _, r := range s {
		val, ok := charIndex[r]
		if !ok {
			return nil, fmt.Errorf("invalid character %q in input string", r)
		}
		num.Mul(num, big.NewInt(int64(base)))
		num.Add(num, big.NewInt(int64(val)))
	}

	// Convert the big.Int back to bytes
	return num.Bytes(), nil
}

// RFC 1123 naming requirements for Kubernetes resources:
// - Must be no longer than 63 characters
// - Must start and end with lowercase alphanumeric characters ([a-z0-9])
// - May contain lowercase letters, numbers, and hyphens in the middle
// - Follows regex pattern: [a-z0-9]([-a-z0-9]*[a-z0-9])?
const rfc1123AlphanumericChars = "abcdefghijklmnopqrstuvwxyz0123456789"
const rfc1123AllowedChars = "abcdefghijklmnopqrstuvwxyz0123456789-"

// GenerateKubernetesCompliantString generates a random string that follows RFC 1123 naming conventions
// as required by Kubernetes resources. The string will:
// - Be between 1 and 63 characters long
// - Start and end with lowercase alphanumeric characters ([a-z0-9])
// - Contain only lowercase letters, numbers, and hyphens
// - Follow the regex pattern: [a-z0-9]([-a-z0-9]*[a-z0-9])?
func GenerateKubernetesCompliantString(n int) (string, error) {
	if n < 1 || n > 63 {
		return "", fmt.Errorf("length must be between 1 and 63 characters (RFC 1123 requirement), got %d", n)
	}

	if n == 1 {
		// Single character, must be alphanumeric
		b, err := GenerateRandomBytes(1)
		if err != nil {
			return "", err
		}
		idx := int(b[0]) % len(rfc1123AlphanumericChars)
		return string(rfc1123AlphanumericChars[idx]), nil
	}

	var result strings.Builder
	result.Grow(n)

	// First character must be alphanumeric ([a-z0-9])
	b, err := GenerateRandomBytes(1)
	if err != nil {
		return "", err
	}
	idx := int(b[0]) % len(rfc1123AlphanumericChars)
	result.WriteByte(rfc1123AlphanumericChars[idx])

	// Middle characters can include hyphens ([a-z0-9-])
	if n > 2 {
		middleBytes, err := GenerateRandomBytes(n - 2)
		if err != nil {
			return "", err
		}
		for _, b := range middleBytes {
			idx := int(b) % len(rfc1123AllowedChars)
			result.WriteByte(rfc1123AllowedChars[idx])
		}
	}

	// Last character must be alphanumeric ([a-z0-9])
	b, err = GenerateRandomBytes(1)
	if err != nil {
		return "", err
	}
	idx = int(b[0]) % len(rfc1123AlphanumericChars)
	result.WriteByte(rfc1123AlphanumericChars[idx])

	return result.String(), nil
}
