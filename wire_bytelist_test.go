package tricoredb

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The byte-array codec is hand-rolled for speed, so it gets its own round-trip
// and malformed-input coverage. A codec that silently truncates or masks is
// worse than a slow one.
func TestByteListRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0},
		{255},
		{0, 1, 127, 128, 254, 255},
		[]byte("hello, world"),
		bytes.Repeat([]byte{0xAB}, 4096),
	}
	for _, want := range cases {
		encoded, err := json.Marshal(byteList(want))
		if err != nil {
			t.Fatalf("marshal %d bytes: %v", len(want), err)
		}
		got, err := decodeByteList(encoded)
		if err != nil {
			t.Fatalf("decode %d bytes: %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round trip lost data: got %v want %v", got, want)
		}
	}
}

func TestByteListRejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		`[256]`,   // not a byte
		`[1,,2]`,  // empty element
		`[1,2,]`,  // trailing comma
		`[-1]`,    // negative
		`[1.5]`,   // not an integer
		`"aGk="`,  // base64, which is what encoding/json would have produced
		`{"a":1}`, // not an array
		`[abc]`,   // not digits
	} {
		if _, err := decodeByteList(json.RawMessage(bad)); err == nil {
			t.Fatalf("decodeByteList(%s) was accepted; it must be refused", bad)
		}
	}
}

func TestByteListNullIsAMiss(t *testing.T) {
	got, err := decodeByteList(json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("null: %v", err)
	}
	if got != nil {
		t.Fatalf("null must decode to a nil slice, got %v", got)
	}
}

// Empty and nil are different answers on this wire: "" is a stored empty value,
// null is a miss. The codec must not collapse them.
func TestByteListEmptyIsNotNil(t *testing.T) {
	got, err := decodeByteList(json.RawMessage(`[]`))
	if err != nil {
		t.Fatalf("[]: %v", err)
	}
	if got == nil {
		t.Fatal("[] must decode to an empty non-nil slice, not nil")
	}
	if len(got) != 0 {
		t.Fatalf("[] must decode to length 0, got %d", len(got))
	}
}
