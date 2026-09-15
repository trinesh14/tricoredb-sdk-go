// Package e2ehardening is the Go driver's hardening suite.
//
// Two kinds of peer, and the choice matters for what each case proves.
//
//   - A *hostile peer* — a raw net.Listener speaking real frame bytes on a real
//     socket. The frame-ceiling, header-version, deadline and handshake-integrity
//     cases need a peer that misbehaves, and the canonical server never does. The
//     driver's own read path is what is under test, so nothing here is mocked:
//     every case opens a socket and every byte is one this driver must parse.
//
//   - A *real tricore-server* on an ephemeral port. The request-id and
//     response-status cases prove what the server sees and produces, so a
//     locally-constructed assertion would prove nothing: the id asserted on is
//     the one the server echoed back, and the status asserted on is one only the
//     server can produce.
//
// Every assertion that touches a socket runs under a hard watchdog (see
// within). A driver that waits for ever must FAIL, not stall: the sibling
// Python workstream had a RED run hang for 35 minutes and produce nothing.
//
// Run with:
//
//	go test ./e2e_hardening            # needs target/{release,debug}/tricore-server
//	TRICORE_SERVER_BIN=... go test ./e2e_hardening
package e2ehardening

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trinesh14/tricoredb-sdk-go"
)

// -- watchdog --------------------------------------------------------------

// within runs fn on its own goroutine and fails the test if it has not returned
// by d.
//
// A stopwatch consulted after the fact is not enough: a driver with no deadline
// blocks for ever, so the assertion has to be able to give up on it. That is the
// difference between a RED run that reports a failure and one that hangs
// overnight.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still waiting after %s (it hung)", what, d)
	}
}

// -- hostile peer ----------------------------------------------------------

// peer is a raw TCP listener that speaks real frame bytes and misbehaves on
// purpose.
type peer struct {
	ln net.Listener
}

func startPeer(t *testing.T, script func(net.Conn)) *peer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		script(c)
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return &peer{ln: ln}
}

func (p *peer) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *peer) connect(t *testing.T, opts tricoredb.Options) (*tricoredb.Client, error) {
	t.Helper()
	opts.Host = "127.0.0.1"
	opts.Port = p.port()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tricoredb.Connect(ctx, opts)
}

func (p *peer) mustConnect(t *testing.T, opts tricoredb.Options) *tricoredb.Client {
	t.Helper()
	db, err := p.connect(t, opts)
	if err != nil {
		t.Fatalf("connect to the peer: %v", err)
	}
	return db
}

func writeHeader(c net.Conn, version byte, tag byte, length uint32) {
	h := make([]byte, 6)
	h[0] = version
	h[1] = tag
	binary.BigEndian.PutUint32(h[2:], length)
	_, _ = c.Write(h)
}

func writeFrame(c net.Conn, tag byte, json string) {
	writeHeader(c, 1, tag, uint32(len(json)))
	_, _ = io.WriteString(c, json)
}

// expectFrame reads one whole frame from the driver, so the script stays in
// step with it.
func expectFrame(c net.Conn) error {
	h := make([]byte, 6)
	if _, err := io.ReadFull(c, h); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(h[2:])
	if n > 0 {
		if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
			return err
		}
	}
	return nil
}

const helloOK = `{"ok":true,"features":1}`

// -- the cases -------------------------------------------------------------

func TestAResponseDeclaringFourGiBIsRefusedBeforeAnythingIsAllocated(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		writeHeader(c, 1, 3, 0xFFFFFFFF) // and never a body
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 5*time.Second, "the oversized RESPONSE", func() {
		_, err := db.Execute("SELECT 1")
		if !errors.Is(err, tricoredb.ErrProtocol) {
			t.Errorf("want a protocol error, got %v", err)
			return
		}
		if !strings.Contains(err.Error(), "exceeds the protocol ceiling") {
			t.Errorf("want the ceiling named, got %v", err)
		}
		if !db.Poisoned() {
			t.Error("the connection should be poisoned")
		}
	})
}

func TestAControlFrameTakesTheTighterCeiling(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		writeHeader(c, 1, 5, 1024*1024) // a 1 MiB PONG
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 5*time.Second, "the 1 MiB PONG", func() {
		err := db.Ping()
		if !errors.Is(err, tricoredb.ErrProtocol) || !strings.Contains(err.Error(), "65536") {
			t.Errorf("want the 64 KiB control ceiling named, got %v", err)
		}
	})
}

func TestAFrameHeaderVersionThisBuildCannotReadIsRefusedByName(t *testing.T) {
	// The reply carries the tag the caller is WAITING for (PONG), so the version
	// is the only thing left that can refuse it. A PING-tagged reply would be
	// refused on the tag alone and this case would pass with the version check
	// deleted — which is exactly what happened to the sibling Python suite.
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		writeHeader(c, 99, 5, 0)
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 5*time.Second, "the version-99 PONG", func() {
		err := db.Ping()
		if err == nil || !strings.Contains(err.Error(), "version 99") {
			t.Errorf("want a refusal naming version 99, got %v", err)
		}
	})
}

func TestAPoisonedConnectionStaysPoisoned(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		writeHeader(c, 99, 5, 0)
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 5*time.Second, "two uses of a poisoned connection", func() {
		if err := db.Ping(); err == nil {
			t.Fatal("the first refusal returned nil")
		}
		err := db.Ping()
		if err == nil || !strings.Contains(err.Error(), "cannot be reused") {
			t.Errorf("want the second use refused by the recorded fault, got %v", err)
		}
	})
}

func TestATwoMiBResponseStillArrivesWhole(t *testing.T) {
	// The ceiling must narrow nothing a working peer sends.
	big := strings.Repeat("a", 2*1024*1024)
	body := `{"request_id":"x","status":"ok","data":{"Message":"` + big + `"}}`
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		writeFrame(c, 3, body)
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 10*time.Second, "a 2 MiB RESPONSE", func() {
		resp, err := db.Execute("SELECT 1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != "ok" {
			t.Fatalf("status = %q", resp.Status)
		}
		// The raw payload is the JSON string, so it carries the two quotes too.
		if len(resp.DataRaw) < len(big) {
			t.Errorf("the payload was truncated: %d bytes for a %d-byte value",
				len(resp.DataRaw), len(big))
		}
	})
}

func TestAStatusThisBuildDoesNotKnowFailsClosed(t *testing.T) {
	// The kernel has three statuses today. The check compares against "ok"
	// rather than enumerating the bad ones precisely so that a fourth, added to
	// the server after this driver shipped, is refused rather than handed to a
	// caller as a completed operation. Enumerating is correct for today's kernel
	// and open by construction for tomorrow's.
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		writeFrame(c, 3, `{"request_id":"x","status":"degraded","data":{"Message":"partially applied"}}`)
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 5*time.Second, "a status this build has never heard of", func() {
		resp, err := db.Execute("SELECT 1")
		if err == nil {
			t.Fatalf("RETURNED %+v instead of failing", resp)
		}
		var se *tricoredb.ServerError
		if !errors.As(err, &se) || se.Status != "degraded" {
			t.Errorf("want a *ServerError naming the status, got %v", err)
		}
	})
}

func TestCloseIsBoundedWhenThePeerNeverAnswersBye(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c) // CLOSE, and then silence for ever
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 8*time.Second, "close() against a peer that never says goodbye", func() {
		start := time.Now()
		_ = db.Close()
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Close took %s", elapsed)
		}
	})
}

func TestAReadDeadlineSurfacesAsATypedFatalTimeout(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c) // PING, never answered
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{ReadTimeout: 300 * time.Millisecond})
	within(t, 5*time.Second, "an unanswered PING under a read deadline", func() {
		err := db.Ping()
		if !errors.Is(err, tricoredb.ErrTimeout) {
			t.Fatalf("want ErrTimeout, got %v", err)
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Errorf("want something net.Error-shaped so existing code keeps working, got %v", err)
		}
		if !db.Poisoned() {
			t.Error("a timed-out connection is fatal, not reusable")
		}
	})
}

func TestTheHandshakeDeadlineDoesNotBecomeAPerQueryDeadline(t *testing.T) {
	// The defect this pins is the Python driver's: a connect timeout left armed
	// on the socket, silently killing any statement that ran longer than it.
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c)
		time.Sleep(900 * time.Millisecond) // longer than the handshake budget below
		writeFrame(c, 3, `{"request_id":"x","status":"ok","data":{"Message":"slow"}}`)
		select {}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	db, err := tricoredb.Connect(ctx, tricoredb.Options{Host: "127.0.0.1", Port: p.port()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	within(t, 5*time.Second, "a statement outliving the handshake budget", func() {
		resp, err := db.Execute("SELECT 1")
		if err != nil {
			t.Fatalf("the handshake deadline leaked onto the query: %v", err)
		}
		if resp.Status != "ok" {
			t.Errorf("status = %q", resp.Status)
		}
	})
}

func TestHelloOKWithOkFalseIsARefusal(t *testing.T) {
	// The tag name is not the verdict.
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, `{"ok":false,"message":"this node is draining"}`)
		select {}
	})
	within(t, 5*time.Second, "a HELLO_OK carrying ok=false", func() {
		_, err := p.connect(t, tricoredb.Options{})
		if err == nil || !strings.Contains(err.Error(), "draining") {
			t.Errorf("want the refusal message, got %v", err)
		}
	})
}

func TestAuthOKWithOkFalseIsARefusalAndTheSecretIsNotEchoed(t *testing.T) {
	const secret = "s3cr3t-do-not-echo"
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c) // AUTH
		writeFrame(c, 9, `{"ok":false,"message":"bad credentials"}`)
		select {}
	})
	within(t, 5*time.Second, "an AUTH_OK carrying ok=false", func() {
		_, err := p.connect(t, tricoredb.Options{User: "admin", Secret: secret})
		if !errors.Is(err, tricoredb.ErrAuth) {
			t.Fatalf("want ErrAuth, got %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Error("the secret appears in the error")
		}
	})
}

func TestAHelloOKWithNoFeaturesFieldGrantsZero(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, `{"ok":true}`)
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	if got := db.GrantedFeatures(); got != 0 {
		t.Errorf("GrantedFeatures = %d, want 0 — never \"everything\"", got)
	}
}

func TestPingCannotStealARequestsReply(t *testing.T) {
	// It goes through the in-flight guard like every other frame pair.
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		_ = expectFrame(c) // REQUEST, deliberately unanswered
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{ReadTimeout: 3 * time.Second})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = db.Execute("SELECT 1") // the read deadline ends it
	}()
	time.Sleep(200 * time.Millisecond) // let the REQUEST reach the peer
	within(t, 5*time.Second, "a Ping beside an in-flight request", func() {
		err := db.Ping()
		if err == nil || !strings.Contains(err.Error(), "already in flight") {
			t.Errorf("want the in-flight guard, got %v", err)
		}
	})
	wg.Wait()
}

func TestAnOutboundControlFrameOverTheCeilingIsRefusedLocally(t *testing.T) {
	p := startPeer(t, func(c net.Conn) {
		_ = expectFrame(c)
		writeFrame(c, 8, helloOK)
		select {}
	})
	db := p.mustConnect(t, tricoredb.Options{})
	within(t, 5*time.Second, "an oversized CANCEL", func() {
		_, err := db.Cancel(strings.Repeat("k", 70*1024))
		if !errors.Is(err, tricoredb.ErrProtocol) || !strings.Contains(err.Error(), "control frames") {
			t.Errorf("want a local refusal naming the control ceiling, got %v", err)
		}
		if db.Poisoned() {
			t.Error("a frame this driver refused before writing must not poison the connection")
		}
	})
}

// -- against a real server -------------------------------------------------

func TestTwoConnectionsOfOnePrincipalIssueDifferentRequestIDs(t *testing.T) {
	a := newClient(t)
	b := newClient(t)
	ra, err := a.Request(sqlQuery("SELECT 1"))
	mustNoErr(t, "query on a", err)
	rb, err := b.Request(sqlQuery("SELECT 1"))
	mustNoErr(t, "query on b", err)
	// The ids the SERVER echoed, not the ones this process believes it sent.
	if ra.RequestID == rb.RequestID {
		t.Fatalf("two connections of one principal issue different ids (got %q and %q)",
			ra.RequestID, rb.RequestID)
	}
	if !strings.HasPrefix(ra.RequestID, "go-") {
		t.Errorf("the id no longer names the SDK: %q", ra.RequestID)
	}
}

func TestSixConcurrentPooledRequestsReachTheServerUnderSixDistinctIDs(t *testing.T) {
	requireServer(t)
	// The decisive case. ExecutionRegistry::cancel stops EVERY entry matching
	// (principal, request_id), and a Pool's connections all authenticate as one
	// principal — so a per-connection counter put six live statements under one
	// id and made one cancel stop all six.
	pool, err := tricoredb.NewPool(tricoredb.Options{
		Host: shared.host, Port: shared.port, User: "admin", Secret: "pw",
	}, 6)
	mustNoErr(t, "new pool", err)
	defer pool.Close()

	var mu sync.Mutex
	ids := map[string]struct{}{}
	var wg sync.WaitGroup
	gate := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(6)

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := pool.Use(ctx, func(c *tricoredb.Client) error {
				// Hold every connection at once, so all six are the FIRST request
				// on their own socket — the case a per-connection counter collides on.
				ready.Done()
				<-gate
				resp, err := c.Request(sqlQuery("SELECT 1"))
				if err != nil {
					return err
				}
				mu.Lock()
				ids[resp.RequestID] = struct{}{}
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("pooled request: %v", err)
			}
		}()
	}
	ready.Wait()
	close(gate)
	wg.Wait()

	if len(ids) != 6 {
		t.Fatalf("six concurrent pooled requests reach the server under six distinct ids "+
			"(got %d, want 6)", len(ids))
	}
}

func TestANotImplementedStatusIsAFailureNotAResult(t *testing.T) {
	db := newClient(t)
	// Live on any node without [sharding]: a recognized hook that was NOT run.
	resp, err := db.Request(map[string]any{"Admin": "RebalanceStatus"})
	if err == nil {
		t.Fatalf("Admin::RebalanceStatus RETURNED %+v instead of failing", resp)
	}
	var se *tricoredb.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("want a *ServerError, got %T: %v", err, err)
	}
	if se.Status != "not_implemented" {
		t.Errorf("Status = %q, want not_implemented", se.Status)
	}
}

func TestAnOrdinaryServerErrorDoesNotRetireAPooledConnection(t *testing.T) {
	requireServer(t)
	pool, err := tricoredb.NewPool(tricoredb.Options{
		Host: shared.host, Port: shared.port, User: "admin", Secret: "pw",
	}, 1)
	mustNoErr(t, "new pool", err)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = pool.Use(ctx, func(c *tricoredb.Client) error {
		_, e := c.Query("SELECT * FROM no_such_table")
		return e
	})
	if !errors.Is(err, tricoredb.ErrServer) {
		t.Fatalf("want ErrServer, got %v", err)
	}
	idle, _ := pool.Stats()
	if idle != 1 {
		t.Errorf("the connection should have gone back to the pool (idle=%d)", idle)
	}
}

func sqlQuery(statement string) map[string]any {
	return map[string]any{"Sql": map[string]any{"Query": map[string]any{"sql": statement}}}
}

func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// -- the shared server -----------------------------------------------------

// serverConfig turns on every client-facing module. They are off or partly off
// in the shipped default (crates/tricore_core/src/config/modules.rs), and
// dev_auth defaults to false, so a server started without this refuses both the
// operations and the credentials these tests use — for reasons that read like
// SDK faults and are not.
const serverConfig = `
[server]
host = "127.0.0.1"
port = 0
protocol = "tricore"
node_id = "sdk-go-hardening"
region_id = "local"
shutdown_grace_secs = 1

[modules]
sql = true
document = true
cache = true
vector = true
graph = true
llm = true
cluster = true

[security]
auth_mode = "password"
dev_auth = true
allow_default_admin = false

[tls]
enabled = false
`

type server struct {
	cmd  *exec.Cmd
	host string
	port int
}

var shared *server

// skipReason is set when no tricore-server binary is available. Tests that
// need a real server then skip with it; tests that use an in-process peer
// still run. A binary that exists but fails to start is a failure, not a skip.
var skipReason string

func TestMain(m *testing.M) {
	if _, err := findServerBinary(); err != nil {
		skipReason = err.Error()
		os.Exit(m.Run())
	}
	srv, err := startServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_hardening: cannot start tricore-server: %v\n", err)
		os.Exit(1)
	}
	shared = srv
	code := m.Run()
	srv.stop()
	os.Exit(code)
}

// requireServer skips the calling test when no server is available.
func requireServer(t *testing.T) {
	t.Helper()
	if shared == nil {
		t.Skipf("no tricore-server: %s", skipReason)
	}
}

func newClient(t *testing.T) *tricoredb.Client {
	t.Helper()
	requireServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := tricoredb.Connect(ctx, tricoredb.Options{
		Host: shared.host, Port: shared.port, User: "admin", Secret: "pw",
	})
	if err != nil {
		t.Fatalf("connect to %s:%d: %v", shared.host, shared.port, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func findServerBinary() (string, error) {
	if p := os.Getenv("TRICORE_SERVER_BIN"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("TRICORE_SERVER_BIN=%q: %w", p, err)
		}
		return p, nil
	}
	name := "tricore-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		for _, profile := range []string{"release", "debug"} {
			candidate := filepath.Join(dir, "target", profile, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no target/{release,debug}/%s found above the test directory "+
				"(build it, or set TRICORE_SERVER_BIN)", name)
		}
		dir = parent
	}
}

func startServer() (*server, error) {
	bin, err := findServerBinary()
	if err != nil {
		return nil, err
	}
	base, err := os.MkdirTemp("", "tricore-go-hardening-")
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(base, "tricore.toml")
	if err := os.WriteFile(cfgPath, []byte(serverConfig), 0o600); err != nil {
		return nil, err
	}
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, "--config", cfgPath, "--port", "0", "--data-dir", dataDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	addr, err := readListenAddr(stdout, 60*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	host, portStr, ok := strings.Cut(addr, ":")
	if !ok {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("unparseable listen address %q", addr)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port == 0 {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("unparseable listen port in %q", addr)
	}
	return &server{cmd: cmd, host: host, port: port}, nil
}

// readListenAddr scans the server's stdout for the line reporting the bound
// address, then keeps draining so a full pipe never blocks the server.
func readListenAddr(stdout io.ReadCloser, timeout time.Duration) (string, error) {
	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 1)
	var once sync.Once
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if rest, ok := strings.CutPrefix(line, "listening on "); ok {
				addr, _, _ := strings.Cut(rest, " ")
				once.Do(func() { ch <- result{addr: addr} })
			}
		}
		once.Do(func() { ch <- result{err: errors.New("server exited before reporting a listen address")} })
	}()
	select {
	case r := <-ch:
		return r.addr, r.err
	case <-time.After(timeout):
		return "", fmt.Errorf("server did not report a listen address within %s", timeout)
	}
}

func (s *server) stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Kill()
	_, _ = s.cmd.Process.Wait()
}
