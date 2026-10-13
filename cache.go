package tricoredb

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// The cache family: string values with TTLs and counters, plus the collection
// types (lists, sets, hashes, streams).
//
// Values are []byte throughout, never string. The server stores opaque bytes,
// and a driver that only spoke strings would silently corrupt any value that is
// not valid UTF-8. The *Text helpers exist for the common case and are explicit
// about the encoding they impose.
//
// A key holds exactly one type at a time: operating on the wrong type is an
// error, never a coercion. A collection mutation never resets the key's TTL,
// and a collection that becomes empty deletes its key.

func cacheOp(body any) map[string]any { return map[string]any{"Cache": body} }

func cacheVariant(variant string, body map[string]any) map[string]any {
	return cacheOp(map[string]any{variant: body})
}

// nsKey is the (namespace, key) pair every keyed cache op starts from.
func nsKey(namespace, key string) map[string]any {
	return map[string]any{"namespace": namespace, "key": key}
}

// cacheValue reads a CacheValue payload. found is false on a miss, which is how
// a miss is told apart from a stored empty value.
func (c *Client) cacheValue(op any, what string) (value []byte, found bool, err error) {
	resp, err := c.request(op)
	if err != nil {
		return nil, false, err
	}
	if resp.DataKind != "CacheValue" {
		return nil, false, &ProtocolError{
			Message: fmt.Sprintf("expected CacheValue for %s, got %s", what, kindOrRaw(resp)),
		}
	}
	if isNullPayload(resp.DataRaw) {
		return nil, false, nil
	}
	v, err := decodeByteList(resp.DataRaw)
	if err != nil {
		return nil, false, &ProtocolError{Message: "malformed CacheValue payload: " + err.Error()}
	}
	return v, true, nil
}

// byteLists encodes a slice of values for the wire. An empty slice is refused
// here rather than by the server, so the error names the argument.
func byteLists(values [][]byte, name string) ([]any, error) {
	if len(values) == 0 {
		return nil, &ArgumentError{Message: name + " must not be empty"}
	}
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = byteList(v)
	}
	return out, nil
}

// CachePair is one (field, value) entry for a hash or a stream. Fields are
// arbitrary bytes and need not be UTF-8, which is why this is a slice of pairs
// rather than a map[string][]byte.
type CachePair struct {
	Field []byte
	Value []byte
}

// TextPairs builds pairs from a string map, encoding both halves as UTF-8.
func TextPairs(m map[string]string) []CachePair {
	out := make([]CachePair, 0, len(m))
	for k, v := range m {
		out = append(out, CachePair{Field: []byte(k), Value: []byte(v)})
	}
	return out
}

func pairList(entries []CachePair, name string) ([]any, error) {
	if len(entries) == 0 {
		return nil, &ArgumentError{Message: name + " must not be empty"}
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = []any{byteList(e.Field), byteList(e.Value)}
	}
	return out, nil
}

// -- strings -----------------------------------------------------------------

// CachePing is a liveness check routed through the cache core.
//
// Distinct from Ping, which exchanges a PING frame and never reaches a module.
// This one proves auth, routing and dispatch are working.
func (c *Client) CachePing() error {
	_, err := c.request(cacheOp("Ping"))
	return err
}

// CacheExists reports whether the key is present and unexpired.
func (c *Client) CacheExists(namespace, key string) (bool, error) {
	var out struct {
		Exists bool `json:"exists"`
	}
	return out.Exists, c.cacheJSON(cacheVariant("Exists", nsKey(namespace, key)), "Exists", &out)
}

// CacheTTL returns the remaining time to live in milliseconds.
//
// has is false when the key is missing *or* has no expiry; use CacheExists to
// tell those two apart.
func (c *Client) CacheTTL(namespace, key string) (ttlMs int64, has bool, err error) {
	var out struct {
		TTLMs *int64 `json:"ttl_ms"`
	}
	if err := c.cacheJSON(cacheVariant("Ttl", nsKey(namespace, key)), "Ttl", &out); err != nil {
		return 0, false, err
	}
	if out.TTLMs == nil {
		return 0, false, nil
	}
	return *out.TTLMs, true, nil
}

// CacheClearNamespace deletes every key in a namespace, returning how many were
// removed.
func (c *Client) CacheClearNamespace(namespace string) (int64, error) {
	var out struct {
		Cleared int64 `json:"cleared"`
	}
	op := cacheVariant("ClearNamespace", map[string]any{"namespace": namespace})
	return out.Cleared, c.cacheJSON(op, "ClearNamespace", &out)
}

// CacheIncr adds by to a counter and returns the new value. A missing key
// starts at 0; a key holding a non-numeric value is an error, never a coercion.
func (c *Client) CacheIncr(namespace, key string, by int64) (int64, error) {
	body := nsKey(namespace, key)
	body["by"] = by
	var out struct {
		Value int64 `json:"value"`
	}
	return out.Value, c.cacheJSON(cacheVariant("Incr", body), "Incr", &out)
}

// CacheExpire sets or replaces a key's TTL. False when the key does not exist.
func (c *Client) CacheExpire(namespace, key string, ttlMs int64) (bool, error) {
	body := nsKey(namespace, key)
	body["ttl_ms"] = ttlMs
	var out struct {
		Updated bool `json:"updated"`
	}
	return out.Updated, c.cacheJSON(cacheVariant("Expire", body), "Expire", &out)
}

// CachePersist removes a key's TTL, making it permanent. False when it had none.
func (c *Client) CachePersist(namespace, key string) (bool, error) {
	var out struct {
		Persisted bool `json:"persisted"`
	}
	return out.Persisted, c.cacheJSON(cacheVariant("Persist", nsKey(namespace, key)), "Persist", &out)
}

// CacheSetNx stores value only if the key is absent, reporting whether the
// write happened. The primitive behind a distributed lock. ttlMs <= 0 means no
// expiry.
func (c *Client) CacheSetNx(namespace, key string, value []byte, ttlMs int64) (bool, error) {
	body := nsKey(namespace, key)
	body["value"] = byteList(value)
	if ttlMs > 0 {
		body["ttl_ms"] = ttlMs
	} else {
		body["ttl_ms"] = nil
	}
	var out struct {
		Set bool `json:"set"`
	}
	return out.Set, c.cacheJSON(cacheVariant("SetNx", body), "SetNx", &out)
}

// CacheKeyInfo is one live key in a namespace.
type CacheKeyInfo struct {
	Key   string `json:"key"`
	TTLMs *int64 `json:"ttl_ms"`
	Bytes int64  `json:"bytes"`
}

// CacheKeys lists live keys in a namespace.
//
// pattern is a simple glob where * matches any run of characters ("user:*",
// "*:sess", "*tmp*"); an empty pattern lists everything. limit <= 0 leaves the
// cap to the server.
func (c *Client) CacheKeys(namespace, pattern string, limit int) ([]CacheKeyInfo, error) {
	body := map[string]any{"namespace": namespace}
	if pattern == "" {
		body["pattern"] = nil
	} else {
		body["pattern"] = pattern
	}
	if limit > 0 {
		body["limit"] = limit
	} else {
		body["limit"] = nil
	}
	var out struct {
		Keys []CacheKeyInfo `json:"keys"`
	}
	return out.Keys, c.cacheJSON(cacheVariant("Keys", body), "Keys", &out)
}

// -- lists -------------------------------------------------------------------

// CacheLPush prepends elements, returning the new length.
func (c *Client) CacheLPush(namespace, key string, values ...[]byte) (int64, error) {
	return c.push("LPush", namespace, key, values)
}

// CacheRPush appends elements, returning the new length.
func (c *Client) CacheRPush(namespace, key string, values ...[]byte) (int64, error) {
	return c.push("RPush", namespace, key, values)
}

func (c *Client) push(variant, namespace, key string, values [][]byte) (int64, error) {
	list, err := byteLists(values, "values")
	if err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["values"] = list
	var out struct {
		Length int64 `json:"length"`
	}
	return out.Length, c.cacheJSON(cacheVariant(variant, body), variant, &out)
}

// CacheLPop removes and returns the first element. found is false on an
// empty or missing key.
func (c *Client) CacheLPop(namespace, key string) (value []byte, found bool, err error) {
	return c.cacheValue(cacheVariant("LPop", nsKey(namespace, key)), "LPop")
}

// CacheRPop removes and returns the last element. found is false on an
// empty or missing key.
func (c *Client) CacheRPop(namespace, key string) (value []byte, found bool, err error) {
	return c.cacheValue(cacheVariant("RPop", nsKey(namespace, key)), "RPop")
}

// CacheLRange reads an inclusive index range. Negative indices count from the
// end (-1 is the last element) and out-of-range bounds clamp.
func (c *Client) CacheLRange(namespace, key string, start, stop int64) ([][]byte, error) {
	body := nsKey(namespace, key)
	body["start"] = start
	body["stop"] = stop
	var out struct {
		Values [][]int `json:"values"`
	}
	if err := c.cacheJSON(cacheVariant("LRange", body), "LRange", &out); err != nil {
		return nil, err
	}
	return toByteSlices(out.Values), nil
}

// CacheLLen is the number of elements; 0 when the key is missing.
func (c *Client) CacheLLen(namespace, key string) (int64, error) {
	var out struct {
		Length int64 `json:"length"`
	}
	return out.Length, c.cacheJSON(cacheVariant("LLen", nsKey(namespace, key)), "LLen", &out)
}

// CacheLIndex reads one element by index; negative counts from the end. found
// is false when the index is out of range.
func (c *Client) CacheLIndex(namespace, key string, index int64) (value []byte, found bool, err error) {
	body := nsKey(namespace, key)
	body["index"] = index
	return c.cacheValue(cacheVariant("LIndex", body), "LIndex")
}

// -- sets --------------------------------------------------------------------

// CacheSAdd adds members, returning how many were newly added.
func (c *Client) CacheSAdd(namespace, key string, members ...[]byte) (int64, error) {
	list, err := byteLists(members, "members")
	if err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["members"] = list
	var out struct {
		Added int64 `json:"added"`
	}
	return out.Added, c.cacheJSON(cacheVariant("SAdd", body), "SAdd", &out)
}

// CacheSRem removes members, returning how many were present.
func (c *Client) CacheSRem(namespace, key string, members ...[]byte) (int64, error) {
	list, err := byteLists(members, "members")
	if err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["members"] = list
	var out struct {
		Removed int64 `json:"removed"`
	}
	return out.Removed, c.cacheJSON(cacheVariant("SRem", body), "SRem", &out)
}

// CacheSIsMember reports whether member is in the set.
func (c *Client) CacheSIsMember(namespace, key string, member []byte) (bool, error) {
	body := nsKey(namespace, key)
	body["member"] = byteList(member)
	var out struct {
		IsMember bool `json:"is_member"`
	}
	return out.IsMember, c.cacheJSON(cacheVariant("SIsMember", body), "SIsMember", &out)
}

// CacheSCard is the number of members; 0 when the key is missing.
func (c *Client) CacheSCard(namespace, key string) (int64, error) {
	var out struct {
		Cardinality int64 `json:"cardinality"`
	}
	return out.Cardinality, c.cacheJSON(cacheVariant("SCard", nsKey(namespace, key)), "SCard", &out)
}

// CacheSMembers returns every member, in ascending byte order.
func (c *Client) CacheSMembers(namespace, key string) ([][]byte, error) {
	var out struct {
		Members [][]int `json:"members"`
	}
	if err := c.cacheJSON(cacheVariant("SMembers", nsKey(namespace, key)), "SMembers", &out); err != nil {
		return nil, err
	}
	return toByteSlices(out.Members), nil
}

// -- hashes ------------------------------------------------------------------

// CacheHSet sets fields, returning how many were newly created (as opposed to
// overwritten).
func (c *Client) CacheHSet(namespace, key string, entries []CachePair) (int64, error) {
	list, err := pairList(entries, "entries")
	if err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["entries"] = list
	var out struct {
		Created int64 `json:"created"`
	}
	return out.Created, c.cacheJSON(cacheVariant("HSet", body), "HSet", &out)
}

// CacheHSetText sets string fields, encoded UTF-8.
func (c *Client) CacheHSetText(namespace, key string, entries map[string]string) (int64, error) {
	return c.CacheHSet(namespace, key, TextPairs(entries))
}

// CacheHGet reads one field. found is false when the field or key is absent.
func (c *Client) CacheHGet(namespace, key string, field []byte) (value []byte, found bool, err error) {
	body := nsKey(namespace, key)
	body["field"] = byteList(field)
	return c.cacheValue(cacheVariant("HGet", body), "HGet")
}

// CacheHDel deletes fields, returning how many were present.
func (c *Client) CacheHDel(namespace, key string, fields ...[]byte) (int64, error) {
	list, err := byteLists(fields, "fields")
	if err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["fields"] = list
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	return out.Deleted, c.cacheJSON(cacheVariant("HDel", body), "HDel", &out)
}

// CacheHGetAll returns every field/value pair, in ascending field order.
func (c *Client) CacheHGetAll(namespace, key string) ([]CachePair, error) {
	var out struct {
		Entries [][][]int `json:"entries"`
	}
	if err := c.cacheJSON(cacheVariant("HGetAll", nsKey(namespace, key)), "HGetAll", &out); err != nil {
		return nil, err
	}
	pairs := make([]CachePair, 0, len(out.Entries))
	for _, e := range out.Entries {
		if len(e) != 2 {
			return nil, &ProtocolError{Message: "expected each hash entry to be a [field, value] pair"}
		}
		pairs = append(pairs, CachePair{Field: toBytes(e[0]), Value: toBytes(e[1])})
	}
	return pairs, nil
}

// CacheHExists reports whether a field exists in the hash.
func (c *Client) CacheHExists(namespace, key string, field []byte) (bool, error) {
	body := nsKey(namespace, key)
	body["field"] = byteList(field)
	var out struct {
		Exists bool `json:"exists"`
	}
	return out.Exists, c.cacheJSON(cacheVariant("HExists", body), "HExists", &out)
}

// CacheHLen is the number of fields; 0 when the key is missing.
func (c *Client) CacheHLen(namespace, key string) (int64, error) {
	var out struct {
		Length int64 `json:"length"`
	}
	return out.Length, c.cacheJSON(cacheVariant("HLen", nsKey(namespace, key)), "HLen", &out)
}

// -- streams -----------------------------------------------------------------
//
// Append-only logs. Each entry carries a strictly increasing "<ms>-<seq>" id;
// that ordering is the point, so XAdd refuses a caller id that is not greater
// than the last. Consumer groups and blocking reads are out of scope in V1 and
// are refused by name.

// StreamEntry is one entry in a stream. Fields is authoritative — stream fields
// are arbitrary bytes; Text is a convenience for the all-UTF-8 case.
type StreamEntry struct {
	ID     string
	Fields []CachePair
}

// Text decodes the fields as UTF-8. Wrong for binary payloads; Fields stays
// authoritative.
func (e StreamEntry) Text() map[string]string {
	m := make(map[string]string, len(e.Fields))
	for _, f := range e.Fields {
		m[string(f.Field)] = string(f.Value)
	}
	return m
}

// CacheXAdd appends an entry, returning the assigned id.
//
// id is "" or "*" to auto-generate, "<ms>" or "<ms>-*" to fix the millisecond,
// or "<ms>-<seq>" for an exact id. A non-increasing id is an error.
func (c *Client) CacheXAdd(namespace, key string, fields []CachePair, id string) (string, error) {
	list, err := pairList(fields, "fields")
	if err != nil {
		return "", err
	}
	body := nsKey(namespace, key)
	body["fields"] = list
	if id == "" {
		body["id"] = nil
	} else {
		body["id"] = id
	}
	var out struct {
		ID string `json:"id"`
	}
	return out.ID, c.cacheJSON(cacheVariant("XAdd", body), "XAdd", &out)
}

// CacheXAddText appends an entry whose fields are strings, encoded UTF-8.
func (c *Client) CacheXAddText(namespace, key string, fields map[string]string, id string) (string, error) {
	return c.CacheXAdd(namespace, key, TextPairs(fields), id)
}

// CacheXLen is the number of entries; 0 when the key is missing.
func (c *Client) CacheXLen(namespace, key string) (int64, error) {
	var out struct {
		Length int64 `json:"length"`
	}
	return out.Length, c.cacheJSON(cacheVariant("XLen", nsKey(namespace, key)), "XLen", &out)
}

// CacheXRange reads entries whose id falls in the inclusive range. "-" and "+"
// are the min/max ids; a bare "<ms>" spans that whole millisecond. count <= 0
// leaves the cap to the server.
func (c *Client) CacheXRange(namespace, key, start, end string, count int) ([]StreamEntry, error) {
	body := nsKey(namespace, key)
	body["start"] = start
	body["end"] = end
	body["count"] = optCount(count)
	return c.streamEntries(cacheVariant("XRange", body), "XRange")
}

// CacheXRead reads entries strictly newer than after — the non-blocking poll
// primitive. Pass "$" for "only new entries". This read never blocks.
func (c *Client) CacheXRead(namespace, key, after string, count int) ([]StreamEntry, error) {
	body := nsKey(namespace, key)
	body["after"] = after
	body["count"] = optCount(count)
	return c.streamEntries(cacheVariant("XRead", body), "XRead")
}

// CacheXDel deletes entries by exact id, returning how many were present.
func (c *Client) CacheXDel(namespace, key string, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, &ArgumentError{Message: "ids must not be empty"}
	}
	body := nsKey(namespace, key)
	body["ids"] = ids
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	return out.Deleted, c.cacheJSON(cacheVariant("XDel", body), "XDel", &out)
}

// CacheXTrim caps the stream by evicting the oldest entries, returning how many
// were evicted.
func (c *Client) CacheXTrim(namespace, key string, maxLen int) (int64, error) {
	if maxLen < 0 {
		return 0, &ArgumentError{Message: "maxLen must not be negative"}
	}
	body := nsKey(namespace, key)
	body["max_len"] = maxLen
	var out struct {
		Trimmed int64 `json:"trimmed"`
	}
	return out.Trimmed, c.cacheJSON(cacheVariant("XTrim", body), "XTrim", &out)
}

func (c *Client) streamEntries(op any, what string) ([]StreamEntry, error) {
	var out struct {
		Entries []struct {
			ID     string    `json:"id"`
			Fields [][][]int `json:"fields"`
		} `json:"entries"`
	}
	if err := c.cacheJSON(op, what, &out); err != nil {
		return nil, err
	}
	entries := make([]StreamEntry, 0, len(out.Entries))
	for _, e := range out.Entries {
		fields := make([]CachePair, 0, len(e.Fields))
		for _, f := range e.Fields {
			if len(f) != 2 {
				return nil, &ProtocolError{Message: "expected each stream field to be a [field, value] pair"}
			}
			fields = append(fields, CachePair{Field: toBytes(f[0]), Value: toBytes(f[1])})
		}
		entries = append(entries, StreamEntry{ID: e.ID, Fields: fields})
	}
	return entries, nil
}

// -- shared ------------------------------------------------------------------

func (c *Client) cacheJSON(op any, what string, out any) error {
	resp, err := c.request(op)
	if err != nil {
		return err
	}
	return decodeJSON(resp, what, out)
}

func optCount(count int) any {
	if count > 0 {
		return count
	}
	return nil
}

func toBytes(ints []int) []byte {
	out := make([]byte, len(ints))
	for i, v := range ints {
		out[i] = byte(v)
	}
	return out
}

func toByteSlices(rows [][]int) [][]byte {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = toBytes(r)
	}
	return out
}

var _ = json.Marshal // keep encoding/json imported for the struct tags above

// -- added for server 0.1.0-rc.3 ---------------------------------------------
//
// Everything below needs tricore-server 0.1.0-rc.3 or later. An older server
// does not know these operations and answers each with an error.

// CacheEntry is one key and its value, as CacheMSet and CacheWarm take them.
type CacheEntry struct {
	Key   string
	Value []byte
}

func entryList(entries []CacheEntry) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = map[string]any{"key": e.Key, "value": byteList(e.Value)}
	}
	return out
}

// ttlOrNull is the wire form of an optional TTL: null when ttlMs <= 0.
func ttlOrNull(ttlMs int64) any {
	if ttlMs > 0 {
		return ttlMs
	}
	return nil
}

// SetCondition says when CacheSetIf is allowed to write.
type SetCondition string

const (
	// SetAlways writes unconditionally.
	SetAlways SetCondition = "Always"
	// SetIfAbsent writes only when the key is absent. An expired key counts
	// as absent.
	SetIfAbsent SetCondition = "IfAbsent"
	// SetIfPresent writes only when the key is present and unexpired.
	SetIfPresent SetCondition = "IfPresent"
)

// CacheExpiry says what an operation does with a key's deadline. Build one
// with ExpiryKeep, ExpirySet or ExpiryClear.
//
// Three intents rather than an optional duration, because "leave the TTL
// alone" and "remove it" would otherwise share a spelling. The zero
// CacheExpiry is invalid and is refused rather than guessed at.
type CacheExpiry struct {
	body any
}

// ExpiryKeep leaves the deadline as it is.
func ExpiryKeep() CacheExpiry { return CacheExpiry{body: "Keep"} }

// ExpiryClear removes the deadline, making the key permanent.
func ExpiryClear() CacheExpiry { return CacheExpiry{body: "Clear"} }

// ExpirySet expires the key ttlMs milliseconds from now.
func ExpirySet(ttlMs int64) CacheExpiry {
	return CacheExpiry{body: map[string]any{"Set": map[string]any{"ttl_ms": ttlMs}}}
}

var errZeroExpiry = errors.New(
	"tricoredb: zero CacheExpiry; use ExpiryKeep(), ExpirySet(ttlMs) or ExpiryClear()")

// check refuses the zero value before anything is written: a frame that fails
// to encode costs the connection, and an argument error should not.
func (e CacheExpiry) check() error {
	if e.body == nil {
		return &ArgumentError{Message: errZeroExpiry.Error()}
	}
	return nil
}

// MarshalJSON writes the server's externally-tagged form.
func (e CacheExpiry) MarshalJSON() ([]byte, error) {
	if e.body == nil {
		return nil, errZeroExpiry
	}
	return json.Marshal(e.body)
}

// ListEnd is which end of a list an element leaves or joins.
type ListEnd string

const (
	// ListLeft is the head, where LPush and LPop work.
	ListLeft ListEnd = "Left"
	// ListRight is the tail, where RPush and RPop work.
	ListRight ListEnd = "Right"
)

// BitOpKind is the bitwise operation CacheBitOp applies across its sources.
type BitOpKind string

const (
	BitAnd BitOpKind = "And"
	BitOr  BitOpKind = "Or"
	BitXor BitOpKind = "Xor"
	// BitNot inverts every bit, and takes exactly one source.
	BitNot BitOpKind = "Not"
)

// ByteRange is an inclusive range of bytes within a value. Negative indices
// count from the end, so {0, -1} is the whole value.
type ByteRange struct {
	Start int64
	End   int64
}

func byteRange(r *ByteRange) any {
	if r == nil {
		return nil
	}
	return []int64{r.Start, r.End}
}

// ScoredMember is one member of a sorted set and its score.
type ScoredMember struct {
	// Score is what the set is ordered by. It must be finite.
	Score  float64
	Member []byte
}

// ScoreBound is one end of a score range.
//
// A stored score is always finite, and JSON cannot carry an infinity, so the
// open ends are ScoreLowest and ScoreHighest rather than math.Inf. A
// non-finite bound is refused before anything is sent.
type ScoreBound struct {
	Value float64
	// Exclusive leaves out a member with exactly this score.
	Exclusive bool
}

// ScoreLowest is a bound below every score a set can hold.
func ScoreLowest() ScoreBound { return ScoreBound{Value: -math.MaxFloat64} }

// ScoreHighest is a bound above every score a set can hold.
func ScoreHighest() ScoreBound { return ScoreBound{Value: math.MaxFloat64} }

// finiteScore refuses NaN, which has no place in an ordering, and the
// infinities, which JSON has no spelling for.
func finiteScore(score float64, name string) error {
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return &ArgumentError{Message: fmt.Sprintf("%s must be a finite number, got %v", name, score)}
	}
	return nil
}

func (b ScoreBound) wire(name string) (map[string]any, error) {
	if err := finiteScore(b.Value, name); err != nil {
		return nil, err
	}
	return map[string]any{"value": b.Value, "exclusive": b.Exclusive}, nil
}

// ZAddResult is what CacheZAdd changed.
type ZAddResult struct {
	// Added counts members that were not in the set before.
	Added int64 `json:"added"`
	// Updated counts members already there whose score changed.
	Updated int64 `json:"updated"`
}

// WarmResult is what CacheWarm did.
type WarmResult struct {
	Written int64 `json:"written"`
	// AlreadyCached counts entries left alone because a live value was there.
	AlreadyCached int64 `json:"alreadyCached"`
}

// FreshState is which of its four answers CacheGetFresh gave.
type FreshState string

const (
	// FreshStateFresh: the value is cached and within its fresh window.
	FreshStateFresh FreshState = "fresh"
	// FreshStateStale: the value is cached but past its fresh window.
	FreshStateStale FreshState = "stale"
	// FreshStateMissing: the source is known to have no value for the key.
	FreshStateMissing FreshState = "missing"
	// FreshStateMiss: nothing is cached for the key.
	FreshStateMiss FreshState = "miss"
)

// FreshRead is the answer to CacheGetFresh.
type FreshRead struct {
	State FreshState
	// Value is the cached value when State is fresh or stale, and nil
	// otherwise.
	Value []byte
	// Refresh, on a stale read, says this caller holds the refresh lease and
	// should recompute the value.
	Refresh bool
	// Lease, on a miss, says this caller holds the lease and should compute
	// the value.
	Lease bool
	// RetryAfterMs, on a miss another caller is computing, is how long to wait
	// before asking again. Zero when the server gave none.
	RetryAfterMs int64
}

// CacheLock is the answer to CacheLockAcquire.
type CacheLock struct {
	Acquired bool `json:"acquired"`
	// Token is the fencing token when the lock was acquired, and zero
	// otherwise. It rises with every acquisition and is never reused; pass it
	// to the resource the lock protects.
	Token uint64 `json:"token"`
	// Holder is who holds the lock.
	Holder string `json:"holder"`
	// ExpiresAtMs is when the current lease ends, in Unix epoch milliseconds.
	ExpiresAtMs int64 `json:"expiresAtMs"`
}

// CacheLockRenewal is the answer to CacheLockRenew.
type CacheLockRenewal struct {
	// Renewed is false once the lease has lapsed, even when nobody else took
	// the lock since.
	Renewed     bool   `json:"renewed"`
	Token       uint64 `json:"token"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// TrimResult is what CacheLTrim left behind.
type TrimResult struct {
	// Length is how many elements the list holds now.
	Length  int64 `json:"length"`
	Removed int64 `json:"removed"`
}

// ExpireAtResult is what CacheExpireAt did.
type ExpireAtResult struct {
	// Updated says a live key was given the deadline.
	Updated bool `json:"updated"`
	// Deleted says the key was deleted because the deadline had passed.
	Deleted bool `json:"deleted"`
}

// CompareAndSwapResult is the answer to CacheCompareAndSwap.
type CompareAndSwapResult struct {
	Swapped bool
	// Current, on a mismatch, is the value actually present when it is valid
	// UTF-8, and nil otherwise.
	Current []byte
	// CurrentIsBinary, on a mismatch, says the value present is binary. The
	// server does not return such a value; read it with CacheGet.
	CurrentIsBinary bool
}

// CacheStats is what one node's cache is doing. Node-wide, not per database.
type CacheStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
	// HitRatio is hits over lookups, and nil before the first lookup.
	HitRatio *float64 `json:"hitRatio"`
	Memory   struct {
		Enabled       bool    `json:"enabled"`
		MaxBytes      *uint64 `json:"maxBytes"`
		LowWaterBytes *uint64 `json:"lowWaterBytes"`
		// ResidentBytes is nil until a measurement has been taken.
		ResidentBytes *uint64 `json:"residentBytes"`
		Measurements  uint64  `json:"measurements"`
	} `json:"memory"`
	Eviction struct {
		Policy  string `json:"policy"`
		Evicted uint64 `json:"evicted"`
	} `json:"eviction"`
}

// NamespaceLimit is a namespace's limit and what it holds now.
type NamespaceLimit struct {
	Namespace string `json:"namespace"`
	Keys      uint64 `json:"keys"`
	Bytes     uint64 `json:"bytes"`
	// MaxKeys and MaxBytes are nil when that cap is not set.
	MaxKeys  *uint64 `json:"max_keys"`
	MaxBytes *uint64 `json:"max_bytes"`
}

// ChannelMessage is one message read by CachePoll.
type ChannelMessage struct {
	// ID rises across every channel.
	ID      uint64 `json:"id"`
	Channel string `json:"channel"`
	Message string `json:"message"`
	// AtMs is when it was published, in Unix epoch milliseconds.
	AtMs int64 `json:"at_ms"`
}

// PollResult is one page of messages from CachePoll.
type PollResult struct {
	// Messages are oldest first.
	Messages []ChannelMessage `json:"messages"`
	// Next is the id to pass as after on the next poll.
	Next uint64 `json:"next"`
}

// CacheCommand is one cache operation inside CachePipeline, CacheTransaction
// or a script. Build one with the Cmd* constructors; CmdRaw builds any other
// from the server's own variant name and fields.
//
// The zero CacheCommand is invalid and is refused rather than skipped.
type CacheCommand struct {
	body any
}

var errZeroCommand = errors.New(
	"tricoredb: zero CacheCommand; build one with CmdGet, CmdSet, CmdRaw and friends")

func checkCommands(ops []CacheCommand) error {
	if len(ops) == 0 {
		return &ArgumentError{Message: "ops must not be empty"}
	}
	for _, op := range ops {
		if op.body == nil {
			return &ArgumentError{Message: errZeroCommand.Error()}
		}
	}
	return nil
}

// MarshalJSON writes the server's externally-tagged form.
func (c CacheCommand) MarshalJSON() ([]byte, error) {
	if c.body == nil {
		return nil, errZeroCommand
	}
	return json.Marshal(c.body)
}

// CmdRaw is any cache operation, by the server's variant name and its fields.
// A nil body is an operation that takes no fields, such as "Ping".
func CmdRaw(variant string, body map[string]any) CacheCommand {
	if body == nil {
		return CacheCommand{body: variant}
	}
	return CacheCommand{body: map[string]any{variant: body}}
}

// CmdGet reads a value.
func CmdGet(namespace, key string) CacheCommand { return CmdRaw("Get", nsKey(namespace, key)) }

// CmdSet stores a value; ttlMs <= 0 means no expiry.
func CmdSet(namespace, key string, value []byte, ttlMs int64) CacheCommand {
	body := nsKey(namespace, key)
	body["value"] = byteList(value)
	body["ttl_ms"] = ttlOrNull(ttlMs)
	return CmdRaw("Set", body)
}

// CmdDelete deletes a key.
func CmdDelete(namespace, key string) CacheCommand { return CmdRaw("Delete", nsKey(namespace, key)) }

// CmdExists reports whether a key is present and unexpired.
func CmdExists(namespace, key string) CacheCommand { return CmdRaw("Exists", nsKey(namespace, key)) }

// CmdIncr adds by to a counter.
func CmdIncr(namespace, key string, by int64) CacheCommand {
	body := nsKey(namespace, key)
	body["by"] = by
	return CmdRaw("Incr", body)
}

// CacheReply is what one operation of a pipeline, transaction or script
// answered.
type CacheReply struct {
	// Err is the server's message when the operation failed, and empty when
	// it succeeded. The operations after a failed one still ran.
	Err string
	// DataKind names the data variant of a success, as Response.DataKind does.
	DataKind string
	// DataRaw is that variant's payload.
	DataRaw json.RawMessage
}

// OK reports whether the operation succeeded.
func (r CacheReply) OK() bool { return r.Err == "" }

// Value is the bytes of a CacheValue reply. found is false for a miss, and for
// a reply that is not a value at all.
func (r CacheReply) Value() (value []byte, found bool) {
	if r.DataKind != "CacheValue" || isNullPayload(r.DataRaw) {
		return nil, false
	}
	v, err := decodeByteList(r.DataRaw)
	if err != nil {
		return nil, false
	}
	return v, true
}

func (c *Client) cacheReplies(op any, what string) ([]CacheReply, error) {
	var out struct {
		Results []struct {
			OK    json.RawMessage `json:"ok"`
			Error *string         `json:"error"`
		} `json:"results"`
	}
	if err := c.cacheJSON(op, what, &out); err != nil {
		return nil, err
	}
	replies := make([]CacheReply, 0, len(out.Results))
	for _, r := range out.Results {
		if r.Error != nil {
			replies = append(replies, CacheReply{Err: *r.Error})
			continue
		}
		kind, raw, err := decodeVariant(r.OK)
		if err != nil {
			return nil, &ProtocolError{Message: "malformed " + what + " result: " + err.Error()}
		}
		replies = append(replies, CacheReply{DataKind: kind, DataRaw: raw})
	}
	return replies, nil
}

// cacheValues reads a CacheValues payload: one value per position, nil where
// there is none.
func (c *Client) cacheValues(op any, what string) ([][]byte, error) {
	resp, err := c.request(op)
	if err != nil {
		return nil, err
	}
	if resp.DataKind != "CacheValues" {
		return nil, &ProtocolError{
			Message: fmt.Sprintf("expected CacheValues for %s, got %s", what, kindOrRaw(resp)),
		}
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(resp.DataRaw, &raw); err != nil {
		return nil, &ProtocolError{Message: "malformed CacheValues payload: " + err.Error()}
	}
	out := make([][]byte, len(raw))
	for i, item := range raw {
		if isNullPayload(item) {
			continue
		}
		v, err := decodeByteList(item)
		if err != nil {
			return nil, &ProtocolError{Message: "malformed CacheValues payload: " + err.Error()}
		}
		if v == nil {
			v = []byte{}
		}
		out[i] = v
	}
	return out, nil
}

// cacheMembers reads the {"members": [...]} answer the set and sorted-set
// reads share.
func (c *Client) cacheMembers(op any, what string) ([][]byte, error) {
	var out struct {
		Members [][]int `json:"members"`
	}
	if err := c.cacheJSON(op, what, &out); err != nil {
		return nil, err
	}
	return toByteSlices(out.Members), nil
}

func (c *Client) cacheScoredMembers(op any, what string) ([]ScoredMember, error) {
	var out struct {
		Members []struct {
			Score  float64 `json:"score"`
			Member []int   `json:"member"`
		} `json:"members"`
	}
	if err := c.cacheJSON(op, what, &out); err != nil {
		return nil, err
	}
	members := make([]ScoredMember, len(out.Members))
	for i, m := range out.Members {
		members[i] = ScoredMember{Score: m.Score, Member: toBytes(m.Member)}
	}
	return members, nil
}

// CacheMGet reads many values in one round trip.
//
// It returns one value per key, in the order asked, with nil where the key is
// absent or expired; a stored empty value is an empty, non-nil slice. Not a
// snapshot: another writer may land between two of the reads.
func (c *Client) CacheMGet(namespace string, keys ...string) ([][]byte, error) {
	return c.cacheValues(cacheVariant("MGet", keysBody(namespace, keys)), "MGet")
}

// CacheCompareAndSwap writes only if the key still holds what the caller last
// read.
//
// A nil expected requires the key to be absent, and a nil value deletes it; an
// empty non-nil slice is the empty value. On a mismatch nothing is written and
// the result carries what is actually there. ttlMs <= 0 means no expiry.
func (c *Client) CacheCompareAndSwap(namespace, key string, expected, value []byte, ttlMs int64) (*CompareAndSwapResult, error) {
	body := nsKey(namespace, key)
	body["expected"] = optBytes(expected)
	body["value"] = optBytes(value)
	body["ttl_ms"] = ttlOrNull(ttlMs)
	var out struct {
		Swapped         bool    `json:"swapped"`
		Current         *string `json:"current"`
		CurrentIsBinary bool    `json:"currentIsBinary"`
	}
	if err := c.cacheJSON(cacheVariant("CompareAndSwap", body), "CompareAndSwap", &out); err != nil {
		return nil, err
	}
	res := &CompareAndSwapResult{Swapped: out.Swapped, CurrentIsBinary: out.CurrentIsBinary}
	if out.Current != nil {
		res.Current = []byte(*out.Current)
	}
	return res, nil
}

func optBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return byteList(b)
}

// CacheGetFresh reads with stale-while-revalidate and stampede protection.
//
// owner names this caller; of the callers that find a value stale or missing,
// one is handed a lease of leaseMs to recompute it and the rest are told to
// serve the stale value or wait.
func (c *Client) CacheGetFresh(namespace, key, owner string, leaseMs int64) (*FreshRead, error) {
	body := nsKey(namespace, key)
	body["owner"] = owner
	body["lease_ms"] = leaseMs
	var out struct {
		State        string  `json:"state"`
		Value        *string `json:"value"`
		ValueHex     *string `json:"valueHex"`
		Refresh      bool    `json:"refresh"`
		Lease        bool    `json:"lease"`
		RetryAfterMs int64   `json:"retryAfterMs"`
	}
	if err := c.cacheJSON(cacheVariant("GetFresh", body), "GetFresh", &out); err != nil {
		return nil, err
	}
	read := &FreshRead{
		State: FreshState(out.State), Refresh: out.Refresh, Lease: out.Lease, RetryAfterMs: out.RetryAfterMs,
	}
	switch read.State {
	case FreshStateFresh, FreshStateStale, FreshStateMissing, FreshStateMiss:
	default:
		return nil, &ProtocolError{Message: fmt.Sprintf("GetFresh answered with the unknown state %q", out.State)}
	}
	// A value that is valid UTF-8 arrives as text, any other as hex.
	switch {
	case out.Value != nil:
		read.Value = []byte(*out.Value)
	case out.ValueHex != nil:
		v, err := hex.DecodeString(*out.ValueHex)
		if err != nil {
			return nil, &ProtocolError{Message: "malformed hex value in GetFresh: " + err.Error()}
		}
		read.Value = v
	}
	return read, nil
}

// CacheZAdd adds scored members to a sorted set, or re-scores ones already
// there. A score that is not finite is refused.
func (c *Client) CacheZAdd(namespace, key string, members ...ScoredMember) (*ZAddResult, error) {
	if len(members) == 0 {
		return nil, &ArgumentError{Message: "members must not be empty"}
	}
	list := make([]any, len(members))
	for i, m := range members {
		if err := finiteScore(m.Score, "score"); err != nil {
			return nil, err
		}
		list[i] = map[string]any{"score": m.Score, "member": byteList(m.Member)}
	}
	body := nsKey(namespace, key)
	body["members"] = list
	var out ZAddResult
	if err := c.cacheJSON(cacheVariant("ZAdd", body), "ZAdd", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheZScore returns one member's score. found is false when it is not in
// the set.
func (c *Client) CacheZScore(namespace, key string, member []byte) (score float64, found bool, err error) {
	body := nsKey(namespace, key)
	body["member"] = byteList(member)
	var out struct {
		Score *float64 `json:"score"`
	}
	if err := c.cacheJSON(cacheVariant("ZScore", body), "ZScore", &out); err != nil || out.Score == nil {
		return 0, false, err
	}
	return *out.Score, true, nil
}

// CacheZRank returns a member's position in the ordering. rev counts from the
// high-scoring end. found is false when the member is absent.
func (c *Client) CacheZRank(namespace, key string, member []byte, rev bool) (rank int64, found bool, err error) {
	body := nsKey(namespace, key)
	body["member"] = byteList(member)
	body["rev"] = rev
	var out struct {
		Rank *int64 `json:"rank"`
	}
	if err := c.cacheJSON(cacheVariant("ZRank", body), "ZRank", &out); err != nil || out.Rank == nil {
		return 0, false, err
	}
	return *out.Rank, true, nil
}

func zrangeOp(namespace, key string, start, stop int64, rev, withScores bool) map[string]any {
	body := nsKey(namespace, key)
	body["start"] = start
	body["stop"] = stop
	body["rev"] = rev
	body["with_scores"] = withScores
	return cacheVariant("ZRange", body)
}

// CacheZRange returns members by rank, start..=stop inclusive. Negative
// indices count from the end, so 0, -1 is the whole set.
func (c *Client) CacheZRange(namespace, key string, start, stop int64, rev bool) ([][]byte, error) {
	return c.cacheMembers(zrangeOp(namespace, key, start, stop, rev, false), "ZRange")
}

// CacheZRangeWithScores is CacheZRange with each member's score.
func (c *Client) CacheZRangeWithScores(namespace, key string, start, stop int64, rev bool) ([]ScoredMember, error) {
	return c.cacheScoredMembers(zrangeOp(namespace, key, start, stop, rev, true), "ZRange")
}

// ScoreRangeOptions narrows CacheZRangeByScore. The zero value returns the
// whole range, lowest score first.
type ScoreRangeOptions struct {
	// Rev returns the highest score first.
	Rev bool
	// Offset skips this many members of the range. It applies only with Count.
	Offset int64
	// Count caps the members returned. Zero or less means no cap.
	Count int64
}

func zrangeByScoreOp(namespace, key string, min, max ScoreBound, opts ScoreRangeOptions, withScores bool) (map[string]any, error) {
	lo, err := min.wire("min")
	if err != nil {
		return nil, err
	}
	hi, err := max.wire("max")
	if err != nil {
		return nil, err
	}
	body := nsKey(namespace, key)
	body["min"] = lo
	body["max"] = hi
	body["rev"] = opts.Rev
	body["with_scores"] = withScores
	if opts.Count > 0 {
		offset := opts.Offset
		if offset < 0 {
			offset = 0
		}
		body["limit"] = []int64{offset, opts.Count}
	} else {
		body["limit"] = nil
	}
	return cacheVariant("ZRangeByScore", body), nil
}

// CacheZRangeByScore returns members by score, between two bounds.
func (c *Client) CacheZRangeByScore(namespace, key string, min, max ScoreBound, opts ScoreRangeOptions) ([][]byte, error) {
	op, err := zrangeByScoreOp(namespace, key, min, max, opts, false)
	if err != nil {
		return nil, err
	}
	return c.cacheMembers(op, "ZRangeByScore")
}

// CacheZRangeByScoreWithScores is CacheZRangeByScore with each member's score.
func (c *Client) CacheZRangeByScoreWithScores(namespace, key string, min, max ScoreBound, opts ScoreRangeOptions) ([]ScoredMember, error) {
	op, err := zrangeByScoreOp(namespace, key, min, max, opts, true)
	if err != nil {
		return nil, err
	}
	return c.cacheScoredMembers(op, "ZRangeByScore")
}

// CacheZIncrBy adds by to a member's score, creating it at by when absent, and
// returns the new score.
func (c *Client) CacheZIncrBy(namespace, key string, member []byte, by float64) (float64, error) {
	if err := finiteScore(by, "by"); err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["member"] = byteList(member)
	body["by"] = by
	var out struct {
		Score float64 `json:"score"`
	}
	return out.Score, c.cacheJSON(cacheVariant("ZIncrBy", body), "ZIncrBy", &out)
}

// CacheSetBit sets one bit of a string value, growing it with zero bytes if
// needed, and returns the bit's old value. Bits are numbered from the most
// significant bit of byte 0.
func (c *Client) CacheSetBit(namespace, key string, offset uint64, value bool) (previous bool, err error) {
	body := nsKey(namespace, key)
	body["offset"] = offset
	body["value"] = value
	var out struct {
		Previous int `json:"previous"`
	}
	err = c.cacheJSON(cacheVariant("SetBit", body), "SetBit", &out)
	return out.Previous != 0, err
}

// CacheGetBit reads one bit. Past the end of the value, and on a missing key,
// every bit is unset.
func (c *Client) CacheGetBit(namespace, key string, offset uint64) (bool, error) {
	body := nsKey(namespace, key)
	body["offset"] = offset
	var out struct {
		Bit int `json:"bit"`
	}
	err := c.cacheJSON(cacheVariant("GetBit", body), "GetBit", &out)
	return out.Bit != 0, err
}

func (c *Client) blockingPop(variant, namespace string, keys []string, timeoutMs int64) (key string, value []byte, found bool, err error) {
	body := keysBody(namespace, keys)
	body["timeout_ms"] = timeoutMs
	values, err := c.cacheValues(cacheVariant(variant, body), variant)
	if err != nil {
		return "", nil, false, err
	}
	if len(values) < 2 || values[0] == nil || values[1] == nil {
		return "", nil, false, nil
	}
	return string(values[0]), values[1], true, nil
}

// CacheBLPop pops the head of the first non-empty list among keys, waiting up
// to timeoutMs for one to receive an element. It returns the key the element
// came from; found is false when the wait ran out.
//
// The wait happens on the server, so keep timeoutMs under this connection's
// read timeout.
func (c *Client) CacheBLPop(namespace string, timeoutMs int64, keys ...string) (key string, value []byte, found bool, err error) {
	return c.blockingPop("BLPop", namespace, keys, timeoutMs)
}

// CacheBRPop is CacheBLPop from the tail.
func (c *Client) CacheBRPop(namespace string, timeoutMs int64, keys ...string) (key string, value []byte, found bool, err error) {
	return c.blockingPop("BRPop", namespace, keys, timeoutMs)
}

// CacheSRandMember returns one random member of a set without removing it, or
// none when the set is empty.
func (c *Client) CacheSRandMember(namespace, key string) ([][]byte, error) {
	body := nsKey(namespace, key)
	body["count"] = nil
	return c.cacheMembers(cacheVariant("SRandMember", body), "SRandMember")
}

// CacheSRandMembers returns random members of a set without removing them.
//
// A positive count gives up to count distinct members; a negative one gives
// exactly -count members with repeats allowed; zero gives none.
func (c *Client) CacheSRandMembers(namespace, key string, count int64) ([][]byte, error) {
	body := nsKey(namespace, key)
	body["count"] = count
	return c.cacheMembers(cacheVariant("SRandMember", body), "SRandMember")
}

// CachePipeline runs operations in order in one round trip, each exactly as if
// it had been sent alone. A failing operation does not stop the rest.
func (c *Client) CachePipeline(ops ...CacheCommand) ([]CacheReply, error) {
	if err := checkCommands(ops); err != nil {
		return nil, err
	}
	return c.cacheReplies(cacheVariant("Pipeline", map[string]any{"ops": ops}), "Pipeline")
}

// CacheTransaction runs operations in order holding the cache write lock
// throughout, so no other writer interleaves with them.
//
// Every operation's inputs are checked before any runs. After that there is no
// rollback: one that fails is reported and the rest still run.
func (c *Client) CacheTransaction(ops ...CacheCommand) ([]CacheReply, error) {
	if err := checkCommands(ops); err != nil {
		return nil, err
	}
	return c.cacheReplies(cacheVariant("Transaction", map[string]any{"ops": ops}), "Transaction")
}

// CacheScriptLoad stores a script: operations that run as one transaction,
// where any string or value written $1, $2, ... is replaced by that argument
// at run time. Loading the same name again replaces it.
func (c *Client) CacheScriptLoad(name string, ops ...CacheCommand) error {
	if err := checkCommands(ops); err != nil {
		return err
	}
	_, err := c.request(cacheVariant("ScriptLoad", map[string]any{"name": name, "ops": ops}))
	return err
}

// CacheScriptRun runs a stored script with these arguments as one transaction.
func (c *Client) CacheScriptRun(name string, args ...string) ([]CacheReply, error) {
	if args == nil {
		args = []string{}
	}
	return c.cacheReplies(cacheVariant("ScriptRun", map[string]any{"name": name, "args": args}), "ScriptRun")
}

// CachePoll returns the messages after id after on these channels. It never
// blocks.
//
// A channel name ending in * is a pattern, and __keyspace__* or __keyevent__*
// turns keyspace notifications on while it is polled. limit <= 0 leaves the
// cap to the server (100).
func (c *Client) CachePoll(after uint64, limit int, channels ...string) (*PollResult, error) {
	body := map[string]any{"channels": strs(channels), "after": after}
	if limit > 0 {
		body["limit"] = limit
	}
	var out PollResult
	if err := c.cacheJSON(cacheVariant("Poll", body), "Poll", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheHotKeys returns the most frequently addressed keys on this node since
// it started. limit <= 0 leaves the cap to the server.
//
// The server answers only when [cache] hot_keys is above zero in its
// configuration, and refuses otherwise. The payload is returned as the server
// sent it.
func (c *Client) CacheHotKeys(limit int) (json.RawMessage, error) {
	body := map[string]any{}
	if limit > 0 {
		body["limit"] = limit
	}
	var out json.RawMessage
	return out, c.cacheJSON(cacheVariant("HotKeys", body), "HotKeys", &out)
}

// CacheSetNamespaceLimit caps a namespace at maxKeys live keys and maxBytes of
// values. A write that would pass either is refused. Zero means no cap of that
// kind, and both zero removes the limit.
func (c *Client) CacheSetNamespaceLimit(namespace string, maxKeys, maxBytes uint64) (*NamespaceLimit, error) {
	body := map[string]any{"namespace": namespace, "max_keys": nil, "max_bytes": nil}
	if maxKeys > 0 {
		body["max_keys"] = maxKeys
	}
	if maxBytes > 0 {
		body["max_bytes"] = maxBytes
	}
	var out NamespaceLimit
	if err := c.cacheJSON(cacheVariant("SetNamespaceLimit", body), "SetNamespaceLimit", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func keysBody(namespace string, keys []string) map[string]any {
	return map[string]any{"namespace": namespace, "keys": strs(keys)}
}

// strs keeps a nil slice from reaching the wire as null where the server
// expects a list.
func strs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CacheMSet writes many values as one batch, all with the same optional TTL,
// and returns how many were written. A repeated key is refused by the server
// rather than resolved last-wins. ttlMs <= 0 means no expiry.
func (c *Client) CacheMSet(namespace string, entries []CacheEntry, ttlMs int64) (int64, error) {
	body := map[string]any{
		"namespace": namespace,
		"entries":   entryList(entries),
		"ttl_ms":    ttlOrNull(ttlMs),
	}
	var out struct {
		Set int64 `json:"set"`
	}
	return out.Set, c.cacheJSON(cacheVariant("MSet", body), "MSet", &out)
}

// CacheWarm writes the entries that are not already cached, leaving live values
// alone. ttlMs <= 0 means no expiry.
func (c *Client) CacheWarm(namespace string, entries []CacheEntry, ttlMs int64) (*WarmResult, error) {
	body := map[string]any{
		"namespace": namespace,
		"entries":   entryList(entries),
		"ttl_ms":    ttlOrNull(ttlMs),
	}
	var out WarmResult
	if err := c.cacheJSON(cacheVariant("Warm", body), "Warm", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheSetIf writes a value under a condition, saying what happens to the
// deadline. False means the condition was not met, not that anything failed.
// ExpiryKeep is Redis's KEEPTTL; ExpiryClear is what a plain set does.
func (c *Client) CacheSetIf(namespace, key string, value []byte, condition SetCondition, expiry CacheExpiry) (bool, error) {
	if err := expiry.check(); err != nil {
		return false, err
	}
	body := nsKey(namespace, key)
	body["value"] = byteList(value)
	body["condition"] = condition
	body["expiry"] = expiry
	var out struct {
		Set bool `json:"set"`
	}
	return out.Set, c.cacheJSON(cacheVariant("SetIf", body), "SetIf", &out)
}

// CacheGetDel reads a key and deletes it in one atomic step. found is false
// when it was absent.
func (c *Client) CacheGetDel(namespace, key string) ([]byte, bool, error) {
	return c.cacheValue(cacheVariant("GetDel", nsKey(namespace, key)), "GetDel")
}

// CacheGetSet writes a value and returns the one it replaced, atomically. The
// old value's TTL is dropped; ttlMs > 0 sets one. found is false when there was
// no previous value.
func (c *Client) CacheGetSet(namespace, key string, value []byte, ttlMs int64) ([]byte, bool, error) {
	body := nsKey(namespace, key)
	body["value"] = byteList(value)
	body["ttl_ms"] = ttlOrNull(ttlMs)
	return c.cacheValue(cacheVariant("GetSet", body), "GetSet")
}

// CacheGetEx reads a key and changes its deadline in the same atomic step.
func (c *Client) CacheGetEx(namespace, key string, expiry CacheExpiry) ([]byte, bool, error) {
	if err := expiry.check(); err != nil {
		return nil, false, err
	}
	body := nsKey(namespace, key)
	body["expiry"] = expiry
	return c.cacheValue(cacheVariant("GetEx", body), "GetEx")
}

// CacheAppend concatenates bytes onto a key's value, creating it when absent,
// and returns the new length. An existing key keeps its expiry.
func (c *Client) CacheAppend(namespace, key string, value []byte) (int64, error) {
	body := nsKey(namespace, key)
	body["value"] = byteList(value)
	var out struct {
		Length int64 `json:"length"`
	}
	return out.Length, c.cacheJSON(cacheVariant("Append", body), "Append", &out)
}

// CacheExpireAt sets the instant an existing live key dies, in Unix epoch
// milliseconds. A deadline already passed deletes the key.
func (c *Client) CacheExpireAt(namespace, key string, atMs int64) (*ExpireAtResult, error) {
	body := nsKey(namespace, key)
	body["at_ms"] = atMs
	var out ExpireAtResult
	if err := c.cacheJSON(cacheVariant("ExpireAt", body), "ExpireAt", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheSetFresh writes a value that reads as fresh for freshMs and as stale
// after that, until its TTL (if any) ends. It returns when the value stops
// being fresh, in Unix epoch milliseconds.
func (c *Client) CacheSetFresh(namespace, key string, value []byte, freshMs, ttlMs int64) (int64, error) {
	body := nsKey(namespace, key)
	body["value"] = byteList(value)
	body["fresh_ms"] = freshMs
	body["ttl_ms"] = ttlOrNull(ttlMs)
	var out struct {
		FreshUntilMs int64 `json:"freshUntilMs"`
	}
	return out.FreshUntilMs, c.cacheJSON(cacheVariant("SetFresh", body), "SetFresh", &out)
}

// CacheSetMissing remembers for ttlMs that the source has no value for key:
// negative caching.
func (c *Client) CacheSetMissing(namespace, key string, ttlMs int64) error {
	body := nsKey(namespace, key)
	body["ttl_ms"] = ttlMs
	_, err := c.request(cacheVariant("SetMissing", body))
	return err
}

// CacheZRem removes members from a sorted set, returning how many were there.
func (c *Client) CacheZRem(namespace, key string, members ...[]byte) (int64, error) {
	membersList, err := byteLists(members, "members")
	if err != nil {
		return 0, err
	}
	body := nsKey(namespace, key)
	body["members"] = membersList
	var out struct {
		Removed int64 `json:"removed"`
	}
	return out.Removed, c.cacheJSON(cacheVariant("ZRem", body), "ZRem", &out)
}

// CacheZCard returns how many members the sorted set holds; zero when the key
// is missing.
func (c *Client) CacheZCard(namespace, key string) (int64, error) {
	var out struct {
		Cardinality int64 `json:"cardinality"`
	}
	return out.Cardinality, c.cacheJSON(cacheVariant("ZCard", nsKey(namespace, key)), "ZCard", &out)
}

// CacheLockAcquire takes a lease on a lock, or refreshes one owner already
// holds. Acquiring a lock you hold returns the same token with a refreshed
// lease, so a retried acquire is safe.
func (c *Client) CacheLockAcquire(namespace, key, owner string, ttlMs int64) (*CacheLock, error) {
	body := nsKey(namespace, key)
	body["owner"] = owner
	body["ttl_ms"] = ttlMs
	var out CacheLock
	if err := c.cacheJSON(cacheVariant("LockAcquire", body), "LockAcquire", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheLockRenew extends a lease the caller still holds.
func (c *Client) CacheLockRenew(namespace, key, owner string, ttlMs int64) (*CacheLockRenewal, error) {
	body := nsKey(namespace, key)
	body["owner"] = owner
	body["ttl_ms"] = ttlMs
	var out CacheLockRenewal
	if err := c.cacheJSON(cacheVariant("LockRenew", body), "LockRenew", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheLockRelease gives up a lock. False when owner is not its live holder.
func (c *Client) CacheLockRelease(namespace, key, owner string) (bool, error) {
	body := nsKey(namespace, key)
	body["owner"] = owner
	var out struct {
		Released bool `json:"released"`
	}
	return out.Released, c.cacheJSON(cacheVariant("LockRelease", body), "LockRelease", &out)
}

// CacheBitCount counts the set bits, optionally within a byte range. A nil
// range counts the whole value.
func (c *Client) CacheBitCount(namespace, key string, r *ByteRange) (int64, error) {
	body := nsKey(namespace, key)
	body["range"] = byteRange(r)
	var out struct {
		Count int64 `json:"count"`
	}
	return out.Count, c.cacheJSON(cacheVariant("BitCount", body), "BitCount", &out)
}

// CacheBitPos returns the position of the first bit equal to bit, optionally
// within a byte range, or -1 when there is none.
func (c *Client) CacheBitPos(namespace, key string, bit bool, r *ByteRange) (int64, error) {
	body := nsKey(namespace, key)
	body["bit"] = bit
	body["range"] = byteRange(r)
	var out struct {
		Position int64 `json:"position"`
	}
	return out.Position, c.cacheJSON(cacheVariant("BitPos", body), "BitPos", &out)
}

// CacheBitOp combines several values bitwise, stores the result at dest, and
// returns its size in bytes. Shorter sources are zero-padded to the longest.
func (c *Client) CacheBitOp(namespace string, kind BitOpKind, dest string, keys ...string) (int64, error) {
	body := map[string]any{
		"namespace": namespace,
		"op":        kind,
		"dest":      dest,
		"keys":      strs(keys),
	}
	var out struct {
		Length int64 `json:"length"`
	}
	return out.Length, c.cacheJSON(cacheVariant("BitOp", body), "BitOp", &out)
}

// CachePfAdd adds elements to a HyperLogLog, creating it when absent. It
// reports whether the estimate may have moved.
func (c *Client) CachePfAdd(namespace, key string, elements ...[]byte) (bool, error) {
	elementsList, err := byteLists(elements, "elements")
	if err != nil {
		return false, err
	}
	body := nsKey(namespace, key)
	body["elements"] = elementsList
	var out struct {
		Changed bool `json:"changed"`
	}
	return out.Changed, c.cacheJSON(cacheVariant("PfAdd", body), "PfAdd", &out)
}

// CachePfCount returns the estimated number of distinct elements. With several
// keys, the estimate is of their union.
func (c *Client) CachePfCount(namespace string, keys ...string) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	return out.Count, c.cacheJSON(cacheVariant("PfCount", map[string]any{"namespace": namespace, "keys": strs(keys)}), "PfCount", &out)
}

// CachePfMerge merges several HyperLogLogs into dest, which counts as a source
// when it already exists.
func (c *Client) CachePfMerge(namespace, dest string, sources ...string) error {
	body := map[string]any{
		"namespace": namespace,
		"dest":      dest,
		"sources":   strs(sources),
	}
	_, err := c.request(cacheVariant("PfMerge", body))
	return err
}

// CacheLMove atomically pops an element from one end of source and pushes it
// onto one end of destination. found is false when source is empty. The two may
// be the same key, which rotates the list.
func (c *Client) CacheLMove(namespace, source, destination string, from, to ListEnd) ([]byte, bool, error) {
	body := map[string]any{
		"namespace":   namespace,
		"source":      source,
		"destination": destination,
		"from":        from,
		"to":          to,
	}
	return c.cacheValue(cacheVariant("LMove", body), "LMove")
}

// CacheLTrim keeps only the inclusive index range of a list, dropping the rest.
// A range selecting nothing empties the list, which deletes the key.
func (c *Client) CacheLTrim(namespace, key string, start, stop int64) (*TrimResult, error) {
	body := nsKey(namespace, key)
	body["start"] = start
	body["stop"] = stop
	var out TrimResult
	if err := c.cacheJSON(cacheVariant("LTrim", body), "LTrim", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheSUnion returns every member of every named set, without duplicates. A
// missing key is the empty set.
func (c *Client) CacheSUnion(namespace string, keys ...string) ([][]byte, error) {
	return c.cacheMembers(cacheVariant("SUnion", map[string]any{"namespace": namespace, "keys": strs(keys)}), "SUnion")
}

// CacheSInter returns the members present in every named set.
func (c *Client) CacheSInter(namespace string, keys ...string) ([][]byte, error) {
	return c.cacheMembers(cacheVariant("SInter", map[string]any{"namespace": namespace, "keys": strs(keys)}), "SInter")
}

// CacheSDiff returns the members of the first set that are in none of the rest.
func (c *Client) CacheSDiff(namespace string, keys ...string) ([][]byte, error) {
	return c.cacheMembers(cacheVariant("SDiff", map[string]any{"namespace": namespace, "keys": strs(keys)}), "SDiff")
}

// CacheHIncrBy adds by to one hash field, atomically, and returns the new
// value. A missing field starts at zero.
func (c *Client) CacheHIncrBy(namespace, key string, field []byte, by int64) (int64, error) {
	body := nsKey(namespace, key)
	body["field"] = byteList(field)
	body["by"] = by
	var out struct {
		Value int64 `json:"value"`
	}
	return out.Value, c.cacheJSON(cacheVariant("HIncrBy", body), "HIncrBy", &out)
}

// CachePublish appends a message to a channel, returning the id it was given.
func (c *Client) CachePublish(channel, message string) (uint64, error) {
	var out struct {
		Id uint64 `json:"id"`
	}
	return out.Id, c.cacheJSON(cacheVariant("Publish", map[string]any{"channel": channel, "message": message}), "Publish", &out)
}

// CacheGetNamespaceLimit returns a namespace's limit and what it holds now.
func (c *Client) CacheGetNamespaceLimit(namespace string) (*NamespaceLimit, error) {
	var out NamespaceLimit
	if err := c.cacheJSON(cacheVariant("GetNamespaceLimit", map[string]any{"namespace": namespace}), "GetNamespaceLimit", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheStats reports what this node's cache is doing: hits and misses, the
// memory budget and the eviction policy. Node-wide, not per database.
func (c *Client) CacheStats() (*CacheStats, error) {
	var out CacheStats
	if err := c.cacheJSON(cacheOp("Stats"), "Stats", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CacheFlushDatabase deletes every cache key in this connection's database,
// across every namespace, returning how many went.
func (c *Client) CacheFlushDatabase() (int64, error) {
	var out struct {
		Cleared int64 `json:"cleared"`
	}
	return out.Cleared, c.cacheJSON(cacheOp("FlushDatabase"), "FlushDatabase", &out)
}

// CachePurgeExpired deletes every expired cache key in this database now,
// rather than leaving it to compaction, returning how many went.
func (c *Client) CachePurgeExpired() (int64, error) {
	var out struct {
		Purged int64 `json:"purged"`
	}
	return out.Purged, c.cacheJSON(cacheOp("PurgeExpired"), "PurgeExpired", &out)
}
