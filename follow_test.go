package tricoredb

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// peer is a scripted server: it completes HELLO and AUTH, and answers each
// REQUEST with reply(requestID, n). A follower's refusal was applied nowhere,
// which is what makes sending a request again safe; these peers stand in for
// a follower and a leader.
type peer struct {
	ln          net.Listener
	reply       func(rid string, n int) map[string]any
	mu          sync.Mutex
	connections int
	requests    int
	users       []string
	requestIDs  []string
}

func startPeer(t *testing.T, reply func(rid string, n int) map[string]any) *peer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &peer{ln: ln, reply: reply}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.connections++
			p.mu.Unlock()
			go p.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *peer) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *peer) addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port())) }

func (p *peer) counts() (connections, requests int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.connections, p.requests
}

func (p *peer) serve(conn net.Conn) {
	defer conn.Close()
	for {
		f, err := readFrame(conn)
		if err != nil {
			return
		}
		var body map[string]any
		_ = json.Unmarshal(f.body, &body)
		switch f.tag {
		case tagHello:
			_ = writeFrame(conn, tagHelloOK, map[string]any{"ok": true, "features": FeatureSessionTxn})
		case tagAuth:
			p.mu.Lock()
			p.users = append(p.users, body["username"].(string))
			p.mu.Unlock()
			_ = writeFrame(conn, tagAuthOK, map[string]any{"ok": true, "session_id": "s1"})
		case tagClose:
			_ = writeFrame(conn, tagBye, map[string]any{})
			return
		case tagRequest:
			rid := body["request_id"].(string)
			p.mu.Lock()
			p.requests++
			n := p.requests
			p.requestIDs = append(p.requestIDs, rid)
			p.mu.Unlock()
			_ = writeFrame(conn, tagResponse, p.reply(rid, n))
		}
	}
}

func peerDiagnostics(extra map[string]any) map[string]any {
	d := map[string]any{"module": nil, "region_id": "local", "route": "", "cache": "",
		"elapsed_ms": 0, "warnings": []string{}}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func okReply(rid string) map[string]any {
	return map[string]any{"request_id": rid, "status": "ok",
		"data": map[string]any{"Json": map[string]any{"transaction": "began"}}, "diagnostics": peerDiagnostics(nil)}
}

func refusal(rid, code, leader string) map[string]any {
	extra := map[string]any{"error_code": code}
	if leader != "" {
		extra["leader_hint"] = leader
	}
	return map[string]any{"request_id": rid, "status": "error",
		"data": map[string]any{"Message": "not the raft leader"}, "diagnostics": peerDiagnostics(extra)}
}

func connectTo(t *testing.T, p *peer, opts Options) *Client {
	t.Helper()
	opts.Host, opts.Port = "127.0.0.1", p.port()
	if opts.LeaderRetryDelay == 0 {
		opts.LeaderRetryDelay = time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := Connect(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestARefusedRequestGoesToTheLeaderAndStaysThere(t *testing.T) {
	leader := startPeer(t, func(rid string, _ int) map[string]any { return okReply(rid) })
	follower := startPeer(t, func(rid string, _ int) map[string]any {
		return refusal(rid, ErrorCodeNotLeader, leader.addr())
	})
	db := connectTo(t, follower, Options{User: "u", Secret: "s"})

	if _, err := db.Execute("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("the write should have reached the leader: %v", err)
	}
	if db.Address() != leader.addr() {
		t.Fatalf("address = %s, want %s", db.Address(), leader.addr())
	}
	if _, err := db.Execute("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	_, fr := follower.counts()
	lc, lr := leader.counts()
	if fr != 1 || lr != 2 || lc != 1 {
		t.Fatalf("follower requests %d, leader requests %d over %d connections", fr, lr, lc)
	}
	if leader.users[0] != "u" {
		t.Fatalf("the leader connection authenticated as %q", leader.users[0])
	}
	if leader.requestIDs[0] != follower.requestIDs[0] {
		t.Fatalf("the resent request has a new id: %s vs %s", leader.requestIDs[0], follower.requestIDs[0])
	}
}

func TestWithNoLeaderNamedTheClientWaitsAndAsksAgain(t *testing.T) {
	p := startPeer(t, func(rid string, n int) map[string]any {
		if n < 3 {
			return refusal(rid, ErrorCodeNotLeader, "")
		}
		return okReply(rid)
	})
	db := connectTo(t, p, Options{})
	if _, err := db.Execute("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if c, r := p.counts(); c != 1 || r != 3 {
		t.Fatalf("connections %d, requests %d", c, r)
	}
}

func TestRetriesAreBoundedAndReported(t *testing.T) {
	p := startPeer(t, func(rid string, _ int) map[string]any { return refusal(rid, ErrorCodeNotLeader, "") })
	db := connectTo(t, p, Options{LeaderRetries: 2})
	_, err := db.Execute("INSERT INTO t VALUES (1)")
	var se *ServerError
	if !errors.As(err, &se) || !se.IsNotLeader() || se.Retried != 2 {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "sent again 2 times") {
		t.Fatalf("message: %v", err)
	}
	if _, r := p.counts(); r != 3 {
		t.Fatalf("requests %d, want 3", r)
	}
}

func TestDisableFollowLeaderReturnsTheFirstRefusal(t *testing.T) {
	leader := startPeer(t, func(rid string, _ int) map[string]any { return okReply(rid) })
	follower := startPeer(t, func(rid string, _ int) map[string]any {
		return refusal(rid, ErrorCodeNotLeader, leader.addr())
	})
	db := connectTo(t, follower, Options{DisableFollowLeader: true})
	_, err := db.Execute("INSERT INTO t VALUES (1)")
	var se *ServerError
	if !errors.As(err, &se) || se.LeaderHint != leader.addr() || se.Retried != 0 {
		t.Fatalf("got %v", err)
	}
	if c, _ := leader.counts(); c != 0 {
		t.Fatalf("the leader was dialled %d times", c)
	}
}

func TestAnUnreachableLeaderLeavesTheConnectionWhereItWas(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	nowhere := ln.Addr().String()
	_ = ln.Close()
	p := startPeer(t, func(rid string, n int) map[string]any {
		if n == 1 {
			return refusal(rid, ErrorCodeNotLeader, nowhere)
		}
		return okReply(rid)
	})
	db := connectTo(t, p, Options{})
	if _, err := db.Execute("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if db.Address() != p.addr() {
		t.Fatalf("address moved to %s", db.Address())
	}
	if _, r := p.counts(); r != 2 {
		t.Fatalf("requests %d, want 2", r)
	}
}

func TestNothingIsSentAgainInsideASessionTransaction(t *testing.T) {
	p := startPeer(t, func(rid string, n int) map[string]any {
		if n == 1 {
			return okReply(rid)
		}
		return refusal(rid, ErrorCodeNotLeader, "")
	})
	db := connectTo(t, p, Options{})
	if _, err := db.Begin(); err != nil {
		t.Fatal(err)
	}
	_, err := db.Execute("INSERT INTO t VALUES (1)")
	var se *ServerError
	if !errors.As(err, &se) || !se.IsNotLeader() {
		t.Fatalf("got %v", err)
	}
	if _, r := p.counts(); r != 2 {
		t.Fatalf("requests %d, want 2", r)
	}
}

func TestOutcomeUnknownIsNeverSentAgain(t *testing.T) {
	p := startPeer(t, func(rid string, _ int) map[string]any {
		return refusal(rid, ErrorCodeOutcomeUnknown, "")
	})
	db := connectTo(t, p, Options{})
	_, err := db.Execute("INSERT INTO t VALUES (1)")
	var se *ServerError
	if !errors.As(err, &se) || se.Code != ErrorCodeOutcomeUnknown || se.IsNotLeader() {
		t.Fatalf("got %v", err)
	}
	if _, r := p.counts(); r != 1 {
		t.Fatalf("requests %d, want 1", r)
	}
}
