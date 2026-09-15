package tricoredb

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Wire tags — see docs/protocol/WIRE_REFERENCE.md. Note the asymmetry: HELLO
// is answered by HELLO_OK (8), not HELLO (0), and AUTH by AUTH_OK (9). A
// client that waits for the tag it sent will hang forever.
const (
	tagHello    = 0
	tagAuth     = 1
	tagRequest  = 2
	tagResponse = 3
	tagPing     = 4
	tagPong     = 5
	tagError    = 6
	tagClose    = 7
	tagHelloOK  = 8
	tagAuthOK   = 9
	tagBye      = 10
	tagCancel   = 11
	tagCancelOK = 12
)

// Version is this SDK's own version, so a consumer can pin one (V2 P2).
//
// Go resolves module versions from git tags, not from source, so a consumer of
// an untagged checkout previously had nothing to state at all. This constant is
// the SDK's version and is deliberately separate from protocolVersion: the two
// move for different reasons.
const Version = "0.1.0"

// Optional protocol capabilities this SDK understands, sent in HELLO as a
// bitmap (V2 P2). The server replies with the subset it granted; a server too
// old to negotiate omits the field, which reads as 0.
const (
	// FeatureCorrelationID: this SDK understands a request-scoped correlation
	// id that the server joins to its access log, audit trail and cancel
	// registry.
	FeatureCorrelationID uint64 = 1 << 0

	// FeatureServerParams: the server binds `?` placeholders to a typed params
	// array carried alongside the statement, instead of the driver rendering
	// values into the SQL text before sending it.
	//
	// This is the difference between escaping and binding. Client-side
	// rendering has to reproduce the server's literal syntax exactly for every
	// type, and any mismatch is either a wrong value or a parse error; a bound
	// parameter is substituted at a value position the grammar has already
	// fixed, so a value can never become syntax however it is spelled.
	//
	// [Client.ExecuteParams] and [Client.QueryParams] require it and refuse by
	// name when it was not granted, rather than falling back to the rendering
	// they used to do — a silent downgrade from binding to escaping is exactly
	// the failure this bit exists to make visible.
	FeatureServerParams uint64 = 1 << 1

	// FeatureSessionTxn: session-scoped transactions — BEGIN, the statements,
	// and COMMIT/ROLLBACK sent as separate requests on one connection, with a
	// real rollback boundary between them.
	//
	// Granted per connection: a sharded or forwarding node may withhold it, and
	// a server too old to negotiate never grants it. Client.Begin refuses by
	// name when it was not granted, rather than sending a BEGIN the server would
	// run as a one-statement autocommit script.
	FeatureSessionTxn uint64 = 1 << 2

	// Features is everything this build understands.
	Features = FeatureCorrelationID | FeatureServerParams | FeatureSessionTxn
)

const (
	protocolName    = "tricore"
	protocolVersion = 1

	// headerSize is [version u8][tag u8][payload_len u32 BE].
	headerSize = 6
)

// The protocol's payload ceilings, mirroring
// crates/tricore_protocol/src/constants/mod.rs.
//
// A driver that trusts a declared length is six header bytes away from an
// unbounded allocation: payload_len is a u32, so any peer — hostile, broken, or
// simply the wrong port — could commit this process to a 4 GiB make([]byte, n)
// and a read that never ends. The server refuses these lengths on its side; the
// client must refuse them on its own, because the server is not the only thing
// this socket can be connected to.
//
// Control frames take the far tighter ceiling (C-40): every frame that is not
// REQUEST/RESPONSE carries a small JSON document or nothing at all, and the two
// an *unauthenticated* peer may send are both in that set.
const (
	MaxFrameSize        uint32 = 16 * 1024 * 1024
	MaxControlFrameSize uint32 = 64 * 1024
)

// MaxSupportedFrameVersion is the highest frame-header format version this
// driver can read.
//
// The header's first byte is a format version. It used to be skipped entirely
// here, so a peer speaking a future frame layout was parsed as though it spoke
// this one. The server refuses an unknown version by name; so does this.
const MaxSupportedFrameVersion byte = 1

// maxPayloadFor is the ceiling for one tag. An *unknown* tag takes the tighter
// ceiling deliberately: a tag this build cannot name is a tag whose payload
// size it cannot vouch for, and the safe direction to be wrong in is "too
// small".
func maxPayloadFor(tag byte) uint32 {
	if tag == tagRequest || tag == tagResponse {
		return MaxFrameSize
	}
	return MaxControlFrameSize
}

// frame is one wire message: a tag plus its (already length-delimited)
// payload. Empty payloads (e.g. PING/PONG) carry a nil Body.
type frame struct {
	tag  byte
	body []byte
}

// writeFrame encodes and writes a single frame. payload may be nil, which
// produces a zero-length body (as PING/CLOSE do on the wire).
func writeFrame(w io.Writer, tag byte, payload any) error {
	var body []byte
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("tricoredb: encode frame: %w", err)
		}
		body = b
	}
	// Mirrors tricore_protocol::write_frame. A control frame over the ceiling is
	// refused locally and named; an oversized REQUEST is still written, so the
	// server's own `frame_too_large` stays the diagnosis for the case an
	// operator will actually meet.
	if tag != tagRequest && uint32(len(body)) > MaxControlFrameSize {
		return &ProtocolError{Message: fmt.Sprintf(
			"a %d-byte payload for tag %d exceeds the protocol's ceiling for control frames at %d bytes",
			len(body), tag, MaxControlFrameSize)}
	}
	header := make([]byte, headerSize)
	header[0] = protocolVersion
	header[1] = tag
	binary.BigEndian.PutUint32(header[2:], uint32(len(body)))
	if _, err := w.Write(append(header, body...)); err != nil {
		return err
	}
	return nil
}

// readFrame reads exactly one frame: the fixed 6-byte header, then exactly
// payload_len bytes of body.
//
// TCP delivers a byte stream, not message boundaries: a single Read can
// return less than a full frame (or, buffered, more than one). io.ReadFull
// loops internally until it has read len(buf) bytes or hit an error, which
// is the only way to be correct once a payload — such as the 100KB cache
// value this driver is tested against — spans more than one TCP segment. A
// naive single conn.Read(buf) passes every local test (loopback rarely
// fragments a small frame) and then corrupts data in production.
func readFrame(r io.Reader) (frame, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return frame{}, &ProtocolError{Message: "connection closed mid-frame by the server"}
		}
		return frame{}, err
	}
	if version := header[0]; version > MaxSupportedFrameVersion {
		return frame{}, &ProtocolError{Message: fmt.Sprintf(
			"frame header version %d is newer than this driver can read (max %d)",
			version, MaxSupportedFrameVersion)}
	}
	tag := header[1]
	length := binary.BigEndian.Uint32(header[2:])
	// Checked BEFORE a payload byte is asked for, and before anything is
	// allocated for it.
	if limit := maxPayloadFor(tag); length > limit {
		return frame{}, &ProtocolError{Message: fmt.Sprintf(
			"frame payload of %d bytes for tag %d exceeds the protocol ceiling of %d bytes",
			length, tag, limit)}
	}
	if length == 0 {
		return frame{tag: tag}, nil
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return frame{}, &ProtocolError{Message: "connection closed mid-frame by the server"}
		}
		return frame{}, err
	}
	return frame{tag: tag, body: body}, nil
}

// byteList encodes a []byte as a plain JSON array of numbers (e.g.
// [119,111,114,108,100]), matching what the server expects for AUTH.secret
// and Cache.Set.value. This is deliberate: encoding/json's default
// marshaling of []byte produces a base64 *string*, which is not what the
// wire spec describes and which the server does not accept here.
type byteList []byte

// MarshalJSON writes the array directly rather than building an []int and
// handing it to encoding/json.
//
// The obvious implementation allocates a second slice the size of the payload
// and then pays reflection per element. At 64-byte values nobody notices; at
// 4 KiB it dominates, and this driver has to move 4 KiB values.
func (b byteList) MarshalJSON() ([]byte, error) {
	// Worst case 4 bytes per element ("255,") plus the brackets.
	out := make([]byte, 0, len(b)*4+2)
	out = append(out, '[')
	for i, c := range b {
		if i > 0 {
			out = append(out, ',')
		}
		out = strconv.AppendUint(out, uint64(c), 10)
	}
	return append(out, ']'), nil
}

// decodeByteList is the inverse of byteList: it reads a JSON array of small
// integers (as sent for CacheValue) back into a []byte.
//
// Hand-parsed for two reasons. The first is correctness: encoding/json's
// []byte fast path assumes a base64 *string*, so unmarshaling straight into
// []byte is wrong here, and the obvious workaround — an explicit []int
// intermediate — is what this used to do.
//
// The second is that the workaround was measurably expensive. Reading 4 KiB
// values ran at ~1,900 ops/s against ~34,000 for 64-byte values on the same
// host: an []int intermediate is 8 bytes of allocation per payload byte plus a
// reflect hop each, so cost grew with value size in a way nothing else did.
// Parsing the digits directly removes both.
//
// Malformed input is refused rather than truncated: a value outside 0..255 is
// not a byte, and silently masking it would corrupt the payload it was meant
// to carry.
func decodeByteList(raw json.RawMessage) ([]byte, error) {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || string(s) == "null" {
		return nil, nil
	}
	if s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("expected a JSON array of bytes, got %.32s", raw)
	}
	s = s[1 : len(s)-1]

	out := make([]byte, 0, len(s)/2+1)
	n := 0      // the integer being parsed
	digits := 0 // how many digits it has, so a bare `,` is an error
	for i := 0; i <= len(s); i++ {
		// One past the end acts as a final separator, so the last element is
		// flushed without duplicating this block after the loop.
		if i == len(s) || s[i] == ',' {
			if digits == 0 {
				if i == len(s) && len(out) == 0 && strings.TrimSpace(string(s)) == "" {
					return out, nil // "[]" — an empty, non-nil value
				}
				return nil, fmt.Errorf("malformed byte array near offset %d", i)
			}
			out = append(out, byte(n))
			n, digits = 0, 0
			continue
		}
		c := s[i]
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			continue
		}
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("malformed byte array: unexpected %q at offset %d", c, i)
		}
		n = n*10 + int(c-'0')
		digits++
		if n > 255 {
			return nil, fmt.Errorf("byte array element at offset %d exceeds 255", i)
		}
	}
	return out, nil
}
