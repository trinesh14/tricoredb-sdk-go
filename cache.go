package tricoredb

import (
	"encoding/json"
	"fmt"
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
