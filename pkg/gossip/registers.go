package gossip

// CRDT register types used by the gossip layer (HA_PLAN3 §7).
//
//   LWWRecord       — Last-Write-Wins keyed by HLC. Used for visitor
//                     records + session continuity. Carries an
//                     optional tombstone for GDPR forget.
//   MonotoneRecord  — "bad wins" merge for bad-actor flags: once any
//                     replica decides BAD, the value stays BAD until
//                     a Raft-side clear (handled at the read layer,
//                     not in the merge function).
//
// Each is gob-encoded into the on-disk + on-the-wire payload. The
// HLC also rides on the Delta envelope for staleness comparison
// without having to decode the payload.

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
)

const (
	registerKindLWW      uint8 = 1
	registerKindMonotone uint8 = 2
)

// LWWRecord is a payload for the visitor / session buckets.
type LWWRecord struct {
	HLC       HLC
	Payload   []byte // opaque caller-provided bytes
	Tombstone bool   // true = GDPR-forget marker; merges win on equal HLC
}

// MonotoneRecord is a payload for the bad-actor flag bucket. Once
// any replica produces an LWWFlagged=true, the merge keeps it true
// regardless of HLC; admin-side clears go through Raft (HA_PLAN3
// §7.5) and the read-time check overrides this register.
type MonotoneRecord struct {
	HLC      HLC
	Flagged  bool
	Reason   string // optional operator-visible reason
	ByNodeID string // who first flagged
}

// MergeLWW returns the winner of LWW(a, b). Higher HLC wins;
// tombstone wins on equal HLC (deletion is sticky).
func MergeLWW(a, b LWWRecord) LWWRecord {
	if a.HLC.After(b.HLC) {
		return a
	}
	if b.HLC.After(a.HLC) {
		return b
	}
	if a.HLC.Equal(b.HLC) {
		// Same writer-emitted HLC ⇒ same payload (programmer error
		// if not; we pick a deterministically by tombstone-first).
		if a.Tombstone {
			return a
		}
		return b
	}
	if a.Tombstone {
		return a
	}
	return b
}

// MergeMonotone returns the merge of a and b under the "bad wins"
// rule. The HLC of the result is max(a.HLC, b.HLC) so future
// staleness comparisons work; Flagged stays true if either side
// flagged.
func MergeMonotone(a, b MonotoneRecord) MonotoneRecord {
	out := a
	if b.HLC.After(a.HLC) {
		out = b
	}
	if a.Flagged || b.Flagged {
		out.Flagged = true
		if a.Flagged && a.Reason != "" && out.Reason == "" {
			out.Reason = a.Reason
			out.ByNodeID = a.ByNodeID
		} else if b.Flagged && b.Reason != "" && out.Reason == "" {
			out.Reason = b.Reason
			out.ByNodeID = b.ByNodeID
		}
	}
	return out
}

// EncodeLWW serialises an LWWRecord as `[kind(1)] [gob-encoded body]`.
func EncodeLWW(r LWWRecord) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(registerKindLWW)
	if err := gob.NewEncoder(&buf).Encode(r); err != nil {
		return nil, fmt.Errorf("encode lww: %w", err)
	}
	return buf.Bytes(), nil
}

// EncodeMonotone serialises a MonotoneRecord.
func EncodeMonotone(r MonotoneRecord) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(registerKindMonotone)
	if err := gob.NewEncoder(&buf).Encode(r); err != nil {
		return nil, fmt.Errorf("encode monotone: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeLWW returns the contained LWW record. Errors when the byte
// stream's kind tag doesn't match.
func DecodeLWW(b []byte) (LWWRecord, error) {
	if len(b) == 0 {
		return LWWRecord{}, errors.New("empty payload")
	}
	if b[0] != registerKindLWW {
		return LWWRecord{}, fmt.Errorf("kind tag %d ≠ LWW(%d)", b[0], registerKindLWW)
	}
	var r LWWRecord
	if err := gob.NewDecoder(bytes.NewReader(b[1:])).Decode(&r); err != nil {
		return LWWRecord{}, fmt.Errorf("decode lww: %w", err)
	}
	return r, nil
}

// DecodeMonotone returns the contained monotone-flag record.
func DecodeMonotone(b []byte) (MonotoneRecord, error) {
	if len(b) == 0 {
		return MonotoneRecord{}, errors.New("empty payload")
	}
	if b[0] != registerKindMonotone {
		return MonotoneRecord{}, fmt.Errorf("kind tag %d ≠ Monotone(%d)", b[0], registerKindMonotone)
	}
	var r MonotoneRecord
	if err := gob.NewDecoder(bytes.NewReader(b[1:])).Decode(&r); err != nil {
		return MonotoneRecord{}, fmt.Errorf("decode monotone: %w", err)
	}
	return r, nil
}

// PayloadKind reports the encoded register kind without decoding
// the body. Useful when iterating a bucket where types may differ.
func PayloadKind(b []byte) (uint8, error) {
	if len(b) == 0 {
		return 0, errors.New("empty payload")
	}
	return b[0], nil
}

// MergeEncoded merges two opaque encoded payloads of the same kind.
// Used by the gossip merge path so callers don't need to know the
// concrete register type up front.
func MergeEncoded(a, b []byte) ([]byte, error) {
	if len(a) == 0 {
		return b, nil
	}
	if len(b) == 0 {
		return a, nil
	}
	if a[0] != b[0] {
		return nil, fmt.Errorf("kind mismatch: a=%d b=%d", a[0], b[0])
	}
	switch a[0] {
	case registerKindLWW:
		ar, err := DecodeLWW(a)
		if err != nil {
			return nil, err
		}
		br, err := DecodeLWW(b)
		if err != nil {
			return nil, err
		}
		return EncodeLWW(MergeLWW(ar, br))
	case registerKindMonotone:
		ar, err := DecodeMonotone(a)
		if err != nil {
			return nil, err
		}
		br, err := DecodeMonotone(b)
		if err != nil {
			return nil, err
		}
		return EncodeMonotone(MergeMonotone(ar, br))
	default:
		return nil, fmt.Errorf("unknown payload kind %d", a[0])
	}
}

// HLCFromPayload extracts the HLC from any encoded register
// without decoding the rest of the body. Returned HLC is zero
// when the payload is empty / corrupt; caller decides how to
// react.
func HLCFromPayload(b []byte) (HLC, error) {
	if len(b) == 0 {
		return HLC{}, nil
	}
	switch b[0] {
	case registerKindLWW:
		r, err := DecodeLWW(b)
		if err != nil {
			return HLC{}, err
		}
		return r.HLC, nil
	case registerKindMonotone:
		r, err := DecodeMonotone(b)
		if err != nil {
			return HLC{}, err
		}
		return r.HLC, nil
	default:
		return HLC{}, fmt.Errorf("unknown payload kind %d", b[0])
	}
}
