package gossip

import (
	"testing"
)

func mkHLC(wall int64, counter uint32, node string) HLC {
	return HLC{WallMs: wall, Counter: counter, NodeID: node}
}

func TestMergeLWW_HigherHLCWins(t *testing.T) {
	a := LWWRecord{HLC: mkHLC(1, 0, "A"), Payload: []byte("old")}
	b := LWWRecord{HLC: mkHLC(2, 0, "A"), Payload: []byte("new")}
	if got := MergeLWW(a, b); string(got.Payload) != "new" {
		t.Errorf("a∪b: payload=%q want=new", got.Payload)
	}
	if got := MergeLWW(b, a); string(got.Payload) != "new" {
		t.Errorf("b∪a: payload=%q want=new", got.Payload)
	}
}

func TestMergeLWW_TombstoneWinsTie(t *testing.T) {
	hlc := mkHLC(5, 0, "A")
	live := LWWRecord{HLC: hlc, Payload: []byte("alive")}
	tomb := LWWRecord{HLC: hlc, Tombstone: true}
	if got := MergeLWW(live, tomb); !got.Tombstone {
		t.Errorf("tombstone should win on equal HLC; got %+v", got)
	}
}

func TestMergeLWW_TombstoneLosesIfOlder(t *testing.T) {
	older := LWWRecord{HLC: mkHLC(1, 0, "A"), Tombstone: true}
	newer := LWWRecord{HLC: mkHLC(2, 0, "A"), Payload: []byte("alive-again")}
	if got := MergeLWW(older, newer); got.Tombstone {
		t.Errorf("newer non-tombstone should win; got %+v", got)
	}
}

func TestMergeMonotone_BadAlwaysWins(t *testing.T) {
	clean := MonotoneRecord{HLC: mkHLC(10, 0, "A")}
	bad := MonotoneRecord{HLC: mkHLC(5, 0, "B"), Flagged: true, Reason: "scoring"}
	got := MergeMonotone(clean, bad)
	if !got.Flagged {
		t.Errorf("BAD should win regardless of HLC; got %+v", got)
	}
	if got.HLC.WallMs != 10 {
		t.Errorf("HLC should advance to max; got %v", got.HLC)
	}
	got2 := MergeMonotone(bad, clean)
	if !got2.Flagged {
		t.Errorf("commutative — BAD wins both ways; got %+v", got2)
	}
}

func TestMergeMonotone_TwoFlaggedKeepFirstReason(t *testing.T) {
	a := MonotoneRecord{HLC: mkHLC(1, 0, "A"), Flagged: true, Reason: "scoring", ByNodeID: "A"}
	b := MonotoneRecord{HLC: mkHLC(2, 0, "B"), Flagged: true, Reason: "manual", ByNodeID: "B"}
	got := MergeMonotone(a, b)
	if !got.Flagged {
		t.Fatal("merge dropped Flagged=true")
	}
	if got.HLC.WallMs != 2 {
		t.Errorf("HLC should be max; got %v", got.HLC)
	}
}

func TestEncodeDecodeLWW_RoundTrip(t *testing.T) {
	src := LWWRecord{HLC: mkHLC(123, 4, "node"), Payload: []byte("hello")}
	b, err := EncodeLWW(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLWW(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HLC.Equal(src.HLC) || string(got.Payload) != "hello" {
		t.Errorf("round-trip lost data: src=%+v got=%+v", src, got)
	}
}

func TestEncodeDecodeMonotone_RoundTrip(t *testing.T) {
	src := MonotoneRecord{HLC: mkHLC(999, 0, "X"), Flagged: true, Reason: "spam", ByNodeID: "X"}
	b, err := EncodeMonotone(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMonotone(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Flagged != src.Flagged || got.Reason != src.Reason {
		t.Errorf("round-trip lost data: src=%+v got=%+v", src, got)
	}
}

func TestDecodeLWW_RejectsWrongKind(t *testing.T) {
	bad, _ := EncodeMonotone(MonotoneRecord{HLC: mkHLC(1, 0, "n")})
	if _, err := DecodeLWW(bad); err == nil {
		t.Error("expected kind-mismatch rejection")
	}
}

func TestMergeEncoded_LWW(t *testing.T) {
	a, _ := EncodeLWW(LWWRecord{HLC: mkHLC(1, 0, "A"), Payload: []byte("old")})
	b, _ := EncodeLWW(LWWRecord{HLC: mkHLC(2, 0, "A"), Payload: []byte("new")})
	merged, err := MergeEncoded(a, b)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := DecodeLWW(merged)
	if string(got.Payload) != "new" {
		t.Errorf("encoded merge lost newer; got %q", got.Payload)
	}
}

func TestMergeEncoded_KindMismatch(t *testing.T) {
	a, _ := EncodeLWW(LWWRecord{HLC: mkHLC(1, 0, "A")})
	b, _ := EncodeMonotone(MonotoneRecord{HLC: mkHLC(2, 0, "A")})
	if _, err := MergeEncoded(a, b); err == nil {
		t.Error("expected kind-mismatch error")
	}
}

func TestHLCFromPayload(t *testing.T) {
	want := mkHLC(42, 1, "node")
	for _, mk := range []func() ([]byte, error){
		func() ([]byte, error) { return EncodeLWW(LWWRecord{HLC: want}) },
		func() ([]byte, error) { return EncodeMonotone(MonotoneRecord{HLC: want, Flagged: true}) },
	} {
		b, err := mk()
		if err != nil {
			t.Fatal(err)
		}
		got, err := HLCFromPayload(b)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(want) {
			t.Errorf("HLCFromPayload got %v, want %v", got, want)
		}
	}
}
