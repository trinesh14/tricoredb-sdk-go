package e2emodels

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/trinesh14/tricoredb-sdk-go"
)

// The operations added for server 0.1.0-rc.3, against a real server.
//
// Like the rest of this package these assert the value that came back, and
// where a wrong answer could pass they assert the wrong answer is absent.

func bs(s string) []byte { return []byte(s) }

func texts(values [][]byte) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func mustBe(t *testing.T, what string, ok bool, got any) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: got %+v", what, got)
	}
}

func refusedByServer(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, tricoredb.ErrServer) {
		t.Fatalf("%s: want a server refusal, got %v", what, err)
	}
}

func cleanNamespace(t *testing.T, db *tricoredb.Client, ns string) {
	t.Helper()
	t.Cleanup(func() { _, _ = db.CacheClearNamespace(ns) })
}

func TestCacheSortedSets(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("zset")
	cleanNamespace(t, db, ns)

	r, err := db.CacheZAdd(ns, "board",
		tricoredb.ScoredMember{Score: 10, Member: bs("ada")},
		tricoredb.ScoredMember{Score: 30, Member: bs("cy")},
		tricoredb.ScoredMember{Score: 20, Member: bs("bob")})
	mustNoErr(t, "zadd", err)
	mustBe(t, "zadd", r.Added == 3 && r.Updated == 0, r)
	r, err = db.CacheZAdd(ns, "board", tricoredb.ScoredMember{Score: 40, Member: bs("ada")})
	mustNoErr(t, "re-score", err)
	mustBe(t, "re-score", r.Added == 0 && r.Updated == 1, r)

	n, err := db.CacheZCard(ns, "board")
	mustNoErr(t, "zcard", err)
	mustBe(t, "zcard", n == 3, n)
	score, found, err := db.CacheZScore(ns, "board", bs("ada"))
	mustNoErr(t, "zscore", err)
	mustBe(t, "zscore", found && score == 40, score)
	_, found, err = db.CacheZScore(ns, "board", bs("nobody"))
	mustNoErr(t, "zscore absent", err)
	mustBe(t, "zscore absent", !found, found)
	rank, found, err := db.CacheZRank(ns, "board", bs("ada"), false)
	mustNoErr(t, "zrank", err)
	mustBe(t, "zrank", found && rank == 2, rank)
	rank, found, err = db.CacheZRank(ns, "board", bs("ada"), true)
	mustNoErr(t, "zrevrank", err)
	mustBe(t, "zrevrank", found && rank == 0, rank)

	all, err := db.CacheZRange(ns, "board", 0, -1, false)
	mustNoErr(t, "zrange", err)
	wantExactlyOrdered(t, "zrange", texts(all), "bob", "cy", "ada")
	top, err := db.CacheZRangeWithScores(ns, "board", 0, 0, true)
	mustNoErr(t, "zrange rev", err)
	mustBe(t, "zrange rev", len(top) == 1 && string(top[0].Member) == "ada" && top[0].Score == 40, top)

	above, err := db.CacheZRangeByScore(ns, "board",
		tricoredb.ScoreBound{Value: 20, Exclusive: true}, tricoredb.ScoreHighest(), tricoredb.ScoreRangeOptions{})
	mustNoErr(t, "by score", err)
	wantExactlyOrdered(t, "by score", texts(above), "cy", "ada")
	page, err := db.CacheZRangeByScoreWithScores(ns, "board", tricoredb.ScoreLowest(), tricoredb.ScoreHighest(),
		tricoredb.ScoreRangeOptions{Offset: 1, Count: 1})
	mustNoErr(t, "paged", err)
	mustBe(t, "paged", len(page) == 1 && string(page[0].Member) == "cy" && page[0].Score == 30, page)

	score, err = db.CacheZIncrBy(ns, "board", bs("bob"), 2.5)
	mustNoErr(t, "zincrby", err)
	mustBe(t, "zincrby", score == 22.5, score)
	removed, err := db.CacheZRem(ns, "board", bs("bob"), bs("zed"))
	mustNoErr(t, "zrem", err)
	mustBe(t, "zrem", removed == 1, removed)

	_, err = db.CacheZAdd(ns, "board", tricoredb.ScoredMember{Score: math.Inf(1), Member: bs("x")})
	if !errors.Is(err, tricoredb.ErrArgument) {
		t.Fatalf("an infinite score: want an argument error, got %v", err)
	}
}

func wantExactlyOrdered(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: got %v, want %v in that order", what, got, want)
	}
}

func TestCacheLocksFenceWithARisingToken(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("lock")
	cleanNamespace(t, db, ns)

	first, err := db.CacheLockAcquire(ns, "job", "worker-1", 30_000)
	mustNoErr(t, "acquire", err)
	mustBe(t, "acquire", first.Acquired && first.Token > 0 && first.Holder == "worker-1", first)
	again, err := db.CacheLockAcquire(ns, "job", "worker-1", 30_000)
	mustNoErr(t, "re-acquire", err)
	mustBe(t, "a retried acquire keeps its token", again.Acquired && again.Token == first.Token, again)
	other, err := db.CacheLockAcquire(ns, "job", "worker-2", 30_000)
	mustNoErr(t, "contend", err)
	mustBe(t, "contend", !other.Acquired && other.Token == 0 && other.Holder == "worker-1", other)

	renewal, err := db.CacheLockRenew(ns, "job", "worker-1", 30_000)
	mustNoErr(t, "renew", err)
	mustBe(t, "renew", renewal.Renewed && renewal.Token == first.Token, renewal)
	renewal, err = db.CacheLockRenew(ns, "job", "worker-2", 30_000)
	mustNoErr(t, "renew by another", err)
	mustBe(t, "renew by another", !renewal.Renewed, renewal)

	released, err := db.CacheLockRelease(ns, "job", "worker-2")
	mustNoErr(t, "release by another", err)
	mustBe(t, "release by another", !released, released)
	released, err = db.CacheLockRelease(ns, "job", "worker-1")
	mustNoErr(t, "release", err)
	mustBe(t, "release", released, released)

	next, err := db.CacheLockAcquire(ns, "job", "worker-2", 30_000)
	mustNoErr(t, "acquire after release", err)
	mustBe(t, "the token never repeats", next.Acquired && next.Token > first.Token, next)
	_, _ = db.CacheLockRelease(ns, "job", "worker-2")
}

func TestCacheBitmapsAndHyperLogLog(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("bits")
	cleanNamespace(t, db, ns)

	prev, err := db.CacheSetBit(ns, "a", 7, true)
	mustNoErr(t, "setbit", err)
	mustBe(t, "setbit", !prev, prev)
	prev, err = db.CacheSetBit(ns, "a", 7, true)
	mustNoErr(t, "setbit again", err)
	mustBe(t, "setbit again", prev, prev)
	bit, err := db.CacheGetBit(ns, "a", 7)
	mustNoErr(t, "getbit", err)
	mustBe(t, "getbit", bit, bit)
	bit, err = db.CacheGetBit(ns, "a", 6)
	mustNoErr(t, "getbit unset", err)
	mustBe(t, "getbit unset", !bit, bit)
	stored, _, err := db.CacheGet(ns, "a")
	mustNoErr(t, "get", err)
	mustBe(t, "bit 7 is the least significant bit of byte 0", bytes.Equal(stored, []byte{0b0000_0001}), stored)

	_, err = db.CacheSetBit(ns, "b", 0, true)
	mustNoErr(t, "setbit b", err)
	count, err := db.CacheBitCount(ns, "a", nil)
	mustNoErr(t, "bitcount", err)
	mustBe(t, "bitcount", count == 1, count)
	count, err = db.CacheBitCount(ns, "a", &tricoredb.ByteRange{Start: 0, End: -1})
	mustNoErr(t, "bitcount range", err)
	mustBe(t, "bitcount range", count == 1, count)
	pos, err := db.CacheBitPos(ns, "a", true, nil)
	mustNoErr(t, "bitpos", err)
	mustBe(t, "bitpos", pos == 7, pos)
	pos, err = db.CacheBitPos(ns, "nokey", true, nil)
	mustNoErr(t, "bitpos missing", err)
	mustBe(t, "bitpos missing", pos == -1, pos)
	length, err := db.CacheBitOp(ns, tricoredb.BitOr, "c", "a", "b")
	mustNoErr(t, "bitop", err)
	mustBe(t, "bitop", length == 1, length)
	stored, _, err = db.CacheGet(ns, "c")
	mustNoErr(t, "get c", err)
	mustBe(t, "or", bytes.Equal(stored, []byte{0b1000_0001}), stored)

	changed, err := db.CachePfAdd(ns, "h1", bs("a"), bs("b"), bs("c"))
	mustNoErr(t, "pfadd", err)
	mustBe(t, "pfadd", changed, changed)
	_, err = db.CachePfAdd(ns, "h2", bs("c"), bs("d"))
	mustNoErr(t, "pfadd h2", err)
	est, err := db.CachePfCount(ns, "h1")
	mustNoErr(t, "pfcount", err)
	mustBe(t, "pfcount", est == 3, est)
	est, err = db.CachePfCount(ns, "h1", "h2")
	mustNoErr(t, "pfcount union", err)
	mustBe(t, "pfcount union", est == 4, est)
	mustNoErr(t, "pfmerge", db.CachePfMerge(ns, "h3", "h1", "h2"))
	est, err = db.CachePfCount(ns, "h3")
	mustNoErr(t, "pfcount merged", err)
	mustBe(t, "pfcount merged", est == 4, est)
}

func TestCacheManyKeyConditionalAndAtomicOperations(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("multi")
	cleanNamespace(t, db, ns)

	written, err := db.CacheMSet(ns, []tricoredb.CacheEntry{{Key: "a", Value: bs("1")}, {Key: "b", Value: []byte{0xff, 0}}}, 0)
	mustNoErr(t, "mset", err)
	mustBe(t, "mset", written == 2, written)
	values, err := db.CacheMGet(ns, "a", "nope", "b", "a")
	mustNoErr(t, "mget", err)
	mustBe(t, "mget", len(values) == 4 && string(values[0]) == "1" && values[1] == nil &&
		bytes.Equal(values[2], []byte{0xff, 0}) && string(values[3]) == "1", values)
	_, err = db.CacheMSet(ns, []tricoredb.CacheEntry{{Key: "x", Value: bs("1")}, {Key: "x", Value: bs("2")}}, 0)
	refusedByServer(t, "a repeated key", err)

	warmed, err := db.CacheWarm(ns, []tricoredb.CacheEntry{{Key: "a", Value: bs("9")}, {Key: "w", Value: bs("8")}}, 0)
	mustNoErr(t, "warm", err)
	mustBe(t, "warm", warmed.Written == 1 && warmed.AlreadyCached == 1, warmed)
	a, _, err := db.CacheGet(ns, "a")
	mustNoErr(t, "get", err)
	mustBe(t, "a live value is left alone", string(a) == "1", a)

	set, err := db.CacheSetIf(ns, "a", bs("2"), tricoredb.SetIfAbsent, tricoredb.ExpiryClear())
	mustNoErr(t, "nx", err)
	mustBe(t, "nx on a present key", !set, set)
	set, err = db.CacheSetIf(ns, "new", bs("2"), tricoredb.SetIfPresent, tricoredb.ExpiryClear())
	mustNoErr(t, "xx", err)
	mustBe(t, "xx on an absent key", !set, set)
	set, err = db.CacheSetIf(ns, "a", bs("2"), tricoredb.SetIfPresent, tricoredb.ExpirySet(60_000))
	mustNoErr(t, "xx with a ttl", err)
	mustBe(t, "xx with a ttl", set, set)
	set, err = db.CacheSetIf(ns, "a", bs("3"), tricoredb.SetAlways, tricoredb.ExpiryKeep())
	mustNoErr(t, "keepttl", err)
	_, has, err := db.CacheTTL(ns, "a")
	mustNoErr(t, "ttl", err)
	mustBe(t, "the deadline was kept", set && has, has)
	_, err = db.CacheSetIf(ns, "a", bs("3"), tricoredb.SetAlways, tricoredb.CacheExpiry{})
	if !errors.Is(err, tricoredb.ErrArgument) || db.Poisoned() {
		t.Fatalf("a zero CacheExpiry: want an argument error and a usable connection, got %v", err)
	}

	swap, err := db.CacheCompareAndSwap(ns, "cas", nil, bs("v1"), 0)
	mustNoErr(t, "cas create", err)
	mustBe(t, "cas create", swap.Swapped, swap)
	swap, err = db.CacheCompareAndSwap(ns, "cas", bs("wrong"), bs("v2"), 0)
	mustNoErr(t, "cas mismatch", err)
	mustBe(t, "cas mismatch", !swap.Swapped && string(swap.Current) == "v1", swap)
	swap, err = db.CacheCompareAndSwap(ns, "b", bs("wrong"), nil, 0)
	mustNoErr(t, "cas binary mismatch", err)
	mustBe(t, "cas binary mismatch", !swap.Swapped && swap.CurrentIsBinary, swap)
	swap, err = db.CacheCompareAndSwap(ns, "cas", bs("v1"), nil, 0)
	mustNoErr(t, "cas delete", err)
	exists, err := db.CacheExists(ns, "cas")
	mustNoErr(t, "exists", err)
	mustBe(t, "cas delete", swap.Swapped && !exists, exists)

	_, found, err := db.CacheGetSet(ns, "gs", bs("1"), 0)
	mustNoErr(t, "getset", err)
	mustBe(t, "getset on an absent key", !found, found)
	old, found, err := db.CacheGetSet(ns, "gs", bs("2"), 0)
	mustNoErr(t, "getset again", err)
	mustBe(t, "getset again", found && string(old) == "1", old)
	cur, found, err := db.CacheGetEx(ns, "gs", tricoredb.ExpirySet(60_000))
	mustNoErr(t, "getex", err)
	_, has, _ = db.CacheTTL(ns, "gs")
	mustBe(t, "getex", found && string(cur) == "2" && has, cur)
	cur, found, err = db.CacheGetDel(ns, "gs")
	mustNoErr(t, "getdel", err)
	mustBe(t, "getdel", found && string(cur) == "2", cur)
	_, found, err = db.CacheGetDel(ns, "gs")
	mustNoErr(t, "getdel again", err)
	mustBe(t, "getdel again", !found, found)

	length, err := db.CacheAppend(ns, "log", bs("ab"))
	mustNoErr(t, "append", err)
	length, err = db.CacheAppend(ns, "log", bs("cd"))
	mustNoErr(t, "append again", err)
	log, _, _ := db.CacheGet(ns, "log")
	mustBe(t, "append", length == 4 && string(log) == "abcd", log)

	at, err := db.CacheExpireAt(ns, "log", 4_102_444_800_000)
	mustNoErr(t, "expireat", err)
	mustBe(t, "expireat", at.Updated && !at.Deleted, at)
	at, err = db.CacheExpireAt(ns, "log", 1)
	mustNoErr(t, "expireat in the past", err)
	exists, _ = db.CacheExists(ns, "log")
	mustBe(t, "a deadline already passed deletes", at.Deleted && !exists, at)
}

func TestCacheFreshReadsHandOutOneLease(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("fresh")
	cleanNamespace(t, db, ns)

	until, err := db.CacheSetFresh(ns, "page", bs("body"), 60_000, 0)
	mustNoErr(t, "setfresh", err)
	read, err := db.CacheGetFresh(ns, "page", "w1", 5000)
	mustNoErr(t, "getfresh", err)
	mustBe(t, "fresh", until > 0 && read.State == tricoredb.FreshStateFresh && string(read.Value) == "body", read)

	_, err = db.CacheSetFresh(ns, "bin", []byte{0xff, 0xfe, 0}, 60_000, 0)
	mustNoErr(t, "setfresh binary", err)
	read, err = db.CacheGetFresh(ns, "bin", "w1", 5000)
	mustNoErr(t, "getfresh binary", err)
	mustBe(t, "binary survives", bytes.Equal(read.Value, []byte{0xff, 0xfe, 0}), read)

	_, err = db.CacheSetFresh(ns, "old", bs("v"), 1, 0)
	mustNoErr(t, "setfresh stale", err)
	time.Sleep(20 * time.Millisecond)
	first, err := db.CacheGetFresh(ns, "old", "w1", 5000)
	mustNoErr(t, "getfresh stale", err)
	second, err := db.CacheGetFresh(ns, "old", "w2", 5000)
	mustNoErr(t, "getfresh stale again", err)
	mustBe(t, "one caller refreshes", first.State == tricoredb.FreshStateStale && first.Refresh &&
		!second.Refresh && string(second.Value) == "v", []any{first, second})

	first, err = db.CacheGetFresh(ns, "absent", "w1", 5000)
	mustNoErr(t, "getfresh miss", err)
	second, err = db.CacheGetFresh(ns, "absent", "w2", 5000)
	mustNoErr(t, "getfresh miss again", err)
	mustBe(t, "one caller computes", first.State == tricoredb.FreshStateMiss && first.Lease && !second.Lease &&
		second.RetryAfterMs > 0, []any{first, second})

	mustNoErr(t, "setmissing", db.CacheSetMissing(ns, "gone", 60_000))
	read, err = db.CacheGetFresh(ns, "gone", "w1", 5000)
	mustNoErr(t, "getfresh missing", err)
	mustBe(t, "missing", read.State == tricoredb.FreshStateMissing, read)
}

func TestCacheListsSetsAndHashes(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("coll")
	cleanNamespace(t, db, ns)

	_, err := db.CacheRPush(ns, "src", bs("a"), bs("b"), bs("c"))
	mustNoErr(t, "rpush", err)
	moved, found, err := db.CacheLMove(ns, "src", "dst", tricoredb.ListLeft, tricoredb.ListRight)
	mustNoErr(t, "lmove", err)
	mustBe(t, "lmove", found && string(moved) == "a", moved)
	_, found, err = db.CacheLMove(ns, "empty", "dst", tricoredb.ListLeft, tricoredb.ListRight)
	mustNoErr(t, "lmove empty", err)
	mustBe(t, "lmove empty", !found, found)
	trim, err := db.CacheLTrim(ns, "src", 0, 0)
	mustNoErr(t, "ltrim", err)
	mustBe(t, "ltrim", trim.Length == 1 && trim.Removed == 1, trim)

	key, value, found, err := db.CacheBLPop(ns, 100, "empty", "dst")
	mustNoErr(t, "blpop", err)
	mustBe(t, "blpop", found && key == "dst" && string(value) == "a", key)
	key, value, found, err = db.CacheBRPop(ns, 100, "src")
	mustNoErr(t, "brpop", err)
	mustBe(t, "brpop", found && key == "src" && string(value) == "b", key)
	_, _, found, err = db.CacheBRPop(ns, 100, "empty")
	mustNoErr(t, "brpop timing out", err)
	mustBe(t, "brpop timing out", !found, found)

	_, err = db.CacheSAdd(ns, "s1", bs("a"), bs("b"))
	mustNoErr(t, "sadd", err)
	_, err = db.CacheSAdd(ns, "s2", bs("b"), bs("c"))
	mustNoErr(t, "sadd s2", err)
	union, err := db.CacheSUnion(ns, "s1", "s2", "none")
	mustNoErr(t, "sunion", err)
	wantExactly(t, "sunion", texts(union), "a", "b", "c")
	inter, err := db.CacheSInter(ns, "s1", "s2")
	mustNoErr(t, "sinter", err)
	wantExactly(t, "sinter", texts(inter), "b")
	diff, err := db.CacheSDiff(ns, "s1", "s2")
	mustNoErr(t, "sdiff", err)
	wantExactly(t, "sdiff", texts(diff), "a")
	one, err := db.CacheSRandMember(ns, "s1")
	mustNoErr(t, "srandmember", err)
	mustBe(t, "srandmember", len(one) == 1, one)
	distinct, err := db.CacheSRandMembers(ns, "s1", 5)
	mustNoErr(t, "srandmembers", err)
	mustBe(t, "up to count distinct members", len(distinct) == 2, distinct)
	repeats, err := db.CacheSRandMembers(ns, "s1", -5)
	mustNoErr(t, "srandmembers with repeats", err)
	mustBe(t, "exactly -count with repeats", len(repeats) == 5, repeats)

	n, err := db.CacheHIncrBy(ns, "h", bs("n"), 5)
	mustNoErr(t, "hincrby", err)
	n, err = db.CacheHIncrBy(ns, "h", bs("n"), -2)
	mustNoErr(t, "hincrby again", err)
	mustBe(t, "hincrby", n == 3, n)
}

func TestCachePipelinesTransactionsAndScripts(t *testing.T) {
	db := newClient(t)
	ns := uniqueName("pipe")
	cleanNamespace(t, db, ns)

	replies, err := db.CachePipeline(
		tricoredb.CmdSet(ns, "k", bs("text"), 0),
		tricoredb.CmdGet(ns, "k"),
		tricoredb.CmdIncr(ns, "k", 1),
		tricoredb.CmdRaw("Ping", nil),
		tricoredb.CmdExists(ns, "k"),
	)
	mustNoErr(t, "pipeline", err)
	mustBe(t, "pipeline", len(replies) == 5, replies)
	value, found := replies[1].Value()
	mustBe(t, "the pipelined read", found && string(value) == "text", value)
	mustBe(t, "a failing step is reported and the rest still run",
		!replies[2].OK() && replies[2].Err != "" && replies[3].OK() && replies[4].OK(), replies)

	replies, err = db.CacheTransaction(
		tricoredb.CmdIncr(ns, "n", 2),
		tricoredb.CmdIncr(ns, "n", 3),
		tricoredb.CmdGet(ns, "n"),
		tricoredb.CmdDelete(ns, "k"),
	)
	mustNoErr(t, "transaction", err)
	value, found = replies[2].Value()
	exists, _ := db.CacheExists(ns, "k")
	mustBe(t, "transaction", found && string(value) == "5" && !exists, value)

	script := uniqueName("script")
	mustNoErr(t, "script load", db.CacheScriptLoad(script,
		tricoredb.CmdSet(ns, "$1", bs("1"), 0), tricoredb.CmdIncr(ns, "$2", 1)))
	replies, err = db.CacheScriptRun(script, "first", "second")
	mustNoErr(t, "script run", err)
	first, _, _ := db.CacheGet(ns, "first")
	second, _ := db.CacheIncr(ns, "second", 0)
	mustBe(t, "the script's arguments were substituted",
		len(replies) == 2 && replies[0].OK() && replies[1].OK() && string(first) == "1" && second == 1, replies)
	_, err = db.CacheScriptRun("go_no_such_script")
	refusedByServer(t, "an unknown script", err)

	_, err = db.CachePipeline(tricoredb.CacheCommand{})
	if !errors.Is(err, tricoredb.ErrArgument) || db.Poisoned() {
		t.Fatalf("a zero CacheCommand: want an argument error and a usable connection, got %v", err)
	}
}

func TestCachePublishPollLimitsAndNodeReads(t *testing.T) {
	db := newClient(t)
	channel := uniqueName("chan")
	first, err := db.CachePublish(channel, "one")
	mustNoErr(t, "publish", err)
	second, err := db.CachePublish(channel, "two")
	mustNoErr(t, "publish again", err)
	mustBe(t, "ids rise", second > first, second)

	all, err := db.CachePoll(0, 0, channel)
	mustNoErr(t, "poll", err)
	mustBe(t, "poll", len(all.Messages) == 2 && all.Messages[0].Message == "one" && all.Messages[1].Message == "two" &&
		all.Next == second, all)
	after, err := db.CachePoll(first, 1, channel)
	mustNoErr(t, "poll after", err)
	mustBe(t, "poll after", len(after.Messages) == 1 && after.Messages[0].Message == "two" &&
		after.Messages[0].Channel == channel && after.Messages[0].ID == second, after)
	pattern, err := db.CachePoll(0, 0, channel[:len(channel)-1]+"*")
	mustNoErr(t, "poll a pattern", err)
	mustBe(t, "poll a pattern", len(pattern.Messages) >= 2, pattern)
	drained, err := db.CachePoll(second, 0, channel)
	mustNoErr(t, "poll drained", err)
	mustBe(t, "poll drained", len(drained.Messages) == 0, drained)

	ns := uniqueName("limit")
	cleanNamespace(t, db, ns)
	limit, err := db.CacheSetNamespaceLimit(ns, 1, 0)
	mustNoErr(t, "set limit", err)
	mustBe(t, "set limit", limit.MaxKeys != nil && *limit.MaxKeys == 1 && limit.MaxBytes == nil && limit.Keys == 0, limit)
	mustNoErr(t, "the first key fits", db.CacheSet(ns, "a", bs("1")))
	refusedByServer(t, "the second key passes the cap", db.CacheSet(ns, "b", bs("1")))
	held, err := db.CacheGetNamespaceLimit(ns)
	mustNoErr(t, "get limit", err)
	mustBe(t, "get limit", held.Keys == 1 && held.Bytes == 1 && *held.MaxKeys == 1, held)
	lifted, err := db.CacheSetNamespaceLimit(ns, 0, 0)
	mustNoErr(t, "lift", err)
	mustBe(t, "lift", lifted.MaxKeys == nil, lifted)
	mustNoErr(t, "unlimited again", db.CacheSet(ns, "b", bs("1")))

	stats, err := db.CacheStats()
	mustNoErr(t, "stats", err)
	mustBe(t, "stats", stats.Eviction.Policy != "", stats)
	_, err = db.CachePurgeExpired()
	mustNoErr(t, "purge expired", err)

	// Hot-key tracking is a server setting. Where it is off the server says
	// so by name; where it is on the payload is whatever it sends.
	if _, err := db.CacheHotKeys(3); err != nil {
		refusedByServer(t, "hot keys", err)
		if !strings.Contains(err.Error(), "hot_keys") {
			t.Fatalf("hot keys: the refusal does not name the setting: %v", err)
		}
		t.Logf("hot keys refused: %v", err)
	}
}

func TestCacheFlushEmptiesOneDatabaseAndNoOther(t *testing.T) {
	main := newClient(t)
	// A database of this test's own, so nothing another client holds is
	// within reach of the flush.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	other, err := tricoredb.Connect(ctx, tricoredb.Options{
		Host: shared.host, Port: shared.port, User: testUser, Secret: testSecret, Database: uniqueName("flushdb"),
	})
	mustNoErr(t, "connect to a scratch database", err)
	defer other.Close()

	ns := uniqueName("flush")
	cleanNamespace(t, main, ns)
	mustNoErr(t, "set in main", main.CacheSet(ns, "kept", bs("1")))
	mustNoErr(t, "set", other.CacheSet(ns, "a", bs("1")))
	mustNoErr(t, "set in a second namespace", other.CacheSet(ns+"_2", "b", bs("2")))

	cleared, err := other.CacheFlushDatabase()
	mustNoErr(t, "flush", err)
	mustBe(t, "flush", cleared == 2, cleared)
	_, found, err := other.CacheGet(ns, "a")
	mustNoErr(t, "get after flush", err)
	mustBe(t, "the flushed key is gone", !found, found)
	kept, found, err := main.CacheGet(ns, "kept")
	mustNoErr(t, "get in main", err)
	mustBe(t, "the other database is untouched", found && string(kept) == "1", kept)
}

func TestDocumentReplaceBatchesAndBulkWrite(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	ids, err := db.DocumentInsertMany(coll,
		map[string]any{"_id": "a", "n": 1, "kind": "x"},
		map[string]any{"_id": "b", "n": 2, "kind": "x"},
		map[string]any{"n": 3, "kind": "y"})
	mustNoErr(t, "insert many", err)
	mustBe(t, "insert many", len(ids) == 3 && ids[0] == "a" && ids[1] == "b" && ids[2] != "", ids)
	_, err = db.DocumentInsertMany(coll, map[string]any{"_id": "z"}, map[string]any{"_id": "a"})
	refusedByServer(t, "a duplicate id", err)
	_, found, err := db.DocumentGet(coll, "z")
	mustNoErr(t, "get", err)
	mustBe(t, "none of a failed batch landed", !found, found)

	replaced, err := db.DocumentReplace(coll, "a", map[string]any{"n": 10}, tricoredb.ReplaceOptions{Return: tricoredb.ReturnBefore})
	mustNoErr(t, "replace", err)
	mustBe(t, "replace", replaced.Replaced && !replaced.Inserted && str(t, replaced.Document, "kind") == "x", replaced)
	stored, _, err := db.DocumentGet(coll, "a")
	mustNoErr(t, "get replaced", err)
	if _, merged := stored["kind"]; merged || num(t, stored, "n") != 10 {
		t.Fatalf("a replace does not merge: %v", stored)
	}
	_, err = db.DocumentReplace(coll, "nope", map[string]any{"n": 1}, tricoredb.ReplaceOptions{})
	refusedByServer(t, "replacing an absent document", err)
	created, err := db.DocumentReplace(coll, "new", map[string]any{"n": 7},
		tricoredb.ReplaceOptions{Upsert: true, Return: tricoredb.ReturnAfter})
	mustNoErr(t, "upsert", err)
	mustBe(t, "upsert", created.Inserted && num(t, created.Document, "n") == 7, created)

	result, err := db.DocumentBulkWrite(coll,
		tricoredb.BulkInsertWithID("w1", map[string]any{"n": 1}),
		tricoredb.BulkInsert(map[string]any{"n": 2}),
		tricoredb.BulkUpdateOne("w1", tricoredb.DocumentUpdate{Inc: map[string]any{"n": 4}}),
		tricoredb.BulkUpdateOne("w2", tricoredb.DocumentUpdate{Set: map[string]any{"n": 6}}).Upsert(),
		tricoredb.BulkReplace("b", map[string]any{"n": 20}),
		tricoredb.BulkDelete("new"),
	)
	mustNoErr(t, "bulk write", err)
	mustBe(t, "bulk write", result.Inserted == 2 && result.Updated == 1 && result.Upserted == 1 &&
		result.Replaced == 1 && result.Deleted == 1 && result.InsertedIDs[0] == "w1", result)
	w1, _, err := db.DocumentGet(coll, "w1")
	mustNoErr(t, "get w1", err)
	mustBe(t, "a later operation sees an earlier one", num(t, w1, "n") == 5, w1)
	_, found, _ = db.DocumentGet(coll, "new")
	mustBe(t, "the bulk delete landed", !found, found)

	_, err = db.DocumentBulkWrite(coll,
		tricoredb.BulkInsertWithID("w9", map[string]any{"n": 1}), tricoredb.BulkReplace("absent", map[string]any{}))
	refusedByServer(t, "a batch with a failing operation", err)
	_, found, _ = db.DocumentGet(coll, "w9")
	mustBe(t, "the batch is atomic", !found, found)

	deleted, err := db.DocumentDeleteMany(coll, tricoredb.FilterEq("kind", "y"))
	mustNoErr(t, "delete many", err)
	mustBe(t, "delete many", deleted == 1, deleted)
}

func TestDocumentTransactionsIsolateUntilCommit(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	txn, err := db.DocumentBeginTransaction(false)
	mustNoErr(t, "begin", err)
	db.SetRequestTxn(txn)
	_, err = db.DocumentInsertWithID(coll, "t1", map[string]any{"n": 1})
	mustNoErr(t, "insert inside", err)
	_, found, err := db.DocumentGet(coll, "t1")
	mustNoErr(t, "get inside", err)
	mustBe(t, "a transaction sees its own write", found, found)
	db.SetRequestTxn("")
	_, found, err = db.DocumentGet(coll, "t1")
	mustNoErr(t, "get outside", err)
	mustBe(t, "nobody else does", !found, found)
	db.SetRequestTxn(txn)
	commit, err := db.DocumentCommitTransaction(txn)
	mustNoErr(t, "commit", err)
	mustBe(t, "commit", commit.Committed && commit.Writes >= 1 && db.RequestTxn() == "", commit)
	_, found, _ = db.DocumentGet(coll, "t1")
	mustBe(t, "the write landed", found, found)

	txn, err = db.DocumentBeginTransaction(true)
	mustNoErr(t, "begin serializable", err)
	db.SetRequestTxn(txn)
	_, err = db.DocumentInsertWithID(coll, "t2", map[string]any{"n": 2})
	mustNoErr(t, "insert inside", err)
	mustNoErr(t, "rollback", db.DocumentRollbackTransaction(txn))
	_, found, _ = db.DocumentGet(coll, "t2")
	mustBe(t, "a rolled-back write is gone", !found && db.RequestTxn() == "", found)
	_, err = db.DocumentCommitTransaction(txn)
	refusedByServer(t, "committing a finished transaction", err)
}

func TestDocumentCollectionOptionsExplainAndMaintenance(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)
	_, err := db.DocumentInsertMany(coll, map[string]any{"_id": "a", "n": 1, "kind": "x"})
	mustNoErr(t, "insert", err)

	none, err := db.DocumentGetCollectionOptions(coll)
	mustNoErr(t, "options", err)
	mustBe(t, "a new collection has no options", none.Schema == nil && !none.Versioned && len(none.Defaults) == 0, none)

	zero, allow := 0.0, true
	options := tricoredb.CollectionOptions{
		Schema: &tricoredb.DocumentSchema{
			Required:         []string{"n"},
			Fields:           map[string]tricoredb.FieldRule{"n": {Type: tricoredb.FieldNumber, Minimum: &zero}},
			AdditionalFields: &allow,
		},
		Defaults:  map[string]any{"kind": "z"},
		Versioned: true,
	}
	checked, err := db.DocumentSetCollectionOptions(coll, options)
	mustNoErr(t, "set options", err)
	mustBe(t, "set options", checked == 1, checked)
	got, err := db.DocumentGetCollectionOptions(coll)
	mustNoErr(t, "get options", err)
	mustBe(t, "what was set reads back", got.Versioned && got.Schema != nil && got.Schema.Required[0] == "n" &&
		got.Schema.Fields["n"].Type == tricoredb.FieldNumber && *got.Schema.Fields["n"].Minimum == 0 &&
		*got.Schema.AdditionalFields && got.Defaults["kind"] == "z", got)

	_, err = db.DocumentInsert(coll, map[string]any{"kind": "q"})
	refusedByServer(t, "a document missing a required field", err)
	_, err = db.DocumentInsert(coll, map[string]any{"n": -1})
	refusedByServer(t, "a document under the minimum", err)
	_, err = db.DocumentInsertWithID(coll, "d", map[string]any{"n": 5})
	mustNoErr(t, "insert", err)
	stored, _, err := db.DocumentGet(coll, "d")
	mustNoErr(t, "get", err)
	mustBe(t, "the default and the version were applied", str(t, stored, "kind") == "z" && num(t, stored, "_version") == 1, stored)

	_, err = db.DocumentReplace(coll, "d", map[string]any{"n": 6}, tricoredb.ReplaceOptions{ExpectedVersion: tricoredb.ExpectVersion(99)})
	refusedByServer(t, "a stale expected version", err)
	replaced, err := db.DocumentReplace(coll, "d", map[string]any{"n": 6},
		tricoredb.ReplaceOptions{ExpectedVersion: tricoredb.ExpectVersion(1), Return: tricoredb.ReturnAfter})
	mustNoErr(t, "replace at the current version", err)
	mustBe(t, "the version rose", num(t, replaced.Document, "_version") == 2, replaced)
	_, err = db.DocumentBulkWrite(coll, tricoredb.BulkDelete("d").ExpectedVersion(1))
	refusedByServer(t, "a bulk delete at a stale version", err)
	_, err = db.DocumentBulkWrite(coll, tricoredb.BulkDelete("d").ExpectedVersion(2))
	mustNoErr(t, "a bulk delete at the current version", err)

	_, err = db.DocumentSetCollectionOptions(coll, tricoredb.CollectionOptions{
		Schema: &tricoredb.DocumentSchema{Required: []string{"absent"}},
	})
	refusedByServer(t, "a schema a stored document breaks", err)

	scan, err := db.DocumentExplain(coll, tricoredb.FilterEq("kind", "x"), tricoredb.ExplainOptions{})
	mustNoErr(t, "explain", err)
	mustBe(t, "explain a scan", !scan.IndexUsed && scan.ScanReason != "" && scan.Plan != "", scan)
	mustNoErr(t, "create index", db.DocumentCreateIndex(coll, "by_kind", "kind", false))
	indexed, err := db.DocumentExplain(coll, tricoredb.FilterEq("kind", "x"), tricoredb.ExplainOptions{Limit: 5})
	mustNoErr(t, "explain indexed", err)
	mustBe(t, "explain an index lookup", indexed.IndexUsed && indexed.Index == "by_kind" && indexed.RowsReturned == 1, indexed)

	rebuilt, err := db.DocumentRebuildIndex(coll, "by_kind")
	mustNoErr(t, "rebuild index", err)
	mustBe(t, "rebuild index", rebuilt.IndexName == "by_kind" && rebuilt.Documents == 1, rebuilt)
	purge, err := db.DocumentPurgeExpired(coll)
	mustNoErr(t, "purge expired", err)
	mustBe(t, "purge expired", purge.Purged == 0, purge)

	renamed := uniqueName("renamed")
	mustNoErr(t, "rename", db.DocumentRenameCollection(coll, renamed))
	t.Cleanup(func() { _ = db.DocumentDropCollection(renamed) })
	_, found, err := db.DocumentGet(renamed, "a")
	mustNoErr(t, "get under the new name", err)
	mustBe(t, "the documents moved", found, found)
	refusedByServer(t, "renaming a collection that is gone", db.DocumentRenameCollection(coll, renamed))
}

func TestVectorBatchFilterCountAndSearchByExample(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 3, tricoredb.MetricCosine)

	written, err := db.VectorUpsertMany(coll, []tricoredb.VectorUpsertItem{
		{ID: "a", Vector: []float32{1, 0, 0}, Metadata: map[string]any{"kind": "x", "_namespace": "n1"}},
		{ID: "b", Vector: []float32{0, 1, 0}, Metadata: map[string]any{"kind": "y", "_namespace": "n1"}},
		{ID: "c", Vector: []float32{0, 0, 1}},
	})
	mustNoErr(t, "upsert many", err)
	mustBe(t, "upsert many", written == 3, written)
	_, err = db.VectorUpsertMany(coll, []tricoredb.VectorUpsertItem{
		{ID: "r", Vector: []float32{1, 0, 0}}, {ID: "r", Vector: []float32{0, 1, 0}},
	})
	refusedByServer(t, "a repeated id", err)

	count, err := db.VectorCount(coll, nil)
	mustNoErr(t, "count", err)
	mustBe(t, "count", count == 3, count)
	count, err = db.VectorCount(coll, map[string]any{"kind": "x"})
	mustNoErr(t, "count filtered", err)
	mustBe(t, "count filtered", count == 1, count)

	lists, err := db.VectorSearchBatch(coll, [][]float32{{1, 0, 0}, {0, 1, 0}}, 1, tricoredb.VectorSearchOptions{})
	mustNoErr(t, "search batch", err)
	mustBe(t, "one list per query, in order", len(lists) == 2 && lists[0][0].ID == "a" && lists[1][0].ID == "b", lists)
	filtered, err := db.VectorSearchBatch(coll, [][]float32{{1, 0, 0}}, 3, tricoredb.VectorSearchOptions{
		Filter: map[string]any{"kind": "y"}, IncludeVectors: true, Explain: true,
	})
	mustNoErr(t, "filtered batch", err)
	mustBe(t, "filtered batch", len(filtered[0]) == 1 && filtered[0][0].ID == "b" &&
		len(filtered[0][0].Vector) == 3 && filtered[0][0].Vector[1] == 1 && filtered[0][0].Reason != "", filtered)

	fused, err := db.VectorSearchMulti(coll, [][]float32{{1, 0, 0}, {0, 1, 0}}, 3, tricoredb.FusionMax, nil)
	mustNoErr(t, "search multi", err)
	mustBe(t, "search multi", fused.Fusion == tricoredb.FusionMax && fused.Queries == 2 && len(fused.Results) == 3, fused)

	byExample, err := db.VectorSearchByExamples(coll,
		tricoredb.ExampleSearch{Positive: []tricoredb.VectorExample{tricoredb.ExampleID("a")}}, 3, tricoredb.VectorSearchOptions{})
	mustNoErr(t, "by examples", err)
	if contains(hitIDs(byExample.Results), "a") {
		t.Fatalf("the example is left out by default: %v", hitIDs(byExample.Results))
	}
	withExample, err := db.VectorSearchByExamples(coll, tricoredb.ExampleSearch{
		Positive:        []tricoredb.VectorExample{tricoredb.ExampleVector([]float32{1, 0, 0})},
		Negative:        []tricoredb.VectorExample{tricoredb.ExampleID("c")},
		IncludeExamples: true,
	}, 1, tricoredb.VectorSearchOptions{})
	mustNoErr(t, "by examples with a vector", err)
	mustBe(t, "by examples with a vector", withExample.Results[0].ID == "a", withExample)

	updated, err := db.VectorUpdateByFilter(coll, map[string]any{"kind": "x"}, map[string]any{"seen": true})
	mustNoErr(t, "update by filter", err)
	item, _, _ := db.VectorGet(coll, "a")
	mustBe(t, "update by filter", updated == 1 && item.Metadata["seen"] == true, item)
	partitions, err := db.VectorListPartitions(coll, "")
	mustNoErr(t, "partitions", err)
	mustBe(t, "partitions", partitions.Field == "_namespace" && len(partitions.Partitions) == 1 &&
		partitions.Partitions[0].Name == "n1" && partitions.Partitions[0].Count == 2 && partitions.Unpartitioned == 1, partitions)

	copyName := uniqueName("vec_copy")
	copied, err := db.VectorCopyCollection(coll, copyName)
	mustNoErr(t, "copy", err)
	mustBe(t, "copy", copied == 3, copied)
	_, err = db.VectorCopyCollection(coll, copyName)
	refusedByServer(t, "copying onto an existing collection", err)
	renamed := uniqueName("vec_renamed")
	moved, err := db.VectorRenameCollection(copyName, renamed)
	mustNoErr(t, "rename", err)
	t.Cleanup(func() { _ = db.VectorDropCollection(renamed) })
	mustBe(t, "rename", moved == 3, moved)
	deleted, err := db.VectorDeleteMany(renamed, "a", "absent")
	mustNoErr(t, "delete many", err)
	mustBe(t, "delete many counts the ids that were present", deleted == 1, deleted)
	deleted, err = db.VectorDeleteByFilter(renamed, map[string]any{"kind": "y"})
	mustNoErr(t, "delete by filter", err)
	mustBe(t, "delete by filter", deleted == 1, deleted)
	_, err = db.VectorDeleteByFilter(renamed, map[string]any{})
	refusedByServer(t, "an empty filter", err)
	left, _ := db.VectorCount(renamed, nil)
	original, _ := db.VectorCount(coll, nil)
	mustBe(t, "the copy shrank and the original did not", left == 1 && original == 3, []int64{left, original})
}

func TestVectorNamedAliasesTextAndIndexMaintenance(t *testing.T) {
	db := newClient(t)
	embedding, err := db.VectorEmbed([]string{"hello world", "goodbye"}, 4)
	mustNoErr(t, "embed", err)
	mustBe(t, "embed", embedding.Dimension == 4 && len(embedding.Vectors) == 2 && len(embedding.Vectors[0]) == 4 &&
		len(embedding.Sparse) == 2 && embedding.Embedder != "", embedding)

	coll := newVectorCollection(t, db, 3, tricoredb.MetricCosine)
	on, off := true, false
	config, err := db.VectorSetCollectionConfig(coll, tricoredb.VectorConfigChange{Normalize: &on, M: 8, EfConstruction: 100})
	mustNoErr(t, "config", err)
	mustBe(t, "config", config.Normalize && config.M == 8 && config.EfConstruction == 100, config)

	written, err := db.VectorUpsertText(coll, []tricoredb.TextUpsert{
		{ID: "t1", Text: "hello world", Metadata: map[string]any{"kind": "t"}}, {ID: "t2", Text: "goodbye"},
	})
	mustNoErr(t, "upsert text", err)
	item, _, _ := db.VectorGet(coll, "t1")
	mustBe(t, "upsert text keeps the text", written == 2 && item.Metadata["text"] == "hello world" && len(item.Vector) == 3, item)
	_, err = db.VectorSetCollectionConfig(coll, tricoredb.VectorConfigChange{Normalize: &off})
	refusedByServer(t, "changing normalize on a collection that holds vectors", err)

	alias := uniqueName("alias")
	mustNoErr(t, "set alias", db.VectorSetAlias(alias, coll))
	aliases, err := db.VectorListAliases()
	mustNoErr(t, "list aliases", err)
	listed := false
	for _, a := range aliases {
		listed = listed || (a.Alias == alias && a.Collection == coll)
	}
	through, err := db.VectorCount(alias, nil)
	mustNoErr(t, "count through the alias", err)
	mustBe(t, "the alias names the collection", listed && through == 2, aliases)
	mustNoErr(t, "remove alias", db.VectorSetAlias(alias, ""))
	aliases, _ = db.VectorListAliases()
	for _, a := range aliases {
		if a.Alias == alias {
			t.Fatalf("the alias was not removed: %+v", aliases)
		}
	}

	mustNoErr(t, "named vector", db.VectorAddNamedVector(coll, "title", 2, tricoredb.MetricDot))
	mustNoErr(t, "upsert named", db.VectorUpsertNamed(coll, "n1",
		map[string][]float32{"default": {1, 1, 0}, "title": {1, 0}}, map[string]any{"kind": "named"}))
	config, err = db.VectorSetCollectionConfig(coll, tricoredb.VectorConfigChange{M: 8})
	mustNoErr(t, "config again", err)
	mustBe(t, "the named vector is declared", len(config.Named) == 1 && config.Named[0].Name == "title" &&
		config.Named[0].Dimension == 2 && config.Named[0].Metric == tricoredb.MetricDot, config)

	stats, err := db.VectorIndexStats(coll)
	mustNoErr(t, "index stats", err)
	mustBe(t, "a small collection is scanned", stats.Index == "flat" && stats.Stored == 3, stats)
	rebuilt, err := db.VectorRebuildIndex(coll)
	mustNoErr(t, "rebuild", err)
	mustBe(t, "rebuild", rebuilt.Rebuilt && rebuilt.Vectors == 3, rebuilt)
	recall, err := db.VectorMeasureRecall(coll, tricoredb.RecallOptions{})
	mustNoErr(t, "recall", err)
	mustBe(t, "a scanned collection has nothing to measure", !recall.Measured && recall.Recall == 1 && recall.Reason != "", recall)
}

func TestVectorGraphIndexIsMeasured(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 4, tricoredb.MetricCosine)
	items := make([]tricoredb.VectorUpsertItem, 300)
	for i := range items {
		x := float64(i)
		items[i] = tricoredb.VectorUpsertItem{
			ID:     fmt.Sprintf("v%d", i),
			Vector: []float32{float32(math.Sin(x)), float32(math.Cos(x)), float32(math.Sin(x * 0.5)), 1},
		}
	}
	written, err := db.VectorUpsertMany(coll, items)
	mustNoErr(t, "upsert many", err)
	mustBe(t, "upsert many", written == 300, written)

	stats, err := db.VectorIndexStats(coll)
	mustNoErr(t, "index stats", err)
	mustBe(t, "a graph was built", stats.Index == "hnsw" && stats.Live == 300 && stats.Layers >= 1 &&
		stats.Parameters.M > 0 && len(stats.LivePerLayer) == stats.Layers, stats)
	recall, err := db.VectorMeasureRecall(coll, tricoredb.RecallOptions{Sample: 5, TopK: 3})
	mustNoErr(t, "recall", err)
	mustBe(t, "recall was measured", recall.Measured && recall.Queries == 5 && recall.Recall > 0 && recall.Recall <= 1, recall)
}

// seedGraph writes a triangle a->b->c->a with a shortcut a->c, and an isolated
// node d.
func seedGraph(t *testing.T, db *tricoredb.Client) string {
	t.Helper()
	graph := newGraph(t, db)
	counts, err := db.GraphAddMany(graph,
		[]tricoredb.GraphNodeInput{
			{ID: "a", Labels: []string{"P"}, Properties: map[string]any{"name": "ada", "age": 30}},
			{ID: "b", Labels: []string{"P"}, Properties: map[string]any{"name": "bob"}},
			{ID: "c", Labels: []string{"P"}},
			{ID: "d"},
		},
		[]tricoredb.GraphEdgeInput{
			{ID: "e1", From: "a", To: "b", Label: "K", Properties: map[string]any{"w": 2}},
			{ID: "e2", From: "b", To: "c", Label: "K"},
			{ID: "e3", From: "c", To: "a", Label: "K"},
			{ID: "e4", From: "a", To: "c", Label: "K"},
		})
	mustNoErr(t, "add many", err)
	mustBe(t, "add many", counts.Nodes == 4 && counts.Edges == 4, counts)
	return graph
}

func TestGraphBulkWriteUpdateCopyAndRemove(t *testing.T) {
	db := newClient(t)
	graph := seedGraph(t, db)
	_, err := db.GraphAddMany(graph, nil, []tricoredb.GraphEdgeInput{{ID: "x", From: "a", To: "nobody", Label: "K"}})
	refusedByServer(t, "an edge to a node that does not exist", err)

	node, err := db.GraphUpdateNode(graph, "a", tricoredb.NodeUpdate{
		Properties: map[string]any{"age": nil, "city": "x"}, AddLabels: []string{"Q"}, RemoveLabels: []string{"P"},
	})
	mustNoErr(t, "update node", err)
	_, hasAge := node.Properties["age"]
	mustBe(t, "update node", len(node.Labels) == 1 && node.Labels[0] == "Q" && !hasAge &&
		node.Properties["city"] == "x" && node.Properties["name"] == "ada", node)
	_, err = db.GraphUpdateNode(graph, "nobody", tricoredb.NodeUpdate{})
	refusedByServer(t, "updating a node that does not exist", err)

	edge, err := db.GraphUpdateEdge(graph, "e1", map[string]any{"w": 3}, "KNOWS")
	mustNoErr(t, "update edge", err)
	mustBe(t, "update edge", edge.Label == "KNOWS" && edge.From == "a" && edge.To == "b" && edge.Properties["w"] == float64(3), edge)
	_, err = db.GraphUpdateEdge(graph, "e1", nil, "K")
	mustNoErr(t, "rename the edge back", err)

	imported, err := db.GraphImportCSV(graph, "id,labels,name\nx1,P,xavier\nx2,P,yan\n", "id,from,to,label\nxe1,x1,x2,K\n")
	mustNoErr(t, "import csv", err)
	mustBe(t, "import csv", imported.Nodes == 2 && imported.Edges == 1, imported)

	copyName := uniqueName("graph_copy")
	copied, err := db.GraphCopy(graph, copyName)
	mustNoErr(t, "copy", err)
	t.Cleanup(func() { _ = db.GraphDrop(copyName) })
	mustBe(t, "copy", copied.Nodes == 6 && copied.Edges == 5, copied)
	_, err = db.GraphCopy(graph, copyName)
	refusedByServer(t, "copying onto an existing graph", err)

	removed, err := db.GraphRemoveMany(copyName, []string{"a", "nobody"}, []string{"e2"}, true)
	mustNoErr(t, "remove many", err)
	mustBe(t, "e2 and the three edges touching a", removed.NodesRemoved == 1 && removed.EdgesRemoved == 4, removed)
	_, found, err := db.GraphGetNode(graph, "a")
	mustNoErr(t, "get node", err)
	mustBe(t, "the original is untouched", found, found)
}

func TestGraphWalksAndAlgorithms(t *testing.T) {
	db := newClient(t)
	graph := seedGraph(t, db)

	dfs, err := db.GraphDfs(graph, "a", tricoredb.TraverseOptions{})
	mustNoErr(t, "dfs", err)
	mustBe(t, "dfs", dfs.Count == 3 && dfs.Nodes[0].ID == "a", dfs)
	wantExactly(t, "dfs reaches what a traversal does", visitIDs(dfs.Nodes), "a", "b", "c")
	bounded, err := db.GraphDfs(graph, "a", tricoredb.TraverseOptions{Direction: tricoredb.DirectionBoth, Limit: 2})
	mustNoErr(t, "bounded dfs", err)
	mustBe(t, "bounded dfs", bounded.Truncated && bounded.Count == 2, bounded)

	sub, err := db.GraphSubgraph(graph, []string{"a"}, tricoredb.TraverseOptions{})
	mustNoErr(t, "subgraph", err)
	mustBe(t, "the induced subgraph", sub.NodeCount == 3 && sub.EdgeCount == 4 && len(sub.Edges) == 4, sub)

	paths, err := db.GraphKShortestPaths(graph, "a", "c", tricoredb.KPathOptions{})
	mustNoErr(t, "k shortest", err)
	mustBe(t, "k shortest", paths.Count == 2 && paths.Paths[0].Edges[0] == "e4" && paths.Paths[1].Hops == 2, paths)
	weighted, err := db.GraphKShortestPaths(graph, "a", "c", tricoredb.KPathOptions{K: 2, WeightProperty: "w"})
	mustNoErr(t, "k shortest weighted", err)
	mustBe(t, "the weight property is the cost", weighted.Paths[1].Cost == 3, weighted)
	none, err := db.GraphKShortestPaths(graph, "a", "d", tricoredb.KPathOptions{})
	mustNoErr(t, "k shortest to an isolated node", err)
	mustBe(t, "no path", none.Count == 0, none)
	all, err := db.GraphAllPaths(graph, "a", "c", tricoredb.TraverseOptions{})
	mustNoErr(t, "all paths", err)
	mustBe(t, "all paths", all.Count == 2 && all.Complete != nil && *all.Complete, all)

	components, err := db.GraphConnectedComponents(graph, tricoredb.ComponentOptions{})
	mustNoErr(t, "components", err)
	mustBe(t, "components", components.Count == 2 && components.Largest == 3, components)
	wantExactly(t, "the largest component", components.Components[0].Nodes, "a", "b", "c")
	strong, err := db.GraphConnectedComponents(graph, tricoredb.ComponentOptions{Kind: tricoredb.ComponentStrong})
	mustNoErr(t, "strong components", err)
	mustBe(t, "strong components", strong.Kind == tricoredb.ComponentStrong, strong)

	cycle, err := db.GraphDetectCycle(graph, false, "")
	mustNoErr(t, "cycle", err)
	mustBe(t, "cycle", cycle.HasCycle && cycle.Cycle != nil && len(cycle.Cycle.Nodes) == 3, cycle)
	noCycle, err := db.GraphDetectCycle(graph, true, "NOPE")
	mustNoErr(t, "cycle over no edges", err)
	mustBe(t, "no cycle", !noCycle.HasCycle && noCycle.Cycle == nil && !noCycle.Directed, noCycle)

	stats, err := db.GraphStats(graph)
	mustNoErr(t, "stats", err)
	mustBe(t, "stats", stats.Nodes == 4 && stats.Edges == 4 && stats.IsolatedNodes == 1 &&
		stats.NodeLabels["P"] == 3 && stats.EdgeTypes["K"] == 4, stats)
	schema, err := db.GraphSchema(graph)
	mustNoErr(t, "schema", err)
	mustBe(t, "schema", schema.EdgeTypes[0].Type == "K" && schema.UnlabeledNodes.Count == 1, schema)
	check, err := db.GraphCheckConsistency(graph)
	mustNoErr(t, "consistency", err)
	mustBe(t, "consistency", check.Consistent && check.Nodes == 4 && check.Edges == 4, check)

	for _, measure := range []tricoredb.CentralityMeasure{
		tricoredb.CentralityBetweenness, tricoredb.CentralityCloseness, tricoredb.CentralityEigenvector,
	} {
		scores, err := db.GraphCentrality(graph, measure, tricoredb.CentralityOptions{})
		mustNoErr(t, "centrality", err)
		mustBe(t, "centrality "+string(measure), scores.Measure == measure && len(scores.Results) == 4 && scores.Directed, scores)
	}
	undirected, err := db.GraphCentrality(graph, tricoredb.CentralityCloseness, tricoredb.CentralityOptions{Undirected: true, Limit: 2})
	mustNoErr(t, "undirected centrality", err)
	mustBe(t, "undirected centrality", !undirected.Directed && len(undirected.Results) == 2, undirected)
	communities, err := db.GraphCommunities(graph, tricoredb.CommunityOptions{})
	mustNoErr(t, "communities", err)
	mustBe(t, "communities", communities.Algorithm == "louvain" && communities.Count == 2, communities)
	triangles, err := db.GraphTriangleCount(graph, "", 0)
	mustNoErr(t, "triangles", err)
	mustBe(t, "triangles", triangles.Triangles == 1 && len(triangles.Nodes) == 4, triangles)
	similar, err := db.GraphNodeSimilarity(graph, "a", tricoredb.SimilarityOptions{})
	mustNoErr(t, "similarity", err)
	mustBe(t, "similarity", len(similar) == 2, similar)
	ranks, err := db.GraphPageRank(graph, tricoredb.PageRankOptions{Limit: 2})
	mustNoErr(t, "page rank", err)
	mustBe(t, "page rank", len(ranks.Ranks) == 2 && ranks.Count == 4 && ranks.Truncated && ranks.Damping == 0.85, ranks)
}

func TestGraphSchemaObjectsCypherWritesAndTransactions(t *testing.T) {
	db := newClient(t)
	graph := seedGraph(t, db)

	mustNoErr(t, "index", db.GraphCreateNodeIndex(graph, "by_name", "P", "name"))
	mustNoErr(t, "composite index", db.GraphCreateNodeIndex(graph, "by_nc", "", "name", "city"))
	mustNoErr(t, "constraint", db.GraphCreateConstraint(graph, "uniq_name", "P", "name", tricoredb.ConstraintUnique, ""))
	refusedByServer(t, "a constraint a stored edge breaks",
		db.GraphCreateConstraint(graph, "req_w", "K", "w", tricoredb.ConstraintRequired, tricoredb.TargetEdge))

	objects, err := db.GraphListSchemaObjects(graph)
	mustNoErr(t, "list schema objects", err)
	mustBe(t, "schema objects", len(objects.Indexes) == 2 && len(objects.Constraints) == 1 &&
		objects.Constraints[0].Kind == tricoredb.ConstraintUnique && objects.Constraints[0].Target == tricoredb.TargetNode, objects)

	found, err := db.GraphFindNodes(graph, "P", map[string]any{"name": "ada"}, 0)
	mustNoErr(t, "find", err)
	mustBe(t, "find through the index", found.Count == 1 && found.Nodes[0].ID == "a" && strings.Contains(found.Plan, "by_name"), found)
	two, err := db.GraphFindNodes(graph, "", nil, 2)
	mustNoErr(t, "find everything", err)
	mustBe(t, "find everything", two.Count == 2, two)
	refusedByServer(t, "the constraint is enforced",
		db.GraphAddNode(graph, "dup", []string{"P"}, map[string]any{"name": "ada"}))

	mustNoErr(t, "drop index", db.GraphDropSchemaObject(graph, "by_nc"))
	refusedByServer(t, "dropping it again", db.GraphDropSchemaObject(graph, "by_nc"))

	named := func(name string) int {
		r, err := db.GraphFindNodes(graph, "", map[string]any{"name": name}, 0)
		mustNoErr(t, "find by name", err)
		return r.Count
	}
	written, err := db.GraphExecute(graph, "CREATE (n:P {name: 'zed'})")
	mustNoErr(t, "execute", err)
	mustBe(t, "execute", written.NodesCreated == 1 && written.Statements == 1 && named("zed") == 1, written)

	txn, err := db.GraphBeginTransaction(graph)
	mustNoErr(t, "begin", err)
	db.SetRequestTxn(txn)
	queued, err := db.GraphExecute(graph, "CREATE (n:P {name: 'tx1'})")
	mustNoErr(t, "queue", err)
	mustBe(t, "a statement inside a transaction is queued", queued.Queued == 1 && queued.NodesCreated == 0, queued)
	db.SetRequestTxn("")
	mustBe(t, "nothing is applied yet", named("tx1") == 0, nil)
	commit, err := db.GraphCommitTransaction(graph, txn)
	mustNoErr(t, "commit", err)
	mustBe(t, "commit", commit.Committed == 1 && commit.NodesCreated == 1 && named("tx1") == 1, commit)

	txn, err = db.GraphBeginTransaction(graph)
	mustNoErr(t, "begin again", err)
	db.SetRequestTxn(txn)
	_, err = db.GraphExecute(graph, "CREATE (n:P {name: 'tx2'})")
	mustNoErr(t, "queue again", err)
	rolledBack, err := db.GraphRollbackTransaction(graph, txn)
	mustNoErr(t, "rollback", err)
	mustBe(t, "rollback", rolledBack == 1 && db.RequestTxn() == "" && named("tx2") == 0, rolledBack)
	_, err = db.GraphCommitTransaction(graph, txn)
	refusedByServer(t, "committing a finished transaction", err)
}
