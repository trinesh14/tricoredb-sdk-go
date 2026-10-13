package tricoredb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"testing"
	"time"
)

// The exact request each 0.3.0 method sends, and how it reads the answer.
//
// A scripted peer completes the handshake, records the one request it is sent
// and plays back a canned answer. The answers are the payloads a 0.1.0-rc.3
// server returned for the same operation, so each case proves two things: the
// operation is spelled the way the server spells it, and the server's real
// answer decodes. No server is needed.

// sent is the request a capturePeer recorded.
type sent struct {
	Op      json.RawMessage `json:"op"`
	Options json.RawMessage `json:"options"`
}

// exchange runs call against a peer that answers its one request with data,
// and returns the request the peer received beside what the call returned.
func exchange(t *testing.T, data string, call func(c *Client) error) (sent, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan sent, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			f, err := readFrame(conn)
			if err != nil {
				return
			}
			switch f.tag {
			case tagHello:
				_ = writeFrame(conn, tagHelloOK, map[string]any{"ok": true, "features": FeatureSessionTxn})
			case tagAuth:
				_ = writeFrame(conn, tagAuthOK, map[string]any{"ok": true, "session_id": "s1"})
			case tagClose:
				_ = writeFrame(conn, tagBye, map[string]any{})
				return
			case tagRequest:
				var req sent
				_ = json.Unmarshal(f.body, &req)
				got <- req
				_ = writeFrame(conn, tagResponse, map[string]any{
					"request_id": "r1", "status": "ok", "data": json.RawMessage(data),
				})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := Connect(ctx, Options{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "admin", Secret: "pw",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	callErr := call(db)
	select {
	case req := <-got:
		return req, callErr
	case <-time.After(5 * time.Second):
		t.Fatalf("the peer saw no request (the call returned %v)", callErr)
		return sent{}, nil
	}
}

func sameJSON(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: the request is not JSON: %v (%s)", what, err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: the expectation is not JSON: %v (%s)", what, err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s\n sent: %s\n want: %s", what, got, want)
	}
}

// requestCase is one method call, the op it must send, and the server's answer
// to decode. call returns an error when what it decoded is not what data held.
type requestCase struct {
	name string
	op   string
	data string
	call func(c *Client) error
}

func runCases(t *testing.T, cases []requestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := exchange(t, tc.data, tc.call)
			if err != nil {
				t.Fatalf("the server's own answer did not decode as expected: %v", err)
			}
			sameJSON(t, "op", req.Op, tc.op)
			if len(req.Options) != 0 {
				t.Errorf("no options were asked for, sent %s", req.Options)
			}
		})
	}
}

// want turns a failed expectation into the error a requestCase returns.
func want(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func f32(v float32) *float32 { return &v }
func yes() *bool             { b := true; return &b }
func no() *bool              { b := false; return &b }

func TestCacheRequestsAddedForRc3(t *testing.T) {
	entries := []CacheEntry{{Key: "a", Value: []byte{1}}, {Key: "b", Value: []byte{2}}}
	pipelineOps := []CacheCommand{
		CmdSet("n", "p", []byte{1}, 0), CmdGet("n", "p"), CmdIncr("n", "p", 1), CmdRaw("Ping", nil),
		CmdExists("n", "p"), CmdDelete("n", "p"), CmdRaw("LLen", map[string]any{"namespace": "n", "key": "l"}),
	}
	wireOps := `[{"Set":{"namespace":"n","key":"p","value":[1],"ttl_ms":null}},{"Get":{"namespace":"n","key":"p"}},
		{"Incr":{"namespace":"n","key":"p","by":1}},"Ping",{"Exists":{"namespace":"n","key":"p"}},
		{"Delete":{"namespace":"n","key":"p"}},{"LLen":{"namespace":"n","key":"l"}}]`
	results := `{"Json":{"results":[{"ok":{"Json":{"set":true}}},{"ok":{"CacheValue":[1]}},
		{"error":"invalid request: cache incr: value at n:p is not a valid i64 integer"},{"ok":{"Message":"pong"}}]}}`
	replies := func(r []CacheReply, err error) error {
		if err != nil {
			return err
		}
		v, found := r[1].Value()
		return want(len(r) == 4 && r[0].OK() && r[0].DataKind == "Json" && found && v[0] == 1 &&
			!r[2].OK() && r[3].OK(), "replies %+v", r)
	}
	members := `{"Json":{"count":2,"members":[[1],[2]]}}`
	two := func(m [][]byte, err error) error {
		if err != nil {
			return err
		}
		return want(len(m) == 2 && m[0][0] == 1 && m[1][0] == 2, "members %v", m)
	}
	limit := `{"Json":{"bytes":0,"keys":0,"max_bytes":100,"max_keys":null,"namespace":"n"}}`

	runCases(t, []requestCase{
		{"MGet", `{"Cache":{"MGet":{"namespace":"n","keys":["a","zz","a"]}}}`, `{"CacheValues":[[1],null,[]]}`,
			func(c *Client) error {
				v, err := c.CacheMGet("n", "a", "zz", "a")
				if err != nil {
					return err
				}
				return want(len(v) == 3 && v[0][0] == 1 && v[1] == nil && v[2] != nil && len(v[2]) == 0, "values %v", v)
			}},
		{"MSet", `{"Cache":{"MSet":{"namespace":"n","entries":[{"key":"a","value":[1]},{"key":"b","value":[2]}],"ttl_ms":null}}}`,
			`{"Json":{"set":2}}`, func(c *Client) error {
				n, err := c.CacheMSet("n", entries, 0)
				return errors.Join(err, want(n == 2, "set %d", n))
			}},
		{"Warm", `{"Cache":{"Warm":{"namespace":"n","entries":[{"key":"a","value":[1]},{"key":"b","value":[2]}],"ttl_ms":5000}}}`,
			`{"Json":{"alreadyCached":1,"written":1}}`, func(c *Client) error {
				r, err := c.CacheWarm("n", entries, 5000)
				if err != nil {
					return err
				}
				return want(r.Written == 1 && r.AlreadyCached == 1, "warm %+v", r)
			}},
		{"SetIf present with a ttl", `{"Cache":{"SetIf":{"namespace":"n","key":"k","value":[5],"condition":"IfPresent","expiry":{"Set":{"ttl_ms":60000}}}}}`,
			`{"Json":{"set":true}}`, func(c *Client) error {
				ok, err := c.CacheSetIf("n", "k", []byte{5}, SetIfPresent, ExpirySet(60000))
				return errors.Join(err, want(ok, "set"))
			}},
		{"SetIf absent keeping the ttl", `{"Cache":{"SetIf":{"namespace":"n","key":"k","value":[5],"condition":"IfAbsent","expiry":"Keep"}}}`,
			`{"Json":{"set":false}}`, func(c *Client) error {
				ok, err := c.CacheSetIf("n", "k", []byte{5}, SetIfAbsent, ExpiryKeep())
				return errors.Join(err, want(!ok, "set"))
			}},
		{"SetIf always clearing the ttl", `{"Cache":{"SetIf":{"namespace":"n","key":"k","value":[5],"condition":"Always","expiry":"Clear"}}}`,
			`{"Json":{"set":true}}`, func(c *Client) error {
				_, err := c.CacheSetIf("n", "k", []byte{5}, SetAlways, ExpiryClear())
				return err
			}},
		{"CompareAndSwap mismatch", `{"Cache":{"CompareAndSwap":{"namespace":"n","key":"k","expected":[1],"value":[2],"ttl_ms":5000}}}`,
			`{"Json":{"current":"hi","swapped":false}}`, func(c *Client) error {
				r, err := c.CacheCompareAndSwap("n", "k", []byte{1}, []byte{2}, 5000)
				if err != nil {
					return err
				}
				return want(!r.Swapped && string(r.Current) == "hi", "cas %+v", r)
			}},
		{"CompareAndSwap absent and delete", `{"Cache":{"CompareAndSwap":{"namespace":"n","key":"k","expected":null,"value":null,"ttl_ms":null}}}`,
			`{"Json":{"currentIsBinary":true,"swapped":false}}`, func(c *Client) error {
				r, err := c.CacheCompareAndSwap("n", "k", nil, nil, 0)
				if err != nil {
					return err
				}
				return want(r.CurrentIsBinary && r.Current == nil, "cas %+v", r)
			}},
		{"GetDel", `{"Cache":{"GetDel":{"namespace":"n","key":"k"}}}`, `{"CacheValue":[2]}`, func(c *Client) error {
			v, found, err := c.CacheGetDel("n", "k")
			return errors.Join(err, want(found && len(v) == 1 && v[0] == 2, "value %v", v))
		}},
		{"GetSet", `{"Cache":{"GetSet":{"namespace":"n","key":"k","value":[1],"ttl_ms":null}}}`, `{"CacheValue":null}`,
			func(c *Client) error {
				_, found, err := c.CacheGetSet("n", "k", []byte{1}, 0)
				return errors.Join(err, want(!found, "found"))
			}},
		{"GetEx", `{"Cache":{"GetEx":{"namespace":"n","key":"k","expiry":"Clear"}}}`, `{"CacheValue":[2]}`,
			func(c *Client) error {
				_, found, err := c.CacheGetEx("n", "k", ExpiryClear())
				return errors.Join(err, want(found, "found"))
			}},
		{"Append", `{"Cache":{"Append":{"namespace":"n","key":"k","value":[1,2]}}}`, `{"Json":{"length":2}}`,
			func(c *Client) error {
				n, err := c.CacheAppend("n", "k", []byte{1, 2})
				return errors.Join(err, want(n == 2, "length %d", n))
			}},
		{"ExpireAt", `{"Cache":{"ExpireAt":{"namespace":"n","key":"k","at_ms":4102444800000}}}`,
			`{"Json":{"deleted":false,"updated":true}}`, func(c *Client) error {
				r, err := c.CacheExpireAt("n", "k", 4102444800000)
				if err != nil {
					return err
				}
				return want(r.Updated && !r.Deleted, "expire %+v", r)
			}},
		{"SetFresh", `{"Cache":{"SetFresh":{"namespace":"n","key":"k","value":[1,2],"fresh_ms":60000,"ttl_ms":null}}}`,
			`{"Json":{"freshUntilMs":1791870307508,"set":true}}`, func(c *Client) error {
				until, err := c.CacheSetFresh("n", "k", []byte{1, 2}, 60000, 0)
				return errors.Join(err, want(until == 1791870307508, "until %d", until))
			}},
		{"GetFresh text", `{"Cache":{"GetFresh":{"namespace":"n","key":"k","owner":"w1","lease_ms":1000}}}`,
			`{"Json":{"refresh":true,"state":"stale","value":"hi"}}`, func(c *Client) error {
				r, err := c.CacheGetFresh("n", "k", "w1", 1000)
				if err != nil {
					return err
				}
				return want(r.State == FreshStateStale && r.Refresh && string(r.Value) == "hi", "read %+v", r)
			}},
		{"GetFresh binary", `{"Cache":{"GetFresh":{"namespace":"n","key":"k","owner":"w1","lease_ms":1000}}}`,
			`{"Json":{"state":"fresh","valueHex":"fffe00"}}`, func(c *Client) error {
				r, err := c.CacheGetFresh("n", "k", "w1", 1000)
				if err != nil {
					return err
				}
				return want(r.State == FreshStateFresh && reflect.DeepEqual(r.Value, []byte{0xff, 0xfe, 0}), "read %+v", r)
			}},
		{"GetFresh miss", `{"Cache":{"GetFresh":{"namespace":"n","key":"k","owner":"w1","lease_ms":1000}}}`,
			`{"Json":{"lease":false,"retryAfterMs":991,"state":"miss"}}`, func(c *Client) error {
				r, err := c.CacheGetFresh("n", "k", "w1", 1000)
				if err != nil {
					return err
				}
				return want(r.State == FreshStateMiss && !r.Lease && r.RetryAfterMs == 991 && r.Value == nil, "read %+v", r)
			}},
		{"SetMissing", `{"Cache":{"SetMissing":{"namespace":"n","key":"k","ttl_ms":60000}}}`, `{"Json":{"set":true}}`,
			func(c *Client) error { return c.CacheSetMissing("n", "k", 60000) }},
		{"ZAdd", `{"Cache":{"ZAdd":{"namespace":"n","key":"z","members":[{"score":1.5,"member":[97]},{"score":2,"member":[98]}]}}}`,
			`{"Json":{"added":2,"updated":0}}`, func(c *Client) error {
				r, err := c.CacheZAdd("n", "z", ScoredMember{1.5, []byte("a")}, ScoredMember{2, []byte("b")})
				if err != nil {
					return err
				}
				return want(r.Added == 2 && r.Updated == 0, "zadd %+v", r)
			}},
		{"ZRem", `{"Cache":{"ZRem":{"namespace":"n","key":"z","members":[[97],[1]]}}}`, `{"Json":{"removed":1}}`,
			func(c *Client) error {
				n, err := c.CacheZRem("n", "z", []byte("a"), []byte{1})
				return errors.Join(err, want(n == 1, "removed %d", n))
			}},
		{"ZScore", `{"Cache":{"ZScore":{"namespace":"n","key":"z","member":[97]}}}`, `{"Json":{"score":9.0}}`,
			func(c *Client) error {
				s, found, err := c.CacheZScore("n", "z", []byte("a"))
				return errors.Join(err, want(found && s == 9, "score %v", s))
			}},
		{"ZScore absent", `{"Cache":{"ZScore":{"namespace":"n","key":"z","member":[97]}}}`, `{"Json":{"score":null}}`,
			func(c *Client) error {
				_, found, err := c.CacheZScore("n", "z", []byte("a"))
				return errors.Join(err, want(!found, "found"))
			}},
		{"ZCard", `{"Cache":{"ZCard":{"namespace":"n","key":"z"}}}`, `{"Json":{"cardinality":3}}`,
			func(c *Client) error {
				n, err := c.CacheZCard("n", "z")
				return errors.Join(err, want(n == 3, "cardinality %d", n))
			}},
		{"ZRank", `{"Cache":{"ZRank":{"namespace":"n","key":"z","member":[97],"rev":true}}}`, `{"Json":{"rank":0}}`,
			func(c *Client) error {
				r, found, err := c.CacheZRank("n", "z", []byte("a"), true)
				return errors.Join(err, want(found && r == 0, "rank %d %v", r, found))
			}},
		{"ZRange", `{"Cache":{"ZRange":{"namespace":"n","key":"z","start":0,"stop":-1,"rev":false,"with_scores":false}}}`,
			members, func(c *Client) error { return two(c.CacheZRange("n", "z", 0, -1, false)) }},
		{"ZRangeWithScores", `{"Cache":{"ZRange":{"namespace":"n","key":"z","start":0,"stop":-1,"rev":true,"with_scores":true}}}`,
			`{"Json":{"count":1,"members":[{"member":[97],"score":9.0}]}}`, func(c *Client) error {
				m, err := c.CacheZRangeWithScores("n", "z", 0, -1, true)
				if err != nil {
					return err
				}
				return want(len(m) == 1 && m[0].Score == 9 && string(m[0].Member) == "a", "members %+v", m)
			}},
		{"ZRangeByScore", `{"Cache":{"ZRangeByScore":{"namespace":"n","key":"z","min":{"value":2,"exclusive":true},
			"max":{"value":1.7976931348623157e308,"exclusive":false},"rev":false,"with_scores":false,"limit":[0,5]}}}`,
			members, func(c *Client) error {
				return two(c.CacheZRangeByScore("n", "z", ScoreBound{Value: 2, Exclusive: true}, ScoreHighest(),
					ScoreRangeOptions{Count: 5}))
			}},
		{"ZRangeByScoreWithScores", `{"Cache":{"ZRangeByScore":{"namespace":"n","key":"z",
			"min":{"value":-1.7976931348623157e308,"exclusive":false},"max":{"value":100,"exclusive":false},
			"rev":true,"with_scores":true,"limit":null}}}`,
			`{"Json":{"count":1,"members":[{"member":[99],"score":3.0}]}}`, func(c *Client) error {
				m, err := c.CacheZRangeByScoreWithScores("n", "z", ScoreLowest(), ScoreBound{Value: 100},
					ScoreRangeOptions{Rev: true})
				if err != nil {
					return err
				}
				return want(len(m) == 1 && m[0].Score == 3, "members %+v", m)
			}},
		{"ZIncrBy", `{"Cache":{"ZIncrBy":{"namespace":"n","key":"z","member":[98],"by":2.5}}}`, `{"Json":{"score":4.5}}`,
			func(c *Client) error {
				s, err := c.CacheZIncrBy("n", "z", []byte("b"), 2.5)
				return errors.Join(err, want(s == 4.5, "score %v", s))
			}},
		{"LockAcquire", `{"Cache":{"LockAcquire":{"namespace":"n","key":"lk","owner":"o1","ttl_ms":30000}}}`,
			`{"Json":{"acquired":true,"expiresAtMs":1791870277688,"holder":"o1","token":1}}`, func(c *Client) error {
				l, err := c.CacheLockAcquire("n", "lk", "o1", 30000)
				if err != nil {
					return err
				}
				return want(l.Acquired && l.Token == 1 && l.Holder == "o1" && l.ExpiresAtMs == 1791870277688, "lock %+v", l)
			}},
		{"LockAcquire contended", `{"Cache":{"LockAcquire":{"namespace":"n","key":"lk","owner":"o2","ttl_ms":30000}}}`,
			`{"Json":{"acquired":false,"expiresAtMs":1791870277688,"holder":"o1","token":null}}`, func(c *Client) error {
				l, err := c.CacheLockAcquire("n", "lk", "o2", 30000)
				if err != nil {
					return err
				}
				return want(!l.Acquired && l.Token == 0 && l.Holder == "o1", "lock %+v", l)
			}},
		{"LockRenew", `{"Cache":{"LockRenew":{"namespace":"n","key":"lk","owner":"o2","ttl_ms":30000}}}`,
			`{"Json":{"expiresAtMs":null,"renewed":false,"token":null}}`, func(c *Client) error {
				r, err := c.CacheLockRenew("n", "lk", "o2", 30000)
				if err != nil {
					return err
				}
				return want(!r.Renewed && r.Token == 0, "renewal %+v", r)
			}},
		{"LockRelease", `{"Cache":{"LockRelease":{"namespace":"n","key":"lk","owner":"o1"}}}`, `{"Json":{"released":true}}`,
			func(c *Client) error {
				ok, err := c.CacheLockRelease("n", "lk", "o1")
				return errors.Join(err, want(ok, "released"))
			}},
		{"SetBit", `{"Cache":{"SetBit":{"namespace":"n","key":"b","offset":7,"value":true}}}`, `{"Json":{"previous":1}}`,
			func(c *Client) error {
				prev, err := c.CacheSetBit("n", "b", 7, true)
				return errors.Join(err, want(prev, "previous"))
			}},
		{"GetBit", `{"Cache":{"GetBit":{"namespace":"n","key":"b","offset":7}}}`, `{"Json":{"bit":0}}`,
			func(c *Client) error {
				bit, err := c.CacheGetBit("n", "b", 7)
				return errors.Join(err, want(!bit, "bit"))
			}},
		{"BitCount in a range", `{"Cache":{"BitCount":{"namespace":"n","key":"b","range":[0,-1]}}}`, `{"Json":{"count":1}}`,
			func(c *Client) error {
				n, err := c.CacheBitCount("n", "b", &ByteRange{Start: 0, End: -1})
				return errors.Join(err, want(n == 1, "count %d", n))
			}},
		{"BitCount", `{"Cache":{"BitCount":{"namespace":"n","key":"b","range":null}}}`, `{"Json":{"count":1}}`,
			func(c *Client) error {
				_, err := c.CacheBitCount("n", "b", nil)
				return err
			}},
		{"BitPos", `{"Cache":{"BitPos":{"namespace":"n","key":"b","bit":true,"range":null}}}`, `{"Json":{"position":-1}}`,
			func(c *Client) error {
				pos, err := c.CacheBitPos("n", "b", true, nil)
				return errors.Join(err, want(pos == -1, "position %d", pos))
			}},
		{"BitOp", `{"Cache":{"BitOp":{"namespace":"n","op":"Xor","dest":"d","keys":["a","b"]}}}`, `{"Json":{"length":1}}`,
			func(c *Client) error {
				n, err := c.CacheBitOp("n", BitXor, "d", "a", "b")
				return errors.Join(err, want(n == 1, "length %d", n))
			}},
		{"PfAdd", `{"Cache":{"PfAdd":{"namespace":"n","key":"h","elements":[[1],[2]]}}}`, `{"Json":{"changed":true}}`,
			func(c *Client) error {
				changed, err := c.CachePfAdd("n", "h", []byte{1}, []byte{2})
				return errors.Join(err, want(changed, "changed"))
			}},
		{"PfCount", `{"Cache":{"PfCount":{"namespace":"n","keys":["h","h2"]}}}`, `{"Json":{"count":4}}`,
			func(c *Client) error {
				n, err := c.CachePfCount("n", "h", "h2")
				return errors.Join(err, want(n == 4, "count %d", n))
			}},
		{"PfMerge", `{"Cache":{"PfMerge":{"namespace":"n","dest":"h3","sources":["h","h2"]}}}`, `{"Json":{"merged":true}}`,
			func(c *Client) error { return c.CachePfMerge("n", "h3", "h", "h2") }},
		{"LMove", `{"Cache":{"LMove":{"namespace":"n","source":"a","destination":"b","from":"Left","to":"Right"}}}`,
			`{"CacheValue":[1]}`, func(c *Client) error {
				v, found, err := c.CacheLMove("n", "a", "b", ListLeft, ListRight)
				return errors.Join(err, want(found && v[0] == 1, "value %v", v))
			}},
		{"LTrim", `{"Cache":{"LTrim":{"namespace":"n","key":"a","start":0,"stop":0}}}`, `{"Json":{"length":1,"removed":1}}`,
			func(c *Client) error {
				r, err := c.CacheLTrim("n", "a", 0, 0)
				if err != nil {
					return err
				}
				return want(r.Length == 1 && r.Removed == 1, "trim %+v", r)
			}},
		{"BLPop", `{"Cache":{"BLPop":{"namespace":"n","keys":["a","l2"],"timeout_ms":100}}}`, `{"CacheValues":[[108,50],[1]]}`,
			func(c *Client) error {
				key, v, found, err := c.CacheBLPop("n", 100, "a", "l2")
				return errors.Join(err, want(found && key == "l2" && v[0] == 1, "popped %q %v", key, v))
			}},
		{"BRPop timing out", `{"Cache":{"BRPop":{"namespace":"n","keys":["a"],"timeout_ms":100}}}`, `{"CacheValues":[null,null]}`,
			func(c *Client) error {
				_, _, found, err := c.CacheBRPop("n", 100, "a")
				return errors.Join(err, want(!found, "found"))
			}},
		{"SUnion", `{"Cache":{"SUnion":{"namespace":"n","keys":["s1","s2"]}}}`, members,
			func(c *Client) error { return two(c.CacheSUnion("n", "s1", "s2")) }},
		{"SInter", `{"Cache":{"SInter":{"namespace":"n","keys":["s1","s2"]}}}`, members,
			func(c *Client) error { return two(c.CacheSInter("n", "s1", "s2")) }},
		{"SDiff", `{"Cache":{"SDiff":{"namespace":"n","keys":["s1","s2"]}}}`, members,
			func(c *Client) error { return two(c.CacheSDiff("n", "s1", "s2")) }},
		{"SRandMember", `{"Cache":{"SRandMember":{"namespace":"n","key":"s1","count":null}}}`, members,
			func(c *Client) error { return two(c.CacheSRandMember("n", "s1")) }},
		{"SRandMembers", `{"Cache":{"SRandMember":{"namespace":"n","key":"s1","count":-3}}}`, members,
			func(c *Client) error { return two(c.CacheSRandMembers("n", "s1", -3)) }},
		{"HIncrBy", `{"Cache":{"HIncrBy":{"namespace":"n","key":"h","field":[110],"by":5}}}`, `{"Json":{"value":5}}`,
			func(c *Client) error {
				n, err := c.CacheHIncrBy("n", "h", []byte("n"), 5)
				return errors.Join(err, want(n == 5, "value %d", n))
			}},
		{"Pipeline", `{"Cache":{"Pipeline":{"ops":` + wireOps + `}}}`, results,
			func(c *Client) error { return replies(c.CachePipeline(pipelineOps...)) }},
		{"Transaction", `{"Cache":{"Transaction":{"ops":` + wireOps + `}}}`, results,
			func(c *Client) error { return replies(c.CacheTransaction(pipelineOps...)) }},
		{"ScriptLoad", `{"Cache":{"ScriptLoad":{"name":"s","ops":` + wireOps + `}}}`, `{"Json":{"loaded":"s","ops":7}}`,
			func(c *Client) error { return c.CacheScriptLoad("s", pipelineOps...) }},
		{"ScriptRun", `{"Cache":{"ScriptRun":{"name":"s","args":["k1","k2"]}}}`, results,
			func(c *Client) error { return replies(c.CacheScriptRun("s", "k1", "k2")) }},
		{"ScriptRun without arguments", `{"Cache":{"ScriptRun":{"name":"s","args":[]}}}`, results,
			func(c *Client) error { return replies(c.CacheScriptRun("s")) }},
		{"Publish", `{"Cache":{"Publish":{"channel":"c","message":"hello"}}}`, `{"Json":{"channel":"c","id":1}}`,
			func(c *Client) error {
				id, err := c.CachePublish("c", "hello")
				return errors.Join(err, want(id == 1, "id %d", id))
			}},
		{"Poll", `{"Cache":{"Poll":{"channels":["c","d*"],"after":1,"limit":10}}}`,
			`{"Json":{"messages":[{"at_ms":1791870248123,"channel":"c","id":2,"message":"again"}],"next":2}}`,
			func(c *Client) error {
				r, err := c.CachePoll(1, 10, "c", "d*")
				if err != nil {
					return err
				}
				return want(r.Next == 2 && len(r.Messages) == 1 && r.Messages[0].Message == "again" &&
					r.Messages[0].Channel == "c" && r.Messages[0].AtMs == 1791870248123, "poll %+v", r)
			}},
		{"Poll with the server's limit", `{"Cache":{"Poll":{"channels":["c"],"after":0}}}`, `{"Json":{"messages":[],"next":0}}`,
			func(c *Client) error {
				_, err := c.CachePoll(0, 0, "c")
				return err
			}},
		{"Stats", `{"Cache":"Stats"}`, `{"Json":{"eviction":{"evicted":0,"policy":"lru"},"hitRatio":0.75,"hits":3,
			"memory":{"enabled":true,"lowWaterBytes":241591860,"maxBytes":268435456,"measurements":0,"residentBytes":null},"misses":1}}`,
			func(c *Client) error {
				s, err := c.CacheStats()
				if err != nil {
					return err
				}
				return want(s.Hits == 3 && s.Misses == 1 && *s.HitRatio == 0.75 && s.Eviction.Policy == "lru" &&
					*s.Memory.MaxBytes == 268435456 && s.Memory.ResidentBytes == nil, "stats %+v", s)
			}},
		{"FlushDatabase", `{"Cache":"FlushDatabase"}`, `{"Json":{"cleared":1}}`, func(c *Client) error {
			n, err := c.CacheFlushDatabase()
			return errors.Join(err, want(n == 1, "cleared %d", n))
		}},
		{"PurgeExpired", `{"Cache":"PurgeExpired"}`, `{"Json":{"purged":0}}`, func(c *Client) error {
			_, err := c.CachePurgeExpired()
			return err
		}},
		// The payload is passed through: the live server refused this
		// operation, so its shape was never observed.
		{"HotKeys", `{"Cache":{"HotKeys":{"limit":3}}}`, `{"Json":{"anything":1}}`, func(c *Client) error {
			raw, err := c.CacheHotKeys(3)
			return errors.Join(err, want(string(raw) == `{"anything":1}`, "payload %s", raw))
		}},
		{"HotKeys with the server's limit", `{"Cache":{"HotKeys":{}}}`, `{"Json":{}}`, func(c *Client) error {
			_, err := c.CacheHotKeys(0)
			return err
		}},
		{"SetNamespaceLimit", `{"Cache":{"SetNamespaceLimit":{"namespace":"n","max_keys":null,"max_bytes":100}}}`, limit,
			func(c *Client) error {
				l, err := c.CacheSetNamespaceLimit("n", 0, 100)
				if err != nil {
					return err
				}
				return want(l.MaxKeys == nil && *l.MaxBytes == 100 && l.Namespace == "n", "limit %+v", l)
			}},
		{"GetNamespaceLimit", `{"Cache":{"GetNamespaceLimit":{"namespace":"n"}}}`, limit, func(c *Client) error {
			_, err := c.CacheGetNamespaceLimit("n")
			return err
		}},
	})
}

func TestDocumentRequestsAddedForRc3(t *testing.T) {
	plan := `{"Json":{"elapsed_us":49,"estimated_cost":1000,"estimated_rows":1000,"index":null,"index_used":false,
		"plan":"FindScan","rows_examined":4,"rows_returned":1,"scan_reason":"no index on field ` + "`kind`" + `","scan_source":"memory_index"}}`
	zero := 0.0
	options := CollectionOptions{
		Schema: &DocumentSchema{
			Required:         []string{"n"},
			Fields:           map[string]FieldRule{"n": {Type: FieldNumber, Minimum: &zero}},
			AdditionalFields: yes(),
		},
		Defaults:  map[string]any{"kind": "z"},
		Versioned: true,
	}
	wireOptions := `{"schema":{"required":["n"],"fields":{"n":{"type":"number","minimum":0}},"additional_fields":true},
		"defaults":{"kind":"z"},"versioned":true}`

	runCases(t, []requestCase{
		{"Replace", `{"Document":{"Replace":{"collection":"c","id":"a","document":{"n":1},"upsert":false}}}`,
			`{"Json":{"id":"a","inserted":false,"replaced":true}}`, func(c *Client) error {
				r, err := c.DocumentReplace("c", "a", map[string]any{"n": 1}, ReplaceOptions{})
				if err != nil {
					return err
				}
				return want(r.Replaced && !r.Inserted && r.Document == nil, "replace %+v", r)
			}},
		{"Replace with every option", `{"Document":{"Replace":{"collection":"c","id":"a","document":{"n":1},"upsert":true,"return":"After","expected_version":0}}}`,
			`{"Json":{"document":{"_id":"a","_version":1,"n":1},"id":"a","inserted":true,"replaced":true}}`, func(c *Client) error {
				r, err := c.DocumentReplace("c", "a", map[string]any{"n": 1},
					ReplaceOptions{Upsert: true, Return: ReturnAfter, ExpectedVersion: ExpectVersion(0)})
				if err != nil {
					return err
				}
				return want(r.Inserted && r.Document["_version"] == float64(1), "replace %+v", r)
			}},
		{"InsertMany", `{"Document":{"InsertMany":{"collection":"c","documents":[{"_id":"a","n":1},{"n":2}]}}}`,
			`{"Json":{"ids":["a","71528832217926cb"],"inserted":2}}`, func(c *Client) error {
				ids, err := c.DocumentInsertMany("c", map[string]any{"_id": "a", "n": 1}, map[string]any{"n": 2})
				return errors.Join(err, want(reflect.DeepEqual(ids, []string{"a", "71528832217926cb"}), "ids %v", ids))
			}},
		{"DeleteMany", `{"Document":{"DeleteMany":{"collection":"c","filter":{"Gte":{"field":"n","value":100}}}}}`,
			`{"Json":{"deleted":1}}`, func(c *Client) error {
				n, err := c.DocumentDeleteMany("c", FilterGte("n", 100))
				return errors.Join(err, want(n == 1, "deleted %d", n))
			}},
		{"BulkWrite", `{"Document":{"BulkWrite":{"collection":"c","ops":[
			{"Insert":{"id":"b1","document":{"n":1}}},
			{"Insert":{"id":null,"document":{"n":2}}},
			{"UpdateOne":{"id":"b1","update":{"set":{"n":5}}}},
			{"UpdateOne":{"id":"b2","update":{"inc":{"n":1}},"upsert":true,"expected_version":0}},
			{"Replace":{"id":"b","document":{"n":20},"expected_version":2}},
			{"Delete":{"id":"old"}}]}}}`,
			`{"Json":{"deleted":1,"inserted":2,"inserted_ids":["b1","08d167b34ddc29df"],"replaced":1,"updated":1,"upserted":1}}`,
			func(c *Client) error {
				r, err := c.DocumentBulkWrite("c",
					BulkInsertWithID("b1", map[string]any{"n": 1}),
					BulkInsert(map[string]any{"n": 2}),
					BulkUpdateOne("b1", DocumentUpdate{Set: map[string]any{"n": 5}}),
					BulkUpdateOne("b2", DocumentUpdate{Inc: map[string]any{"n": 1}}).Upsert().ExpectedVersion(0),
					BulkReplace("b", map[string]any{"n": 20}).ExpectedVersion(2),
					BulkDelete("old").Upsert(),
				)
				if err != nil {
					return err
				}
				return want(r.Inserted == 2 && r.Updated == 1 && r.Upserted == 1 && r.Replaced == 1 && r.Deleted == 1 &&
					len(r.InsertedIDs) == 2, "bulk %+v", r)
			}},
		{"RenameCollection", `{"Document":{"RenameCollection":{"collection":"c","to":"d"}}}`,
			`{"Json":{"documents":5,"from":"c","indexes":1,"renamed":true,"to":"d"}}`,
			func(c *Client) error { return c.DocumentRenameCollection("c", "d") }},
		{"PurgeExpired", `{"Document":{"PurgeExpired":{"collection":"c"}}}`, `{"Json":{"more":false,"purged":0}}`,
			func(c *Client) error {
				r, err := c.DocumentPurgeExpired("c")
				if err != nil {
					return err
				}
				return want(r.Purged == 0 && !r.More, "purge %+v", r)
			}},
		{"Explain", `{"Document":{"Explain":{"collection":"c","filter":{"Eq":{"field":"kind","value":"x"}}}}}`, plan,
			func(c *Client) error {
				p, err := c.DocumentExplain("c", FilterEq("kind", "x"), ExplainOptions{})
				if err != nil {
					return err
				}
				return want(p.Plan == "FindScan" && p.RowsExamined == 4 && p.RowsReturned == 1 && !p.IndexUsed &&
					p.Index == "" && p.ScanReason != "" && p.EstimatedRows == 1000, "plan %+v", p)
			}},
		{"Explain with every option", `{"Document":{"Explain":{"collection":"c","filter":"All","limit":2,
			"sort":[{"field":"n","descending":true}],"skip":1}}}`, plan,
			func(c *Client) error {
				_, err := c.DocumentExplain("c", FilterAll(),
					ExplainOptions{Limit: 2, Sort: []SortKey{{Field: "n", Descending: true}}, Skip: 1})
				return err
			}},
		{"RebuildIndex", `{"Document":{"RebuildIndex":{"collection":"c","index_name":"by_kind"}}}`,
			`{"Json":{"documents":4,"entries":2,"rebuilt":"by_kind"}}`, func(c *Client) error {
				r, err := c.DocumentRebuildIndex("c", "by_kind")
				if err != nil {
					return err
				}
				return want(r.IndexName == "by_kind" && r.Documents == 4 && r.Entries == 2, "rebuild %+v", r)
			}},
		{"SetCollectionOptions", `{"Document":{"SetCollectionOptions":{"collection":"c","options":` + wireOptions + `}}}`,
			`{"Json":{"documents_checked":4,"updated":true}}`, func(c *Client) error {
				n, err := c.DocumentSetCollectionOptions("c", options)
				return errors.Join(err, want(n == 4, "checked %d", n))
			}},
		{"GetCollectionOptions", `{"Document":{"GetCollectionOptions":{"collection":"c"}}}`,
			`{"Json":{"collection":"c","options":` + wireOptions + `}}`, func(c *Client) error {
				got, err := c.DocumentGetCollectionOptions("c")
				if err != nil {
					return err
				}
				return want(reflect.DeepEqual(*got, options), "what was set is what reads back: %+v", got)
			}},
		{"BeginTransaction", `{"Document":{"BeginTransaction":{"serializable":true}}}`,
			`{"Json":{"serializable":true,"txn":"dtx-1-2"}}`, func(c *Client) error {
				txn, err := c.DocumentBeginTransaction(true)
				return errors.Join(err, want(txn == "dtx-1-2", "txn %q", txn))
			}},
	})
}

func TestVectorRequestsAddedForRc3(t *testing.T) {
	hnsw := `{"Json":{"collection":"c","degree":{"max":29,"mean":10.5,"min":2},"entry":"v280","index":"hnsw","layers":3,
		"live":300,"live_per_layer":[300,17,1],"memory":{"overhead_bytes":64922,"vector_bytes":9600},"nodes":300,
		"parameters":{"ef_construction":200,"ef_search":64,"m":16,"m0":32},"persisted":true,"reachable":300,
		"should_rebuild":false,"stored":300,"tombstones":0,"unreachable":0}}`

	runCases(t, []requestCase{
		{"Embed", `{"Vector":{"Embed":{"texts":["hello world"],"dimension":4}}}`,
			`{"Json":{"dimension":4,"embedder":"hash-v1","sparse":[{"indices":[822003,11189515],"values":[0.7,0.7]}],"vectors":[[0.5,0.5,0.0,-0.5]]}}`,
			func(c *Client) error {
				e, err := c.VectorEmbed([]string{"hello world"}, 4)
				if err != nil {
					return err
				}
				return want(e.Embedder == "hash-v1" && len(e.Vectors[0]) == 4 && e.Sparse[0].Indices[0] == 822003, "embedding %+v", e)
			}},
		{"UpsertText", `{"Vector":{"UpsertText":{"collection":"c","items":[{"id":"t1","text":"hello","metadata":{"kind":"t"}},{"id":"t2","text":"bye"}]}}}`,
			`{"Json":{"embedder":"hash-v1","upserted":2}}`, func(c *Client) error {
				n, err := c.VectorUpsertText("c", []TextUpsert{
					{ID: "t1", Text: "hello", Metadata: map[string]any{"kind": "t"}}, {ID: "t2", Text: "bye"},
				})
				return errors.Join(err, want(n == 2, "upserted %d", n))
			}},
		{"UpsertMany", `{"Vector":{"UpsertMany":{"collection":"c","items":[{"id":"a","vector":[1,0],"metadata":{"kind":"x"}},{"id":"b","vector":[0,1]}]}}}`,
			`{"Json":{"upserted":2}}`, func(c *Client) error {
				n, err := c.VectorUpsertMany("c", []VectorUpsertItem{
					{ID: "a", Vector: []float32{1, 0}, Metadata: map[string]any{"kind": "x"}}, {ID: "b", Vector: []float32{0, 1}},
				})
				return errors.Join(err, want(n == 2, "upserted %d", n))
			}},
		{"UpsertNamed", `{"Vector":{"UpsertNamed":{"collection":"c","id":"n1","vectors":{"default":[1,1],"title":[1]},"metadata":{"kind":"named"}}}}`,
			`{"Json":{"id":"n1","vectors":2}}`, func(c *Client) error {
				return c.VectorUpsertNamed("c", "n1", map[string][]float32{"default": {1, 1}, "title": {1}},
					map[string]any{"kind": "named"})
			}},
		{"AddNamedVector", `{"Vector":{"AddNamedVector":{"collection":"c","name":"title","dimension":2,"metric":"dot"}}}`,
			`{"Json":{"collection":"c","dimension":2,"name":"title"}}`,
			func(c *Client) error { return c.VectorAddNamedVector("c", "title", 2, MetricDot) }},
		{"DeleteMany", `{"Vector":{"DeleteMany":{"collection":"c","ids":["a","nosuch"]}}}`, `{"Json":{"deleted":1}}`,
			func(c *Client) error {
				n, err := c.VectorDeleteMany("c", "a", "nosuch")
				return errors.Join(err, want(n == 1, "deleted %d", n))
			}},
		{"DeleteByFilter", `{"Vector":{"DeleteByFilter":{"collection":"c","filter":{"kind":"y"}}}}`, `{"Json":{"deleted":1}}`,
			func(c *Client) error {
				n, err := c.VectorDeleteByFilter("c", map[string]any{"kind": "y"})
				return errors.Join(err, want(n == 1, "deleted %d", n))
			}},
		{"UpdateByFilter", `{"Vector":{"UpdateByFilter":{"collection":"c","filter":{"kind":"x"},"set":{"seen":true}}}}`,
			`{"Json":{"updated":1}}`, func(c *Client) error {
				n, err := c.VectorUpdateByFilter("c", map[string]any{"kind": "x"}, map[string]any{"seen": true})
				return errors.Join(err, want(n == 1, "updated %d", n))
			}},
		{"Count", `{"Vector":{"Count":{"collection":"c","filter":{"kind":"x"}}}}`, `{"Json":{"count":1,"filtered":true}}`,
			func(c *Client) error {
				n, err := c.VectorCount("c", map[string]any{"kind": "x"})
				return errors.Join(err, want(n == 1, "count %d", n))
			}},
		{"Count of everything", `{"Vector":{"Count":{"collection":"c"}}}`, `{"Json":{"count":5,"filtered":false}}`,
			func(c *Client) error {
				n, err := c.VectorCount("c", nil)
				return errors.Join(err, want(n == 5, "count %d", n))
			}},
		{"SearchBatch", `{"Vector":{"SearchBatch":{"collection":"c","vectors":[[1,0],[0,1]],"top_k":2}}}`,
			`{"Json":{"count":2,"results":[[{"id":"a","metadata":{"kind":"x"},"score":1.0}],[{"id":"b","metadata":null,"score":1.0}]]}}`,
			func(c *Client) error {
				r, err := c.VectorSearchBatch("c", [][]float32{{1, 0}, {0, 1}}, 2, VectorSearchOptions{})
				if err != nil {
					return err
				}
				return want(len(r) == 2 && r[0][0].ID == "a" && r[1][0].ID == "b" && r[1][0].Vector == nil, "lists %+v", r)
			}},
		{"SearchBatch with options", `{"Vector":{"SearchBatch":{"collection":"c","vectors":[[1,0]],"top_k":2,"filter":{"kind":"x"},
			"min_score":0.5,"include_vectors":true,"include_metadata":false,"offset":1,"max_distance":2,"ef_search":50,"exact":true,
			"score_metric":"l2","filter_strategy":"exact","using":"title","tenant":"t","namespace":"ns",
			"sparse":{"indices":[1],"values":[0.5]},"hybrid_alpha":0.25,"explain":true,"score_expr":"score","explain_text":"q",
			"ann":"ivf_pq","nprobe":4}}}`,
			`{"Json":{"count":1,"results":[[{"id":"a","reason":"cosine similarity 1.000 (very close)","reason_source":"rules","score":1.0,"vector":[1.0,0.0]}]]}}`,
			func(c *Client) error {
				r, err := c.VectorSearchBatch("c", [][]float32{{1, 0}}, 2, VectorSearchOptions{
					Filter: map[string]any{"kind": "x"}, MinScore: f32(0.5), IncludeVectors: true, OmitMetadata: true,
					Offset: 1, MaxDistance: f32(2), EfSearch: 50, Exact: true, ScoreMetric: ScoreL2,
					FilterStrategy: FilterStrategyExact, Using: "title", Tenant: "t", Namespace: "ns",
					Sparse: &SparseVector{Indices: []uint32{1}, Values: []float32{0.5}}, HybridAlpha: f32(0.25),
					Explain: true, ScoreExpr: "score", ExplainText: "q", Ann: AnnIvfPq, Nprobe: 4,
				})
				if err != nil {
					return err
				}
				return want(len(r[0][0].Vector) == 2 && r[0][0].Reason != "", "hit %+v", r[0][0])
			}},
		{"SearchMulti", `{"Vector":{"SearchMulti":{"collection":"c","vectors":[[1,0],[0,1]],"top_k":3,"fusion":"max","filter":{"kind":"x"}}}}`,
			`{"Json":{"depth":12,"fusion":"max","queries":2,"results":[{"id":"a","matched":2,"metadata":{"kind":"x"},"score":1.0}]}}`,
			func(c *Client) error {
				r, err := c.VectorSearchMulti("c", [][]float32{{1, 0}, {0, 1}}, 3, FusionMax, map[string]any{"kind": "x"})
				if err != nil {
					return err
				}
				return want(r.Fusion == FusionMax && r.Queries == 2 && r.Depth == 12 && r.Results[0].Matched == 2, "fused %+v", r)
			}},
		{"SearchMulti with the server's fusion", `{"Vector":{"SearchMulti":{"collection":"c","vectors":[[1,0]],"top_k":3}}}`,
			`{"Json":{"depth":12,"fusion":"rrf","queries":1,"results":[]}}`, func(c *Client) error {
				_, err := c.VectorSearchMulti("c", [][]float32{{1, 0}}, 3, "", nil)
				return err
			}},
		{"SearchByExamples", `{"Vector":{"SearchByExamples":{"collection":"c","positive":["a",[0,1]],"negative":["c"],"top_k":3}}}`,
			`{"Json":{"examples":{"negative":1,"positive":2},"index":"flat","results":[{"id":"b","metadata":{"kind":"y"},"score":0.5}],"top_k":5}}`,
			func(c *Client) error {
				r, err := c.VectorSearchByExamples("c", ExampleSearch{
					Positive: []VectorExample{ExampleID("a"), ExampleVector([]float32{0, 1})},
					Negative: []VectorExample{ExampleID("c")},
				}, 3, VectorSearchOptions{})
				if err != nil {
					return err
				}
				return want(r.Index == "flat" && r.Results[0].ID == "b", "result %+v", r)
			}},
		{"SearchByExamples keeping the examples", `{"Vector":{"SearchByExamples":{"collection":"c","positive":["a"],"top_k":3,"exclude_examples":false,"using":"title"}}}`,
			`{"Json":{"index":"flat","results":[],"top_k":3}}`, func(c *Client) error {
				_, err := c.VectorSearchByExamples("c",
					ExampleSearch{Positive: []VectorExample{ExampleID("a")}, IncludeExamples: true}, 3,
					VectorSearchOptions{Using: "title"})
				return err
			}},
		{"SetAlias", `{"Vector":{"SetAlias":{"alias":"al","collection":"c"}}}`, `{"Json":{"alias":"al","collection":"c"}}`,
			func(c *Client) error { return c.VectorSetAlias("al", "c") }},
		{"SetAlias removing it", `{"Vector":{"SetAlias":{"alias":"al","collection":null}}}`, `{"Json":{"alias":"al","collection":null}}`,
			func(c *Client) error { return c.VectorSetAlias("al", "") }},
		{"ListAliases", `{"Vector":"ListAliases"}`, `{"Json":{"aliases":[{"alias":"al","collection":"c"}]}}`,
			func(c *Client) error {
				a, err := c.VectorListAliases()
				return errors.Join(err, want(len(a) == 1 && a[0].Alias == "al" && a[0].Collection == "c", "aliases %+v", a))
			}},
		{"ListPartitions", `{"Vector":{"ListPartitions":{"collection":"c","field":"_tenant"}}}`,
			`{"Json":{"field":"_tenant","partitions":[{"count":2,"name":"n1"}],"unpartitioned":3}}`, func(c *Client) error {
				p, err := c.VectorListPartitions("c", "_tenant")
				if err != nil {
					return err
				}
				return want(p.Field == "_tenant" && p.Partitions[0].Name == "n1" && p.Partitions[0].Count == 2 &&
					p.Unpartitioned == 3, "partitions %+v", p)
			}},
		{"ListPartitions by the server's field", `{"Vector":{"ListPartitions":{"collection":"c"}}}`,
			`{"Json":{"field":"_namespace","partitions":[],"unpartitioned":5}}`, func(c *Client) error {
				_, err := c.VectorListPartitions("c", "")
				return err
			}},
		{"RenameCollection", `{"Vector":{"RenameCollection":{"collection":"c","to":"d"}}}`,
			`{"Json":{"from":"c","moved":3,"renamed":true,"to":"d"}}`, func(c *Client) error {
				n, err := c.VectorRenameCollection("c", "d")
				return errors.Join(err, want(n == 3, "moved %d", n))
			}},
		{"CopyCollection", `{"Vector":{"CopyCollection":{"collection":"c","to":"d"}}}`, `{"Json":{"copied":3,"to":"d"}}`,
			func(c *Client) error {
				n, err := c.VectorCopyCollection("c", "d")
				return errors.Join(err, want(n == 3, "copied %d", n))
			}},
		{"RebuildIndex", `{"Vector":{"RebuildIndex":{"collection":"c"}}}`,
			`{"Json":{"build_ms":28,"persisted":true,"rebuilt":true,"tombstones_cleared":0,"vectors":6}}`, func(c *Client) error {
				r, err := c.VectorRebuildIndex("c")
				if err != nil {
					return err
				}
				return want(r.Rebuilt && r.Persisted && r.Vectors == 6 && r.BuildMs == 28, "rebuild %+v", r)
			}},
		{"IndexStats of a scanned collection", `{"Vector":{"IndexStats":{"collection":"c"}}}`,
			`{"Json":{"collection":"c","flat_scan_threshold":256,"index":"flat","stored":6}}`, func(c *Client) error {
				s, err := c.VectorIndexStats("c")
				if err != nil {
					return err
				}
				return want(s.Index == "flat" && s.Stored == 6 && s.FlatScanThreshold == 256 && s.Layers == 0, "stats %+v", s)
			}},
		{"IndexStats of a graph", `{"Vector":{"IndexStats":{"collection":"c"}}}`, hnsw, func(c *Client) error {
			s, err := c.VectorIndexStats("c")
			if err != nil {
				return err
			}
			return want(s.Index == "hnsw" && s.Layers == 3 && s.Reachable == 300 && s.Entry == "v280" &&
				reflect.DeepEqual(s.LivePerLayer, []int64{300, 17, 1}) && s.Degree.Max == 29 &&
				s.Memory.VectorBytes == 9600 && s.Parameters.M0 == 32 && s.Persisted, "stats %+v", s)
		}},
		{"MeasureRecall", `{"Vector":{"MeasureRecall":{"collection":"c","sample":5,"top_k":3,"ef_search":50}}}`,
			`{"Json":{"collection":"c","ef_search":64,"flat_fallbacks":0,"index":"hnsw","measured":true,"min_recall":1.0,"queries":5,"recall":1.0,"top_k":3}}`,
			func(c *Client) error {
				r, err := c.VectorMeasureRecall("c", RecallOptions{Sample: 5, TopK: 3, EfSearch: 50})
				if err != nil {
					return err
				}
				return want(r.Measured && r.Recall == 1 && r.Queries == 5 && r.TopK == 3, "recall %+v", r)
			}},
		{"MeasureRecall with the server's defaults", `{"Vector":{"MeasureRecall":{"collection":"c"}}}`,
			`{"Json":{"collection":"c","index":"flat","measured":false,"reason":"the collection is small enough that searches scan every vector","recall":1.0}}`,
			func(c *Client) error {
				r, err := c.VectorMeasureRecall("c", RecallOptions{})
				if err != nil {
					return err
				}
				return want(!r.Measured && r.Reason != "", "recall %+v", r)
			}},
		{"SetCollectionConfig", `{"Vector":{"SetCollectionConfig":{"collection":"c","normalize":true,"m":8,"ef_construction":100,"multi_tenant":false}}}`,
			`{"Json":{"collection":"c","ef_construction":100,"m":8,"multi_tenant":false,"named":[{"dimension":2,"metric":"dot","name":"title"}],"normalize":true}}`,
			func(c *Client) error {
				cfg, err := c.VectorSetCollectionConfig("c",
					VectorConfigChange{Normalize: yes(), M: 8, EfConstruction: 100, MultiTenant: no()})
				if err != nil {
					return err
				}
				return want(cfg.M == 8 && cfg.EfConstruction == 100 && cfg.Normalize && cfg.Named[0].Metric == MetricDot, "config %+v", cfg)
			}},
	})
}

func TestGraphRequestsAddedForRc3(t *testing.T) {
	walk := TraverseOptions{Direction: DirectionBoth, Label: "K", MaxDepth: 2, Limit: 10}
	summary := `{"Json":{"graph":"g","labelsAdded":0,"labelsRemoved":0,"nodesCreated":1,"nodesDeleted":0,"propertiesSet":0,
		"relationshipsCreated":0,"relationshipsDeleted":0,"statements":1}}`

	runCases(t, []requestCase{
		{"AddMany", `{"Graph":{"AddMany":{"graph":"g","nodes":[{"id":"a","labels":["P"],"properties":{"name":"ada"}},{"id":"b","labels":[]}],
			"edges":[{"id":"e1","from":"a","to":"b","label":"K","properties":{"w":2}},{"id":"e2","from":"b","to":"a","label":"K"}]}}}`,
			`{"Json":{"atomic":true,"edges":2,"nodes":2}}`, func(c *Client) error {
				r, err := c.GraphAddMany("g",
					[]GraphNodeInput{{ID: "a", Labels: []string{"P"}, Properties: map[string]any{"name": "ada"}}, {ID: "b"}},
					[]GraphEdgeInput{{ID: "e1", From: "a", To: "b", Label: "K", Properties: map[string]any{"w": 2}},
						{ID: "e2", From: "b", To: "a", Label: "K"}})
				if err != nil {
					return err
				}
				return want(r.Nodes == 2 && r.Edges == 2, "counts %+v", r)
			}},
		{"AddMany of nodes only", `{"Graph":{"AddMany":{"graph":"g","nodes":[{"id":"a","labels":[]}],"edges":[]}}}`,
			`{"Json":{"atomic":true,"edges":0,"nodes":1}}`, func(c *Client) error {
				_, err := c.GraphAddMany("g", []GraphNodeInput{{ID: "a"}}, nil)
				return err
			}},
		{"RemoveMany", `{"Graph":{"RemoveMany":{"graph":"g","nodes":["a"],"edges":[],"detach":true}}}`,
			`{"Json":{"atomic":true,"edges_removed":4,"nodes_removed":1}}`, func(c *Client) error {
				r, err := c.GraphRemoveMany("g", []string{"a"}, nil, true)
				if err != nil {
					return err
				}
				return want(r.NodesRemoved == 1 && r.EdgesRemoved == 4, "removed %+v", r)
			}},
		{"UpdateNode", `{"Graph":{"UpdateNode":{"graph":"g","id":"a","properties":{"age":null,"city":"x"},"add_labels":["Q"],"remove_labels":["P"]}}}`,
			`{"Json":{"id":"a","labels":["Q"],"properties":{"city":"x","name":"ada"}}}`, func(c *Client) error {
				n, err := c.GraphUpdateNode("g", "a", NodeUpdate{
					Properties: map[string]any{"age": nil, "city": "x"}, AddLabels: []string{"Q"}, RemoveLabels: []string{"P"},
				})
				if err != nil {
					return err
				}
				return want(n.ID == "a" && reflect.DeepEqual(n.Labels, []string{"Q"}) && n.Properties["city"] == "x", "node %+v", n)
			}},
		{"UpdateNode changing nothing", `{"Graph":{"UpdateNode":{"graph":"g","id":"a"}}}`,
			`{"Json":{"id":"a","labels":[],"properties":null}}`, func(c *Client) error {
				_, err := c.GraphUpdateNode("g", "a", NodeUpdate{})
				return err
			}},
		{"UpdateEdge", `{"Graph":{"UpdateEdge":{"graph":"g","id":"e1","properties":{"w":3},"label":"KNOWS"}}}`,
			`{"Json":{"from":"a","id":"e1","label":"KNOWS","properties":{"w":3},"to":"b"}}`, func(c *Client) error {
				e, err := c.GraphUpdateEdge("g", "e1", map[string]any{"w": 3}, "KNOWS")
				if err != nil {
					return err
				}
				return want(e.Label == "KNOWS" && e.From == "a" && e.To == "b", "edge %+v", e)
			}},
		{"UpdateEdge changing nothing", `{"Graph":{"UpdateEdge":{"graph":"g","id":"e1"}}}`,
			`{"Json":{"from":"a","id":"e1","label":"K","properties":null,"to":"b"}}`, func(c *Client) error {
				_, err := c.GraphUpdateEdge("g", "e1", nil, "")
				return err
			}},
		{"CopyGraph", `{"Graph":{"CopyGraph":{"graph":"g","to":"g2"}}}`, `{"Json":{"copied":true,"edges":5,"nodes":7,"to":"g2"}}`,
			func(c *Client) error {
				r, err := c.GraphCopy("g", "g2")
				if err != nil {
					return err
				}
				return want(r.Nodes == 7 && r.Edges == 5, "counts %+v", r)
			}},
		{"ImportCsv", `{"Graph":{"ImportCsv":{"graph":"g","nodes":"id\nx\n","edges":"id,from,to,label\ne,x,x,K\n"}}}`,
			`{"Json":{"atomic":true,"edges":1,"nodes":1}}`, func(c *Client) error {
				r, err := c.GraphImportCSV("g", "id\nx\n", "id,from,to,label\ne,x,x,K\n")
				if err != nil {
					return err
				}
				return want(r.Nodes == 1 && r.Edges == 1, "counts %+v", r)
			}},
		{"ImportCsv of nodes only", `{"Graph":{"ImportCsv":{"graph":"g","nodes":"id\nx\n"}}}`,
			`{"Json":{"atomic":true,"edges":0,"nodes":1}}`, func(c *Client) error {
				_, err := c.GraphImportCSV("g", "id\nx\n", "")
				return err
			}},
		{"Dfs", `{"Graph":{"Dfs":{"graph":"g","start":"a","direction":"both","label":"K","max_depth":2,"limit":10}}}`,
			`{"Json":{"count":2,"direction":"both","limit":10,"max_depth":2,"nodes":[{"depth":0,"id":"a","labels":["P"],"properties":{"name":"ada"}},
			{"depth":1,"id":"c","labels":["P"],"properties":null}],"order":"depth_first","start":"a","truncated":true}}`,
			func(c *Client) error {
				r, err := c.GraphDfs("g", "a", walk)
				if err != nil {
					return err
				}
				return want(r.Count == 2 && r.Truncated && r.Nodes[1].Depth == 1, "walk %+v", r)
			}},
		{"Dfs with the server's bounds", `{"Graph":{"Dfs":{"graph":"g","start":"a"}}}`, `{"Json":{"count":0,"nodes":[],"start":"a"}}`,
			func(c *Client) error {
				_, err := c.GraphDfs("g", "a", TraverseOptions{})
				return err
			}},
		{"Subgraph", `{"Graph":{"Subgraph":{"graph":"g","seeds":["a"],"direction":"both","label":"K","max_depth":2,"limit":10}}}`,
			`{"Json":{"direction":"both","edge_count":1,"edges":[{"from":"a","id":"e1","label":"K","properties":{"w":3},"to":"b"}],
			"edges_truncated":false,"limit":10,"max_depth":2,"node_count":2,"nodes":[{"depth":0,"id":"a","labels":["P"],"properties":null},
			{"depth":1,"id":"b","labels":[],"properties":null}],"nodes_truncated":false}}`,
			func(c *Client) error {
				r, err := c.GraphSubgraph("g", []string{"a"}, walk)
				if err != nil {
					return err
				}
				return want(r.NodeCount == 2 && r.EdgeCount == 1 && r.Edges[0].ID == "e1" && r.Nodes[1].ID == "b", "subgraph %+v", r)
			}},
		{"KShortestPaths", `{"Graph":{"KShortestPaths":{"graph":"g","from":"a","to":"c","k":2,"direction":"outgoing","label":"K","weight_property":"w"}}}`,
			`{"Json":{"count":2,"direction":"outgoing","from":"a","k":2,"paths":[{"cost":1.0,"edges":["e4"],"hops":1,"nodes":["a","c"]},
			{"cost":4.0,"edges":["e1","e2"],"hops":2,"nodes":["a","b","c"]}],"to":"c","weight_property":"w"}}`,
			func(c *Client) error {
				r, err := c.GraphKShortestPaths("g", "a", "c",
					KPathOptions{K: 2, Direction: DirectionOutgoing, Label: "K", WeightProperty: "w"})
				if err != nil {
					return err
				}
				return want(r.Count == 2 && r.Paths[1].Cost == 4 && r.Paths[1].Hops == 2 && r.Complete == nil, "paths %+v", r)
			}},
		{"AllPaths", `{"Graph":{"AllPaths":{"graph":"g","from":"a","to":"c","direction":"both","label":"K","max_depth":2,"limit":10}}}`,
			`{"Json":{"complete":true,"count":1,"direction":"both","from":"a","limit":10,"max_depth":2,
			"paths":[{"cost":1.0,"edges":["e4"],"hops":1,"nodes":["a","c"]}],"stopped_by":null,"to":"c"}}`,
			func(c *Client) error {
				r, err := c.GraphAllPaths("g", "a", "c", walk)
				if err != nil {
					return err
				}
				return want(r.Complete != nil && *r.Complete && r.StoppedBy == "" &&
					reflect.DeepEqual(r.Paths[0].Nodes, []string{"a", "c"}), "paths %+v", r)
			}},
		{"ConnectedComponents", `{"Graph":{"ConnectedComponents":{"graph":"g","kind":"strong","label":"K","limit":5}}}`,
			`{"Json":{"components":[{"nodes":["a","b","c"],"size":3,"truncated":false},{"nodes":["d"],"size":1,"truncated":false}],
			"count":2,"kind":"strong","largest":3,"truncated":false}}`,
			func(c *Client) error {
				r, err := c.GraphConnectedComponents("g", ComponentOptions{Kind: ComponentStrong, Label: "K", Limit: 5})
				if err != nil {
					return err
				}
				return want(r.Kind == ComponentStrong && r.Count == 2 && r.Largest == 3 && r.Components[0].Size == 3, "components %+v", r)
			}},
		{"ConnectedComponents with the server's defaults", `{"Graph":{"ConnectedComponents":{"graph":"g"}}}`,
			`{"Json":{"components":[],"count":0,"kind":"weak","largest":0,"truncated":false}}`, func(c *Client) error {
				_, err := c.GraphConnectedComponents("g", ComponentOptions{})
				return err
			}},
		{"DetectCycle", `{"Graph":{"DetectCycle":{"graph":"g"}}}`,
			`{"Json":{"cycle":{"edges":["e1","e2","e3"],"nodes":["a","b","c"]},"directed":true,"hasCycle":true}}`,
			func(c *Client) error {
				r, err := c.GraphDetectCycle("g", false, "")
				if err != nil {
					return err
				}
				return want(r.HasCycle && r.Directed && len(r.Cycle.Edges) == 3, "cycle %+v", r)
			}},
		{"DetectCycle undirected", `{"Graph":{"DetectCycle":{"graph":"g","directed":false,"label":"K"}}}`,
			`{"Json":{"cycle":null,"directed":false,"hasCycle":false}}`, func(c *Client) error {
				r, err := c.GraphDetectCycle("g", true, "K")
				if err != nil {
					return err
				}
				return want(!r.HasCycle && r.Cycle == nil, "cycle %+v", r)
			}},
		{"GraphStats", `{"Graph":{"GraphStats":{"graph":"g"}}}`,
			`{"Json":{"avgOutDegree":1.0,"danglingEdges":0,"edgeTypes":{"K":4},"edges":4,"isolatedNodes":1,"maxInDegree":2,
			"maxOutDegree":2,"nodeLabels":{"P":3,"Q":1},"nodes":4,"selfLoops":0,"unlabeledNodes":1}}`,
			func(c *Client) error {
				s, err := c.GraphStats("g")
				if err != nil {
					return err
				}
				return want(s.Nodes == 4 && s.Edges == 4 && s.IsolatedNodes == 1 && s.NodeLabels["P"] == 3 &&
					s.EdgeTypes["K"] == 4 && s.MaxInDegree == 2 && s.AvgOutDegree == 1 && s.UnlabeledNodes == 1, "stats %+v", s)
			}},
		{"GraphSchema", `{"Graph":{"GraphSchema":{"graph":"g"}}}`,
			`{"Json":{"edge_types":[{"count":4,"properties":[{"count":1,"key":"w","types":["number"]}],"type":"K"}],"edges":4,
			"node_labels":[{"count":3,"label":"P","properties":[{"count":2,"key":"name","types":["string"]}]}],"nodes":4,
			"unlabeled_nodes":{"count":1,"properties":[]}}}`,
			func(c *Client) error {
				s, err := c.GraphSchema("g")
				if err != nil {
					return err
				}
				return want(s.EdgeTypes[0].Type == "K" && s.NodeLabels[0].Properties[0].Key == "name" &&
					s.UnlabeledNodes.Count == 1 && s.Nodes == 4, "schema %+v", s)
			}},
		{"CheckConsistency", `{"Graph":{"CheckConsistency":{"graph":"g"}}}`,
			`{"Json":{"consistent":true,"dangling_edges":{"count":0,"listed":[]},"edges":4,"mirror":{"differences":{"count":0,"listed":[]},
			"loaded":true},"misfiled":{"count":0,"listed":[]},"nodes":4,"undecodable":{"count":0,"listed":[]}}}`,
			func(c *Client) error {
				r, err := c.GraphCheckConsistency("g")
				if err != nil {
					return err
				}
				return want(r.Consistent && r.Mirror.Loaded && r.Nodes == 4 && r.DanglingEdges.Count == 0, "check %+v", r)
			}},
		{"Centrality", `{"Graph":{"Centrality":{"graph":"g","measure":"eigenvector","directed":false,"label":"K","limit":2,"iterations":50,"tolerance":0.0001}}}`,
			`{"Json":{"converged":true,"directed":false,"iterations":10,"measure":"eigenvector","nodes":4,"results":[{"id":"c","score":0.72}]}}`,
			func(c *Client) error {
				r, err := c.GraphCentrality("g", CentralityEigenvector,
					CentralityOptions{Undirected: true, Label: "K", Limit: 2, Iterations: 50, Tolerance: 0.0001})
				if err != nil {
					return err
				}
				return want(r.Measure == CentralityEigenvector && *r.Converged && r.Iterations == 10 && r.Results[0].ID == "c", "scores %+v", r)
			}},
		{"Centrality with the server's defaults", `{"Graph":{"Centrality":{"graph":"g","measure":"betweenness"}}}`,
			`{"Json":{"directed":true,"measure":"betweenness","nodes":4,"results":[{"id":"a","score":0.16}]}}`, func(c *Client) error {
				r, err := c.GraphCentrality("g", CentralityBetweenness, CentralityOptions{})
				if err != nil {
					return err
				}
				return want(r.Converged == nil && r.Directed, "scores %+v", r)
			}},
		{"Communities", `{"Graph":{"Communities":{"graph":"g","label":"K","iterations":5,"limit":2}}}`,
			`{"Json":{"algorithm":"louvain","communities":[{"nodes":["a","b","c"],"size":3,"truncated":false}],"converged":true,"count":2,"iterations":3,"modularity":0.0}}`,
			func(c *Client) error {
				r, err := c.GraphCommunities("g", CommunityOptions{Label: "K", Iterations: 5, Limit: 2})
				if err != nil {
					return err
				}
				return want(r.Algorithm == "louvain" && r.Count == 2 && r.Communities[0].Size == 3, "communities %+v", r)
			}},
		{"TriangleCount", `{"Graph":{"TriangleCount":{"graph":"g","label":"K","limit":2}}}`,
			`{"Json":{"average_clustering":0.75,"nodes":[{"clustering":1.0,"id":"a","triangles":1}],"transitivity":1.0,"triangles":1}}`,
			func(c *Client) error {
				r, err := c.GraphTriangleCount("g", "K", 2)
				if err != nil {
					return err
				}
				return want(r.Triangles == 1 && r.AverageClustering == 0.75 && r.Nodes[0].ID == "a", "triangles %+v", r)
			}},
		{"NodeSimilarity", `{"Graph":{"NodeSimilarity":{"graph":"g","node":"a","direction":"outgoing","label":"K","metric":"overlap","limit":2}}}`,
			`{"Json":{"count":1,"direction":"outgoing","metric":"overlap","node":"a","results":[{"id":"b","score":1.0}]}}`,
			func(c *Client) error {
				r, err := c.GraphNodeSimilarity("g", "a",
					SimilarityOptions{Direction: DirectionOutgoing, Label: "K", Metric: SimilarityOverlap, Limit: 2})
				return errors.Join(err, want(len(r) == 1 && r[0].ID == "b" && r[0].Score == 1, "similar %+v", r))
			}},
		{"PageRank", `{"Graph":{"PageRank":{"graph":"g","damping":0.5,"iterations":10,"tolerance":0.001,"label":"K","limit":2}}}`,
			`{"Json":{"converged":true,"count":4,"damping":0.5,"iterations":6,"ranks":[{"id":"c","score":0.32}],"truncated":true}}`,
			func(c *Client) error {
				r, err := c.GraphPageRank("g", PageRankOptions{Damping: 0.5, Iterations: 10, Tolerance: 0.001, Label: "K", Limit: 2})
				if err != nil {
					return err
				}
				return want(r.Converged && r.Truncated && r.Count == 4 && r.Ranks[0].ID == "c", "ranks %+v", r)
			}},
		{"PageRank with the server's defaults", `{"Graph":{"PageRank":{"graph":"g"}}}`,
			`{"Json":{"converged":false,"count":0,"damping":0.85,"iterations":20,"ranks":[],"truncated":false}}`, func(c *Client) error {
				_, err := c.GraphPageRank("g", PageRankOptions{})
				return err
			}},
		{"CreateNodeIndex", `{"Graph":{"CreateNodeIndex":{"graph":"g","name":"by_name","label":"P","properties":["name"]}}}`,
			`{"Json":{"created":true,"name":"by_name"}}`, func(c *Client) error { return c.GraphCreateNodeIndex("g", "by_name", "P", "name") }},
		{"CreateNodeIndex on every label", `{"Graph":{"CreateNodeIndex":{"graph":"g","name":"by_nc","properties":["name","city"]}}}`,
			`{"Json":{"created":true,"name":"by_nc"}}`, func(c *Client) error { return c.GraphCreateNodeIndex("g", "by_nc", "", "name", "city") }},
		{"CreateConstraint", `{"Graph":{"CreateConstraint":{"graph":"g","name":"u","label":"P","property":"name","kind":"unique"}}}`,
			`{"Json":{"created":true,"kind":"unique","name":"u","target":"node"}}`,
			func(c *Client) error { return c.GraphCreateConstraint("g", "u", "P", "name", ConstraintUnique, "") }},
		{"CreateConstraint on edges", `{"Graph":{"CreateConstraint":{"graph":"g","name":"r","label":"K","property":"w","kind":"required","target":"edge"}}}`,
			`{"Json":{"created":true,"kind":"required","name":"r","target":"edge"}}`,
			func(c *Client) error {
				return c.GraphCreateConstraint("g", "r", "K", "w", ConstraintRequired, TargetEdge)
			}},
		{"DropSchemaObject", `{"Graph":{"DropSchemaObject":{"graph":"g","name":"by_nc"}}}`, `{"Json":{"dropped":true,"name":"by_nc"}}`,
			func(c *Client) error { return c.GraphDropSchemaObject("g", "by_nc") }},
		{"ListSchemaObjects", `{"Graph":{"ListSchemaObjects":{"graph":"g"}}}`,
			`{"Json":{"constraints":[{"kind":"unique","label":"P","name":"u","property":"name","target":"node"}],
			"indexes":[{"label":"P","name":"by_name","properties":["name"]},{"label":null,"name":"by_nc","properties":["name","city"]}]}}`,
			func(c *Client) error {
				r, err := c.GraphListSchemaObjects("g")
				if err != nil {
					return err
				}
				return want(r.Constraints[0].Kind == ConstraintUnique && r.Constraints[0].Target == TargetNode &&
					len(r.Indexes) == 2 && r.Indexes[1].Label == "", "objects %+v", r)
			}},
		{"FindNodes", `{"Graph":{"FindNodes":{"graph":"g","label":"P","properties":{"name":"ada"},"limit":1}}}`,
			`{"Json":{"count":1,"nodes":[{"id":"a","labels":["Q","P"],"properties":{"city":"x","name":"ada"}}],"plan":"index ` + "`by_name`" + `","truncated":false}}`,
			func(c *Client) error {
				r, err := c.GraphFindNodes("g", "P", map[string]any{"name": "ada"}, 1)
				if err != nil {
					return err
				}
				return want(r.Count == 1 && r.Nodes[0].ID == "a" && r.Plan == "index `by_name`", "found %+v", r)
			}},
		{"FindNodes of everything", `{"Graph":{"FindNodes":{"graph":"g"}}}`, `{"Json":{"count":0,"nodes":[],"plan":"scan","truncated":false}}`,
			func(c *Client) error {
				_, err := c.GraphFindNodes("g", "", nil, 0)
				return err
			}},
		{"Execute", `{"Graph":{"Execute":{"graph":"g","cypher":"CREATE (n:P {name: 'zed'})"}}}`, summary, func(c *Client) error {
			r, err := c.GraphExecute("g", "CREATE (n:P {name: 'zed'})")
			if err != nil {
				return err
			}
			return want(r.NodesCreated == 1 && r.Statements == 1 && r.Queued == 0, "summary %+v", r)
		}},
		{"BeginTransaction", `{"Graph":{"BeginTransaction":{"graph":"g"}}}`, `{"Json":{"transaction":"gtx-1-7"}}`,
			func(c *Client) error {
				txn, err := c.GraphBeginTransaction("g")
				return errors.Join(err, want(txn == "gtx-1-7", "txn %q", txn))
			}},
	})
}

// A transaction is named in the request's options, not in its op: these are
// the calls that carry one.
func TestATransactionIsNamedInTheRequestOptions(t *testing.T) {
	cases := []struct {
		name    string
		op      string
		options string
		data    string
		call    func(c *Client) error
	}{
		{"document commit", `{"Document":"CommitTransaction"}`, `{"txn":"dtx-1-2"}`, `{"Json":{"committed":true,"writes":2}}`,
			func(c *Client) error {
				r, err := c.DocumentCommitTransaction("dtx-1-2")
				if err != nil {
					return err
				}
				return want(r.Committed && r.Writes == 2, "commit %+v", r)
			}},
		{"document rollback", `{"Document":"RollbackTransaction"}`, `{"txn":"dtx-1-3"}`, `{"Json":{"rolled_back":true}}`,
			func(c *Client) error { return c.DocumentRollbackTransaction("dtx-1-3") }},
		{"a request inside a transaction, with a deadline",
			`{"Document":{"Insert":{"collection":"c","id":"t1","document":{"n":1}}}}`, `{"timeout_ms":2000,"txn":"dtx-9"}`,
			`{"Json":{"id":"t1","inserted":true}}`, func(c *Client) error {
				c.SetRequestTxn("dtx-9")
				c.SetRequestTimeout(2 * time.Second)
				_, err := c.DocumentInsertWithID("c", "t1", map[string]any{"n": 1})
				return errors.Join(err, want(c.RequestTxn() == "dtx-9", "the transaction is still named"))
			}},
		{"a commit stops naming the transaction", `{"Document":"CommitTransaction"}`, `{"txn":"dtx-9"}`,
			`{"Json":{"committed":true,"writes":0}}`, func(c *Client) error {
				c.SetRequestTxn("dtx-9")
				_, err := c.DocumentCommitTransaction("dtx-9")
				return errors.Join(err, want(c.RequestTxn() == "", "still naming %q", c.RequestTxn()))
			}},
		{"a commit leaves another transaction named", `{"Document":"CommitTransaction"}`, `{"txn":"dtx-1"}`,
			`{"Json":{"committed":true,"writes":0}}`, func(c *Client) error {
				c.SetRequestTxn("dtx-2")
				_, err := c.DocumentCommitTransaction("dtx-1")
				return errors.Join(err, want(c.RequestTxn() == "dtx-2", "naming %q", c.RequestTxn()))
			}},
		{"begin is never sent inside another transaction", `{"Document":{"BeginTransaction":{"serializable":false}}}`, ``,
			`{"Json":{"serializable":false,"txn":"dtx-3"}}`, func(c *Client) error {
				c.SetRequestTxn("dtx-2")
				_, err := c.DocumentBeginTransaction(false)
				return err
			}},
		{"graph execute inside a transaction", `{"Graph":{"Execute":{"graph":"g","cypher":"CREATE (n:P)"}}}`, `{"txn":"gtx-1-7"}`,
			`{"Json":{"queued":1,"transaction":"gtx-1-7"}}`, func(c *Client) error {
				c.SetRequestTxn("gtx-1-7")
				r, err := c.GraphExecute("g", "CREATE (n:P)")
				if err != nil {
					return err
				}
				return want(r.Queued == 1 && r.NodesCreated == 0, "summary %+v", r)
			}},
		{"graph commit", `{"Graph":{"CommitTransaction":{"graph":"g"}}}`, `{"txn":"gtx-1-7"}`,
			`{"Json":{"committed":1,"cypher":"CREATE (n:P)","graph":"g","nodesCreated":1,"statements":1}}`, func(c *Client) error {
				r, err := c.GraphCommitTransaction("g", "gtx-1-7")
				if err != nil {
					return err
				}
				return want(r.Committed == 1 && r.NodesCreated == 1, "summary %+v", r)
			}},
		{"graph rollback", `{"Graph":{"RollbackTransaction":{"graph":"g"}}}`, `{"txn":"gtx-1-8"}`, `{"Json":{"rolled_back":1}}`,
			func(c *Client) error {
				n, err := c.GraphRollbackTransaction("g", "gtx-1-8")
				return errors.Join(err, want(n == 1, "rolled back %d", n))
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := exchange(t, tc.data, tc.call)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "op", req.Op, tc.op)
			if tc.options == "" {
				if len(req.Options) != 0 {
					t.Errorf("no options expected, sent %s", req.Options)
				}
				return
			}
			sameJSON(t, "options", req.Options, tc.options)
		})
	}
}

// What cannot be said on the wire is refused here, by name, and nothing is
// sent: a NaN or infinite score, a zero-valued tagged item, an empty change.
func TestWhatCannotBeSentIsRefusedBeforeItIsSent(t *testing.T) {
	_, err := exchange(t, `{"Message":"pong"}`, func(c *Client) error {
		refused := func(what string, err error) error {
			if errors.Is(err, ErrArgument) {
				return nil
			}
			return fmt.Errorf("%s: want an argument error, got %v", what, err)
		}
		_, e1 := c.CacheZAdd("n", "z", ScoredMember{Score: math.NaN(), Member: []byte("a")})
		_, e2 := c.CacheZRangeByScore("n", "z", ScoreBound{Value: math.Inf(-1)}, ScoreHighest(), ScoreRangeOptions{})
		_, e3 := c.CacheZIncrBy("n", "z", []byte("a"), math.Inf(1))
		_, e4 := c.CacheZRem("n", "z")
		_, e5 := c.DocumentBulkWrite("c", BulkOp{})
		_, e6 := c.DocumentBulkWrite("c")
		_, e7 := c.VectorSetCollectionConfig("c", VectorConfigChange{})
		_, e8 := c.DocumentInsertMany("c")
		_, e9 := c.CacheSetIf("n", "k", nil, SetAlways, CacheExpiry{})
		_, _, e10 := c.CacheGetEx("n", "k", CacheExpiry{})
		_, e11 := c.CachePipeline(CacheCommand{})
		_, e12 := c.CacheTransaction()
		e13 := c.CacheScriptLoad("s", CacheCommand{})
		_, e14 := c.VectorSearchByExamples("c", ExampleSearch{Positive: []VectorExample{{}}}, 1, VectorSearchOptions{})
		if err := errors.Join(
			refused("a NaN score", e1), refused("an infinite bound", e2), refused("an infinite increment", e3),
			refused("no members", e4), refused("a zero BulkOp", e5), refused("no bulk ops", e6),
			refused("an empty config change", e7), refused("no documents", e8),
			refused("a zero expiry", e9), refused("a zero expiry on a read", e10), refused("a zero command", e11),
			refused("no commands", e12), refused("a zero command in a script", e13), refused("a zero example", e14),
		); err != nil {
			return err
		}
		// The one request the peer waits for.
		return c.CachePing()
	})
	if err != nil {
		t.Fatal(err)
	}
	// A zero CacheExpiry or CacheCommand has no wire form at all.
	if _, err := json.Marshal(CacheExpiry{}); err == nil {
		t.Error("a zero CacheExpiry marshalled")
	}
	if _, err := json.Marshal(CacheCommand{}); err == nil {
		t.Error("a zero CacheCommand marshalled")
	}
	if _, err := json.Marshal(VectorExample{}); err == nil {
		t.Error("a zero VectorExample marshalled")
	}
}
