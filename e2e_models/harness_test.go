// Package e2emodels exercises the tricoredb driver's document, vector, and
// graph APIs against a real tricore-server process.
//
// Every test in this package talks to a live server. There are no stubs and no
// fakes: a stub that returned an empty slice would pass a membership test, so
// the read-path tests here are written as distinct-value witnesses — they
// assert the value that came back is the one written *and* that the wrong
// answer is genuinely absent.
//
// Run with: go test ./e2e_models
//
// The server binary is found by walking up to the repository's
// target/debug/tricore-server.exe, or named by TRICORE_SERVER_BIN. It is never
// built here — building the Rust workspace from a test is slow and collides
// with anything else compiling.
package e2emodels

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

// serverConfig enables the three model modules. They are OFF in the shipped
// default (crates/tricore_core/src/config/modules.rs), so a server started
// without this refuses every document/vector/graph op — which reads exactly
// like an unimplemented operation and is not one.
//
// port = 0 asks the OS for an ephemeral port. The bound port is read back from
// the server's own "listening on" line, never assumed: sibling test runs bind
// their own servers on the same host at the same time.
const serverConfig = `
[server]
host = "127.0.0.1"
port = 0
protocol = "tricore"
node_id = "sdk-go-e2e-models"
region_id = "local"

[modules]
sql = true
document = true
cache = true
vector = true
graph = true
llm = false
cluster = false

[security]
auth_mode = "password"
dev_auth = true
allow_default_admin = false
`

// server is the tricore-server process this package's tests share.
type server struct {
	cmd  *exec.Cmd
	addr string
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
		fmt.Fprintf(os.Stderr, "e2e_models: cannot start tricore-server: %v\n", err)
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

// findServerBinary locates the debug server binary by walking up from the test
// working directory. TRICORE_SERVER_BIN overrides the search.
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
		candidate := filepath.Join(dir, "target", "debug", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no target/debug/%s found above the test directory "+
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

	base, err := os.MkdirTemp("", "tricore-go-models-")
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

	return &server{cmd: cmd, addr: addr, host: host, port: port}, nil
}

// readListenAddr scans the server's stdout for the line that reports the bound
// address, then keeps draining stdout so a full pipe never blocks the server.
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
				// "listening on 127.0.0.1:54321" or "... (once)".
				addr, _, _ := strings.Cut(rest, " ")
				once.Do(func() { ch <- result{addr: addr} })
			}
		}
		once.Do(func() { ch <- result{err: fmt.Errorf("server exited before reporting a listen address")} })
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

// newClient dials the shared server and closes the connection when the test
// ends.
func newClient(t *testing.T) *tricoredb.Client {
	t.Helper()
	requireServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := tricoredb.Connect(ctx, tricoredb.Options{
		Host: shared.host, Port: shared.port, User: "admin", Secret: "pw",
	})
	if err != nil {
		t.Fatalf("connect to %s: %v", shared.addr, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// -- assertion helpers -----------------------------------------------------

func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", what, err)
	}
}

// num reads a JSON number out of a decoded document. encoding/json gives every
// number back as float64, so a test that asserts against an int literal without
// this fails for the wrong reason.
func num(t *testing.T, doc tricoredb.Document, key string) float64 {
	t.Helper()
	v, ok := doc[key]
	if !ok {
		t.Fatalf("document %v has no field %q", doc, key)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("document field %q is %T (%v), not a number", key, v, v)
	}
	return f
}

func str(t *testing.T, doc tricoredb.Document, key string) string {
	t.Helper()
	v, ok := doc[key]
	if !ok {
		t.Fatalf("document %v has no field %q", doc, key)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("document field %q is %T (%v), not a string", key, v, v)
	}
	return s
}

// idsOf lists the "_id" of each document, for set comparisons that name both
// what must be present and what must be absent.
func idsOf(t *testing.T, docs []tricoredb.Document) []string {
	t.Helper()
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = str(t, d, "_id")
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// wantExactly compares a set of ids without caring about order, and reports
// both the missing and the unexpected — "the right answer is present" and "the
// wrong answer is absent" are different claims and both matter here.
func wantExactly(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("%s: expected %q in %v, absent", what, w, got)
		}
	}
	for _, g := range got {
		if !contains(want, g) {
			t.Errorf("%s: unexpected %q in result %v (wanted exactly %v)", what, g, got, want)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%s: expected %d results %v, got %d %v", what, len(want), want, len(got), got)
	}
}

// uniqueName keeps concurrently-running lanes and repeated runs from colliding
// on a shared server, and keeps one failing test from poisoning the next.
func uniqueName(prefix string) string {
	return fmt.Sprintf("go_%s_%d", prefix, time.Now().UnixNano()%1_000_000_000)
}
