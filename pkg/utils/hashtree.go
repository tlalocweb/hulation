package utils

import (
	"bytes"
	"encoding/gob"
	"github.com/cespare/xxhash/v2"
	iradix "github.com/hashicorp/go-immutable-radix/v2"
)

func (t *HashTree) Checksum() uint64 { //r *iradix.Tree[struct{}]) [32]byte {
	h := xxhash.New()
    it := t.tree.Root().Iterator()
    for key, _, ok := it.Next(); ok; key, _, ok = it.Next() {
        h.Write(key)
        h.Write([]byte{0})
    }
    return h.Sum64()
}

type HashTree struct {
    tree     *iradix.Tree[struct{}]
    checksum uint64
}

func NewHashTree() *HashTree {
    return &HashTree{
        tree:     iradix.New[struct{}](),
        checksum: 0,
    }
}

func (h *HashTree) Insert(key []byte) {
    h.tree, _, _ = h.tree.Insert(key, struct{}{})
    h.checksum ^= xxhash.Sum64(key) // XOR is order-independent
}

func (h *HashTree) Delete(key []byte) {
    h.tree, _, _ = h.tree.Delete(key)
    h.checksum ^= xxhash.Sum64(key) // XOR again removes it
}

func (h *HashTree) InsertString(key string) {
    h.Insert([]byte(key))
}

func (h *HashTree) DeleteString(key string) {
    h.Delete([]byte(key))
}

func (h *HashTree) FindByPrefix(prefix string) []string {
    matches := make([]string, 0)
    it := h.tree.Root().Iterator()
    it.SeekPrefix([]byte(prefix))
    for key, _, ok := it.Next(); ok; key, _, ok = it.Next() {
        matches = append(matches, string(key))
    }
    return matches
}

func (h *HashTree) HasPrefix(prefix string) bool {
    it := h.tree.Root().Iterator()
    it.SeekPrefix([]byte(prefix))
    _, _, ok := it.Next()
    return ok
}


// Serialize: dump the keys using gob encoding
func (h *HashTree) Serialize() []byte {
	var keys []string
	it := h.tree.Root().Iterator()
	for key, _, ok := it.Next(); ok; key, _, ok = it.Next() {
		keys = append(keys, string(key))
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(keys)
	return buf.Bytes()
}

// Deserialize: rebuild the tree from gob encoded data and recalculate checksum
func (h *HashTree) Deserialize(data []byte) error {
	var keys []string
	buf := bytes.NewReader(data)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&keys); err != nil {
		return err
	}

	r := iradix.New[struct{}]()
	h.checksum = 0
	for _, k := range keys {
		r, _, _ = r.Insert([]byte(k), struct{}{})
		h.checksum ^= xxhash.Sum64([]byte(k))
	}
	h.tree = r
	return nil
}

// Standalone Serialize function for backward compatibility
func Serialize(r *iradix.Tree[struct{}]) []byte {
	var keys []string
	it := r.Root().Iterator()
	for key, _, ok := it.Next(); ok; key, _, ok = it.Next() {
		keys = append(keys, string(key))
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(keys)
	return buf.Bytes()
}

// Standalone Deserialize function for backward compatibility
func Deserialize(data []byte) *iradix.Tree[struct{}] {
	var keys []string
	buf := bytes.NewReader(data)
	dec := gob.NewDecoder(buf)
	dec.Decode(&keys)

	r := iradix.New[struct{}]()
	for _, k := range keys {
		r, _, _ = r.Insert([]byte(k), struct{}{})
	}
	return r
}

// MatchWithWildcards checks if a key exists in the tree, considering wildcard patterns
// at each dot-separated segment. For example, given key "server.abc.forms.create",
// it will check for matches against:
//   - "*"
//   - "server.*"
//   - "server.abc.*"
//   - "server.abc.forms.*"
//   - "server.abc.forms.create" (exact match)
//
// Implementation uses upstream iradix primitives: a single Get() for the exact key,
// plus one Get() per dotted component for the wildcard prefixes. O(n) in the number
// of dot segments, which for permission strings is small (< 10 segments in practice).
func (h *HashTree) MatchWithWildcards(key string) bool {
	root := h.tree.Root()

	// Bare "*" matches anything.
	if _, ok := root.Get([]byte("*")); ok {
		return true
	}

	// Exact match.
	if _, ok := root.Get([]byte(key)); ok {
		return true
	}

	// Wildcard matches at each dot-segment boundary.
	// For key "a.b.c.d" check "a.*", "a.b.*", "a.b.c.*".
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			prefix := key[:i+1] + "*"
			if _, ok := root.Get([]byte(prefix)); ok {
				return true
			}
		}
	}

	return false
}
