package tricoredb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultPort is the port tricore-server listens on when not configured
// otherwise.
const DefaultPort = 8427

// Options configures a new connection. Host, Port, Database, and ClientName
// fall back to sane defaults when left zero; User/Secret are only sent (via
// an AUTH frame) when User is non-empty, matching servers that allow
// unauthenticated sessions.
type Options struct {
	// Host is the server address. Defaults to "127.0.0.1".
	Host string
	// Port is the server's native protocol port. Defaults to [DefaultPort].
	Port int

	// User is the principal to authenticate as. Empty skips AUTH.
	User string
	// Secret is the password or token sent with User.
	Secret string

	// Database is sent as the `database` field of every REQUEST frame.
	// Defaults to "main".
	Database string

	// ClientName identifies this driver in the HELLO frame. Defaults to
	// "tricoredb-go".
	ClientName string

	// DialTimeout bounds the TCP connect only. Use ctx for the deadline
	// on the handshake/auth that follow.
	DialTimeout time.Duration

	// ReadTimeout is a client-side deadline on each steady-state read, or
	// zero (the default) for none.
	//
	// Deliberately separate from ctx's handshake deadline, which Connect
	// clears once AUTH completes: conflating the two is how the Python driver
	// turned a documented 10s *connect* timeout into an undocumented 10s
	// deadline on every query it would ever run. Deliberately separate also
	// from SetRequestTimeout, which makes the *server* stop; this one only
	// stops the wait.
	//
	// There is no default. A read legitimately blocks for exactly as long as
	// the statement runs, the server's own statement_timeout_ms defaults to
	// unlimited, and any number this driver picked would be a guess at a
	// guarantee the server does not make.
	ReadTimeout time.Duration

	// TLS turns on TLS when non-nil. Plain TCP otherwise — on which the
	// Secret above crosses the wire in the clear.
	TLS *TLSOptions

	// Features is the capability bitmap announced in HELLO. Nil (the default)
	// means [Features] — everything this build understands. The server grants
	// only what was asked for, so masking a bit out here is how a caller opts
	// out of one:
	//
	//	mask := tricoredb.Features &^ tricoredb.FeatureSessionTxn
	//	opts.Features = &mask
	//
	// A pointer rather than a plain uint64 because zero is a meaningful value
	// here — "announce nothing" — and must not be indistinguishable from
	// "unset".
	Features *uint64
}

// TLSOptions configures the TLS session. It mirrors the Rust client's
// TlsOptions (crates/tricore_client/src/tls.rs).
//
// Supplying a *TLSOptions at all is what enables TLS. Once enabled the server
// certificate is verified and its name checked, unless
// DangerAcceptInvalidCerts is set.
//
// # mTLS
//
// Set ClientCertFile and ClientKeyFile to present a client identity. A server
// with require_client_cert = true rejects a client that does not.
//
// # Secret hygiene
//
// Private key contents are never logged. Errors name the *path* so an operator
// can find the file, never the bytes in it.
type TLSOptions struct {
	// CAFile is a PEM bundle used to verify the server. When empty the trust
	// store is left EMPTY rather than falling back to the system roots —
	// matching the Rust client, and making a typo'd path fail closed instead
	// of quietly succeeding against some unrelated public CA.
	CAFile string

	// ServerName is the expected server name (SNI and certificate name).
	// Defaults to "localhost".
	ServerName string

	// DangerAcceptInvalidCerts disables certificate and hostname verification.
	// DEVELOPMENT ONLY. A connection with this set looks encrypted but
	// authenticates nothing, so an active attacker can sit in the middle
	// undetected — which is strictly worse than visibly using plain TCP.
	DangerAcceptInvalidCerts bool

	// ClientCertFile is a PEM client certificate chain to present (mTLS).
	// Requires ClientKeyFile.
	ClientCertFile string
	// ClientKeyFile is the PEM private key for ClientCertFile (mTLS).
	ClientKeyFile string
}

// config builds the *tls.Config. Verification stays on unless explicitly
// disabled: InsecureSkipVerify is set from the loudly-named field and nowhere
// else.
func (o *TLSOptions) config() (*tls.Config, error) {
	if (o.ClientCertFile == "") != (o.ClientKeyFile == "") {
		missing := "ClientKeyFile"
		if o.ClientKeyFile != "" {
			missing = "ClientCertFile"
		}
		return nil, &ProtocolError{Message: fmt.Sprintf(
			"tls %s is required alongside the other (both are needed for mTLS)", missing)}
	}

	serverName := o.ServerName
	if serverName == "" {
		serverName = "localhost"
	}

	cfg := &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: o.DangerAcceptInvalidCerts, //nolint:gosec // opt-in, dev only, loudly named
		MinVersion:         tls.VersionTLS12,
		// Start empty rather than nil: a nil RootCAs means "use the system
		// roots", which is not the contract this driver implements.
		RootCAs: x509.NewCertPool(),
	}

	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, &ProtocolError{Message: fmt.Sprintf("tls CAFile %q: %v", o.CAFile, err)}
		}
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			// AppendCertsFromPEM reports only success/failure, so say which
			// file — never its contents.
			return nil, &ProtocolError{Message: fmt.Sprintf("tls CAFile %q: no PEM certificates found", o.CAFile)}
		}
	}

	if o.ClientCertFile != "" {
		cert, err := tls.LoadX509KeyPair(o.ClientCertFile, o.ClientKeyFile)
		if err != nil {
			// err names the paths (LoadX509KeyPair takes filenames), not the key bytes.
			return nil, &ProtocolError{Message: fmt.Sprintf(
				"tls client identity (cert %q, key %q): %v", o.ClientCertFile, o.ClientKeyFile, err)}
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

// Client is one session with a TriCoreDB server: a single TCP connection
// plus the HELLO/AUTH handshake already completed.
//
// A Client is not safe for concurrent use. The protocol is a strict
// request/response stream over one socket — two goroutines issuing requests
// at once would interleave frames and desync the connection. Use one Client
// per goroutine (or guard it with your own mutex if you truly need to share
// one).
type Client struct {
	conn      net.Conn
	database  string
	sessionID string

	// grantedFeatures is what HELLO negotiated for this connection (V2 P2).
	grantedFeatures uint64

	// txnOpen is true from a successful Begin until the block ends. See
	// InTransaction.
	txnOpen atomic.Bool

	rid uint64 // atomic; incremented per request for request_id.

	// requestTimeoutMs is a server-side deadline stamped on every request,
	// or 0 for none. Atomic so SetRequestTimeout is safe to call while
	// another goroutine holds the client.
	requestTimeoutMs atomic.Int64

	// lastRequestID is the request_id most recently sent, for Cancel.
	lastRequestID atomic.Value

	// inFlight is held between writing a frame and reading its reply. See
	// exchange.
	inFlight atomic.Bool

	// ridPrefix makes this connection's request_ids unique across every other
	// connection the same principal holds. See nextConnectionPrefix.
	ridPrefix string

	// readTimeout is the steady-state read deadline, in nanoseconds; 0 for
	// none. Atomic so SetReadTimeout is safe beside another goroutine.
	readTimeout atomic.Int64

	// fault is set once this connection can no longer be trusted to be
	// frame-aligned.
	//
	// Throwing alone is not enough after a refused frame: the bytes the header
	// declared are still queued behind it, and a length-prefixed stream has no
	// resynchronisation point once the length is untrustworthy. So the socket
	// is dropped and every later use fails with the same error.
	fault atomic.Pointer[connectionFault]

	// closeOnce guards against double-close doing anything user-visible
	// twice (e.g. a defer db.Close() after an explicit one).
	closeOnce sync.Once
	closeErr  error
}

// connectionFault is the recorded reason a connection was poisoned.
type connectionFault struct{ err error }

// poison records err as fatal, drops the socket, and returns err so a call site
// can read `return nil, c.poison(err)`.
func (c *Client) poison(err error) error {
	if c.fault.CompareAndSwap(nil, &connectionFault{err: err}) {
		_ = c.conn.Close()
	}
	return err
}

// Poisoned reports whether this connection was closed after a protocol
// failure, a read deadline, or a transport error. A Pool consults it directly:
// a driver that knows it is un-resynchronisable is more reliable than a list of
// error types that a new failure mode can fall out of.
func (c *Client) Poisoned() bool { return c.fault.Load() != nil }

func (c *Client) usable() error {
	if f := c.fault.Load(); f != nil {
		return &ProtocolError{Message: "this connection was closed after a protocol failure " +
			"and cannot be reused: " + f.err.Error()}
	}
	return nil
}

// A request_id prefix unique to one connection.
//
// # Why a bare counter is a correctness bug, not a cosmetic one
//
// Ids used to be go-1, go-2, ... restarting at zero *per connection*. The
// server's cancel registry is keyed by request_id scoped to the *principal*,
// and ExecutionRegistry::cancel (crates/tricore_core/src/execution/registry.rs)
// stops *every* entry that matches — so a Pool, whose connections all
// authenticate as one principal, issued concurrent go-1s and one Cancel("go-1")
// stopped all of them. The Rust client and the Node and Python drivers carried
// the same defect and were fixed the same way.
//
// Uniqueness is the requirement, not unguessability: the registry treats the id
// as guessable by construction and scopes every lookup to the principal, so
// secrecy buys nothing. A pid, a process-start nanosecond stamp and a monotonic
// counter give uniqueness across connections in a process and across processes
// on a host, with no dependency this driver does not already have.
var (
	processStamp  = strconv.FormatInt(time.Now().UnixNano(), 16)
	connectionSeq atomic.Uint64
)

func nextConnectionPrefix() string {
	return strconv.FormatInt(int64(os.Getpid()), 16) + processStamp +
		strconv.FormatUint(connectionSeq.Add(1), 16)
}

// SetReadTimeout sets a client-side deadline on each subsequent read. A
// non-positive duration clears it. See Options.ReadTimeout.
func (c *Client) SetReadTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.readTimeout.Store(int64(d))
}

// SetRequestTimeout stamps a server-side deadline on every subsequent request.
// A non-positive duration clears it.
//
// This is not a client-side timeout: cancelling on this side abandons the read
// while the server keeps working. This makes the server stop.
func (c *Client) SetRequestTimeout(d time.Duration) {
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	c.requestTimeoutMs.Store(ms)
}

// LastRequestID is the request_id most recently sent by this client. Pass it to
// Cancel from a *second* connection to stop a running statement.
func (c *Client) LastRequestID() string {
	if v, ok := c.lastRequestID.Load().(string); ok {
		return v
	}
	return ""
}

// Connect dials the server, performs HELLO, and — when Options.User is set —
// AUTH. ctx bounds the whole handshake (dial + HELLO + AUTH); it does not
// linger over the lifetime of the returned Client.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}
	database := opts.Database
	if database == "" {
		database = "main"
	}
	clientName := opts.ClientName
	if clientName == "" {
		clientName = "tricoredb-go"
	}

	var dialer net.Dialer
	if opts.DialTimeout > 0 {
		dialer.Timeout = opts.DialTimeout
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("tricoredb: dial %s:%d: %w", host, port, err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		// Cache/SQL round trips are latency-sensitive request/response
		// pairs, not bulk transfer — Nagle's algorithm would otherwise
		// add tens of milliseconds of pointless delay to every one.
		_ = tc.SetNoDelay(true)
	}

	// The deadline must cover the TLS handshake too, so set it before.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	// Handshake before any frame is written: HELLO and the AUTH secret must
	// travel inside the TLS session, not ahead of it.
	if opts.TLS != nil {
		cfg, err := opts.TLS.config()
		if err != nil {
			conn.Close()
			return nil, err
		}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, &ProtocolError{Message: fmt.Sprintf(
				"tls handshake with %q: %v", cfg.ServerName, err)}
		}
		conn = tlsConn
	}

	features := Features
	if opts.Features != nil {
		features = *opts.Features
	}

	c := &Client{conn: conn, database: database, ridPrefix: nextConnectionPrefix()}
	if err := c.hello(clientName, features); err != nil {
		conn.Close()
		return nil, err
	}
	if opts.User != "" {
		if err := c.auth(opts.User, opts.Secret); err != nil {
			conn.Close()
			return nil, err
		}
	}
	// The handshake deadline (if any) must not leak onto every later
	// call the caller makes without its own context.
	_ = conn.SetDeadline(time.Time{})
	// Armed only now, so the connect budget and the steady-state one stay two
	// different deadlines.
	c.SetReadTimeout(opts.ReadTimeout)

	return c, nil
}

// Close ends the session politely (CLOSE/BYE) and releases the socket.
//
// A failure during the goodbye handshake is not surfaced — the socket is
// closed either way, mirroring the reference Python driver's stance that a
// teardown error isn't worth failing over — but Close is idempotent and
// safe to call more than once (e.g. from both a defer and an explicit call).
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		// The server rolls an abandoned block back when the socket goes, so the
		// flag must not outlive the connection: a stale `true` would make a
		// pool retire a connection it has already dropped.
		c.txnOpen.Store(false)
		if c.Poisoned() {
			// Already dropped by poison; nothing to say goodbye on.
			c.closeErr = c.conn.Close()
			return
		}
		// Bounded: the goodbye is optional, the teardown is not. Without a
		// deadline a peer that accepts CLOSE and answers nothing holds Close
		// open for ever — and Close is what a `defer` and the pool's retire
		// path both call, so the hang lands in the application's cleanup.
		_ = c.conn.SetDeadline(time.Now().Add(closeTimeout))
		_ = writeFrame(c.conn, tagClose, nil)
		_, _ = readFrame(c.conn) // best-effort BYE; errors ignored during teardown.
		c.closeErr = c.conn.Close()
	})
	return c.closeErr
}

// closeTimeout is how long Close waits for a BYE before dropping the socket.
const closeTimeout = 2 * time.Second

// Ping checks liveness with a PING/PONG round trip.
//
// Routed through exchange like every other frame pair: it used to call
// writeFrame/readFrame directly, so a Ping from a second goroutine bypassed the
// in-flight guard this driver enforces everywhere else and consumed the first
// goroutine's reply. It bypassed ReadTimeout with it.
func (c *Client) Ping() error {
	f, err := c.exchange(tagPing, nil)
	if err != nil {
		return err
	}
	if f.tag != tagPong {
		return &ProtocolError{Message: fmt.Sprintf("expected PONG, got tag %d", f.tag)}
	}
	return nil
}

// -- handshake ---------------------------------------------------------

type helloBody struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
	// ServerVersion is what the peer says it speaks. Reported even on a
	// refusal, so a client learns what it is talking to and not merely that it
	// failed.
	ServerVersion any `json:"server_version"`
	// Features the server granted (V2 P2). A server too old to negotiate omits
	// the field, which unmarshals to 0 — so an old server and one that granted
	// nothing are indistinguishable here, which is correct: in both cases the
	// client may use no optional capability.
	Features uint64 `json:"features"`
	// Code is a stable refusal code. Branch on this, never on Message.
	Code string `json:"code"`
}

func (c *Client) hello(clientName string, features uint64) error {
	payload := map[string]any{
		"protocol": protocolName,
		"version":  map[string]int{"major": protocolVersion, "minor": 0},
		"client":   clientName,
		"features": features,
	}
	if err := writeFrame(c.conn, tagHello, payload); err != nil {
		return err
	}
	f, err := readFrame(c.conn)
	if err != nil {
		return err
	}
	if f.tag == tagError {
		return &ProtocolError{Message: errorText(f.body)}
	}
	if f.tag != tagHelloOK {
		return &ProtocolError{Message: fmt.Sprintf("expected HELLO_OK, got tag %d", f.tag)}
	}
	var body helloBody
	if err := json.Unmarshal(f.body, &body); err != nil {
		return &ProtocolError{Message: "malformed HELLO_OK body: " + err.Error()}
	}
	if !body.Ok {
		msg := body.Message
		if msg == "" {
			msg = "handshake refused"
		}
		return &ProtocolError{Message: msg}
	}
	c.grantedFeatures = body.Features
	return nil
}

// GrantedFeatures reports the optional protocol capabilities this connection
// negotiated at HELLO (V2 P2).
//
// Zero before a handshake, and zero against a server too old to negotiate.
func (c *Client) GrantedFeatures() uint64 { return c.grantedFeatures }

// SessionTxnGranted reports whether the server granted [FeatureSessionTxn] on
// this connection — the bit [Client.Begin] requires.
func (c *Client) SessionTxnGranted() bool {
	return c.grantedFeatures&FeatureSessionTxn != 0
}

// ServerParamsGranted reports whether the server granted [FeatureServerParams]
// on this connection — the bit [Client.ExecuteParams] and [Client.QueryParams]
// require.
//
// False against a server too old to negotiate. Those two methods refuse by name
// in that case rather than rendering the values into the statement text, so a
// caller never binds server-side on one connection and client-side on the next
// without being told.
func (c *Client) ServerParamsGranted() bool {
	return c.grantedFeatures&FeatureServerParams != 0
}

// InTransaction reports whether a [Client.Begin] block is open on this
// connection.
//
// A closed or poisoned connection has no transaction: the server rolls one back
// the moment the socket goes, and Close clears the flag for the same reason.
func (c *Client) InTransaction() bool {
	return c.txnOpen.Load() && !c.Poisoned()
}

type authOKBody struct {
	Ok        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

func (c *Client) auth(user, secret string) error {
	payload := map[string]any{
		"username": user,
		"secret":   byteList(secret),
	}
	if err := writeFrame(c.conn, tagAuth, payload); err != nil {
		return err
	}
	f, err := readFrame(c.conn)
	if err != nil {
		return err
	}
	if f.tag == tagError {
		return &AuthError{Message: errorText(f.body)}
	}
	if f.tag != tagAuthOK {
		return &ProtocolError{Message: fmt.Sprintf("expected AUTH_OK, got tag %d", f.tag)}
	}
	var body authOKBody
	if err := json.Unmarshal(f.body, &body); err != nil {
		return &ProtocolError{Message: "malformed AUTH_OK body: " + err.Error()}
	}
	// An AUTH_OK-tagged frame with ok=false is still a refusal — the tag
	// name is not the verdict.
	if !body.Ok {
		msg := body.Message
		if msg == "" {
			msg = "authentication refused"
		}
		return &AuthError{Message: msg}
	}
	c.sessionID = body.SessionID
	return nil
}

// SessionID is the server-assigned session identifier from AUTH_OK, or ""
// if the connection never authenticated.
func (c *Client) SessionID() string { return c.sessionID }

// -- requests ------------------------------------------------------------

type requestEnvelope struct {
	RequestID string          `json:"request_id"`
	Database  string          `json:"database"`
	Op        any             `json:"op"`
	Options   *requestOptions `json:"options,omitempty"`
}

// requestOptions carries the per-request knobs the server understands. Only
// the timeout is exposed today; the field is a pointer so an unset timeout
// omits the whole object rather than sending an explicit null.
type requestOptions struct {
	TimeoutMs int64 `json:"timeout_ms"`
}

type wireResponse struct {
	RequestID   string          `json:"request_id"`
	Status      string          `json:"status"`
	Data        json.RawMessage `json:"data"`
	Diagnostics *diagnostics    `json:"diagnostics"`
}

type diagnostics struct {
	Route      string   `json:"route"`
	ElapsedMs  int64    `json:"elapsed_ms"`
	Warnings   []string `json:"warnings"`
	ErrorCode  string   `json:"error_code"`
	LeaderHint string   `json:"leader_hint"`
}

// Response is a server response: the operation's typed result plus how it
// was produced.
type Response struct {
	// RequestID is the request_id the server echoed back.
	RequestID string
	// Status is the server's status string; "ok" on success.
	Status string

	// DataKind is the externally-tagged variant name of Data — one of
	// "Json", "CacheValue", "Rows", "Documents", "Message", "Empty".
	DataKind string
	// DataRaw is that variant's payload, still JSON-encoded, for callers
	// that need a shape this driver doesn't decode into a named field.
	DataRaw json.RawMessage

	Route     string
	ElapsedMs int64

	// Warnings are non-fatal, and not decorative: a broadcast DDL that
	// couldn't reach every shard reports it here while Status is still
	// "ok". A caller that ignores this can silently run on a divergent
	// cluster.
	Warnings []string

	// ErrorCode is the server's machine-readable reason a request failed
	// (diagnostics.error_code), empty when it did not fail or the server sent
	// none. Branch on this, never on the message text.
	ErrorCode string

	// LeaderHint is the `host:port` a refused write belongs to, set only
	// alongside an ErrorCode of [ErrorCodeNotLeader]. It is an address a client
	// can dial, resolved by the server from `[[raft.peers]].address`, not a node
	// id.
	//
	// Empty when the code is something else, and empty *even for a not_leader
	// refusal* in three cases: raised mid-election, raised on a non-Raft node,
	// and raised when the leader's id has no `[[raft.peers]]` entry to resolve.
	// So a caller must treat its absence as "destination unknown, wait and
	// retry", not as "no redirect happened". See [ServerError.LeaderHint].
	LeaderHint string
}

// exchange writes one frame and reads its reply, holding the connection for the
// duration.
//
// A connection is one request/response stream, and readFrame reads a 6-byte
// header followed by exactly that many body bytes. Two overlapping exchanges —
// two goroutines sharing one *Client — both read a header: the first gets the
// header, the second gets the FIRST frame's body and mis-parses it. From there
// the stream is unrecoverable and the driver blocks forever on bytes that never
// arrive.
//
// Refusing the second caller turns that hang into an error naming the mistake
// and the fix. This is a guard, not a queue: silently serialising would make an
// unsafe pattern appear to work while giving the caller no concurrency at all —
// use a Pool for that.
func (c *Client) exchange(tag byte, payload any) (*frame, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}
	if !c.inFlight.CompareAndSwap(false, true) {
		return nil, &ArgumentError{Message: "a request is already in flight on this connection. " +
			"A tricore connection is a single request/response stream: overlapping requests " +
			"interleave frames and deadlock. Use one connection per goroutine, or a Pool."}
	}
	defer c.inFlight.Store(false)

	if err := writeFrame(c.conn, tag, payload); err != nil {
		// A refusal this driver made before writing anything (an oversized
		// control frame) leaves the stream intact; a failed write does not.
		if errors.Is(err, ErrProtocol) {
			return nil, err
		}
		return nil, c.poison(err)
	}

	budget := time.Duration(c.readTimeout.Load())
	if budget > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(budget)); err != nil {
			return nil, c.poison(err)
		}
	}
	f, err := readFrame(c.conn)
	if budget > 0 {
		_ = c.conn.SetReadDeadline(time.Time{})
	}
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			err = &TimeoutError{Message: fmt.Sprintf(
				"no reply within the connection's ReadTimeout of %s", budget)}
		}
		// Any read failure past this point leaves the stream un-resynchronised:
		// the reply may still be in flight, and reusing the socket would read it
		// as the answer to the next request.
		return nil, c.poison(err)
	}
	return &f, nil
}

// Request sends a raw, externally-tagged operation and returns the decoded
// response — the escape hatch every other TriCoreDB driver exposes, and which
// this one was missing.
//
// The typed helpers cover every operation a V1 client should need, so reach for
// this only when there is genuinely no typed method: a server operation that
// exists solely to be refused (`Cache::XGroup`), or one added to the server
// ahead of this driver. `op` is the wire form, e.g.
//
//	db.Request(map[string]any{"Cache": map[string]any{"Ping": nil}})
//
// Nothing here validates the shape, so a malformed op is refused by the server
// rather than by the driver. That is the trade: full reach, no type safety.
func (c *Client) Request(op any) (*Response, error) {
	return c.request(op)
}

// request sends a raw operation and returns the decoded response. The typed
// helpers (Execute, Query, CacheSet, ...) all funnel through this.
func (c *Client) request(op any) (*Response, error) {
	rid := atomic.AddUint64(&c.rid, 1)
	// See nextConnectionPrefix: the prefix is what keeps a CANCEL naming one
	// statement rather than every connection's Nth statement.
	requestID := fmt.Sprintf("go-%s-%d", c.ridPrefix, rid)
	c.lastRequestID.Store(requestID)
	env := requestEnvelope{
		RequestID: requestID,
		Database:  c.database,
		Op:        op,
	}
	// A server-side deadline, distinct from any client-side deadline: this one
	// makes the *server* stop, rather than abandoning the read on this side
	// while the work carries on.
	if ms := c.requestTimeoutMs.Load(); ms > 0 {
		env.Options = &requestOptions{TimeoutMs: ms}
	}
	f, err := c.exchange(tagRequest, env)
	if err != nil {
		return nil, err
	}
	if f.tag == tagError {
		return nil, &ServerError{Message: errorText(f.body), Status: "error"}
	}
	if f.tag != tagResponse {
		return nil, &ProtocolError{Message: fmt.Sprintf("expected RESPONSE, got tag %d", f.tag)}
	}

	var wr wireResponse
	if err := json.Unmarshal(f.body, &wr); err != nil {
		return nil, &ProtocolError{Message: "malformed RESPONSE body: " + err.Error()}
	}

	kind, raw, err := decodeVariant(wr.Data)
	if err != nil {
		return nil, &ProtocolError{Message: "malformed response data: " + err.Error()}
	}

	resp := &Response{
		RequestID: wr.RequestID,
		Status:    wr.Status,
		DataKind:  kind,
		DataRaw:   raw,
	}
	if wr.Diagnostics != nil {
		resp.Route = wr.Diagnostics.Route
		resp.ElapsedMs = wr.Diagnostics.ElapsedMs
		resp.Warnings = wr.Diagnostics.Warnings
		resp.ErrorCode = wr.Diagnostics.ErrorCode
		resp.LeaderHint = wr.Diagnostics.LeaderHint
	}

	// Anything that is not `ok` means the server did not complete the operation
	// and must not reach a caller wearing a success's clothes. Compared against
	// `ok` rather than a list of bad statuses, so a status added to the kernel
	// later fails closed — enumerating "error" and "not_implemented" was correct
	// for today's kernel and open by construction for tomorrow's.
	if wr.Status != "ok" {
		return resp, &ServerError{
			Message:    messageOf(kind, raw, wr.Status),
			Status:     wr.Status,
			Code:       resp.ErrorCode,
			LeaderHint: resp.LeaderHint,
		}
	}
	return resp, nil
}

// decodeVariant unwraps an externally-tagged `data` field — either
// `{"Variant": payload}` or the bare string `"Empty"` — into its variant
// name and raw payload.
func decodeVariant(b json.RawMessage) (kind string, raw json.RawMessage, err error) {
	if len(b) == 0 || string(b) == "null" {
		return "", nil, nil
	}
	// The "Empty" variant is a bare JSON string, not an object.
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return s, nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return "", nil, err
	}
	for k, v := range m {
		return k, v, nil // externally tagged: exactly one key.
	}
	return "", nil, nil
}

func messageOf(kind string, raw json.RawMessage, status string) string {
	switch kind {
	case "Message":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case "Json":
		return string(raw)
	}
	if status != "" {
		return status
	}
	return "request failed"
}

func errorText(body []byte) string {
	if len(body) == 0 {
		return "unknown error"
	}
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if msg, ok := m["message"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := m["error"].(string); ok && msg != "" {
			return msg
		}
	}
	return string(body)
}

// -- SQL -------------------------------------------------------------------

// Execute runs a write statement (CREATE/INSERT/UPDATE/DELETE/BEGIN...COMMIT
// and the like). The server refuses a write submitted through Query instead
// of Execute.
func (c *Client) Execute(sql string) (*Response, error) {
	return c.request(map[string]any{
		"Sql": map[string]any{"Exec": map[string]any{"sql": sql}},
	})
}

// Rows is a SQL result set: column names plus each row's values, both as the
// strings the server returns them as.
type Rows struct {
	Columns []string
	Rows    [][]string
}

// Dicts returns each row as a map keyed by column name, for callers that
// want name-based access instead of positional.
func (r *Rows) Dicts() []map[string]string {
	out := make([]map[string]string, len(r.Rows))
	for i, row := range r.Rows {
		m := make(map[string]string, len(r.Columns))
		for j, col := range r.Columns {
			if j < len(row) {
				m[col] = row[j]
			}
		}
		out[i] = m
	}
	return out
}

type rowsPayload struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// Query runs a read and returns its rows. Submitting a write here is refused
// by the server rather than executed — use Execute for those.
func (c *Client) Query(sql string) (*Rows, error) {
	resp, err := c.request(map[string]any{
		"Sql": map[string]any{"Query": map[string]any{"sql": sql}},
	})
	if err != nil {
		return nil, err
	}
	return rowsOf(resp)
}

// rowsOf decodes a Rows payload. Shared by Query and QueryParams so the two
// cannot drift in what they accept.
func rowsOf(resp *Response) (*Rows, error) {
	if resp.DataKind != "Rows" {
		return nil, &ProtocolError{Message: fmt.Sprintf("expected Rows, got %s", kindOrRaw(resp))}
	}
	var rp rowsPayload
	if err := json.Unmarshal(resp.DataRaw, &rp); err != nil {
		return nil, &ProtocolError{Message: "malformed Rows payload: " + err.Error()}
	}
	return &Rows{Columns: rp.Columns, Rows: rp.Rows}, nil
}

func kindOrRaw(resp *Response) string {
	if resp.DataKind != "" {
		return resp.DataKind
	}
	return "no data"
}

// -- cache -------------------------------------------------------------------

// CacheSet stores value under (namespace, key) with no expiry. Use
// CacheSetTTL to attach one.
func (c *Client) CacheSet(namespace, key string, value []byte) error {
	return c.CacheSetTTL(namespace, key, value, 0)
}

// CacheSetTTL stores value under (namespace, key), expiring after ttlMs
// milliseconds. ttlMs <= 0 means no expiry.
func (c *Client) CacheSetTTL(namespace, key string, value []byte, ttlMs int64) error {
	set := map[string]any{
		"namespace": namespace,
		"key":       key,
		"value":     byteList(value),
	}
	if ttlMs > 0 {
		set["ttl_ms"] = ttlMs
	} else {
		set["ttl_ms"] = nil
	}
	_, err := c.request(map[string]any{"Cache": map[string]any{"Set": set}})
	return err
}

// CacheGet fetches the value stored under (namespace, key). found is false
// on a miss, which is how a miss is told apart from a stored empty value —
// both otherwise look like a zero-length (or nil-ish) byte slice.
func (c *Client) CacheGet(namespace, key string) (value []byte, found bool, err error) {
	resp, err := c.request(map[string]any{
		"Cache": map[string]any{"Get": map[string]any{"namespace": namespace, "key": key}},
	})
	if err != nil {
		return nil, false, err
	}
	if resp.DataKind != "CacheValue" {
		return nil, false, &ProtocolError{Message: fmt.Sprintf("expected CacheValue, got %s", kindOrRaw(resp))}
	}
	if len(resp.DataRaw) == 0 || string(resp.DataRaw) == "null" {
		return nil, false, nil // miss
	}
	v, err := decodeByteList(resp.DataRaw)
	if err != nil {
		return nil, false, &ProtocolError{Message: "malformed CacheValue payload: " + err.Error()}
	}
	return v, true, nil
}

// CacheDelete removes (namespace, key), reporting whether it was there.
//
// Deleting an absent key is not an error — existed is simply false. The flag is
// returned rather than discarded so this driver answers the same question as
// the other TriCoreDB SDKs, all of which report it: "delete succeeded" and "the
// key was there" are different facts, and only one of them tells you whether
// somebody else got there first.
func (c *Client) CacheDelete(namespace, key string) (existed bool, err error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err = c.cacheJSON(cacheVariant("Delete", nsKey(namespace, key)), "Delete", &out)
	return out.Deleted, err
}

// Cancel asks the server to stop one of this principal's running statements,
// named by the request_id it was sent with (see LastRequestID).
//
// This must be sent on a second connection. The connection running the
// statement is blocked reading its reply and is not reading anything else, so a
// cancel can never reach it there.
//
// Scoped by the authenticated principal: you cannot stop — or learn about —
// somebody else's statement. An unknown id returns 0 rather than an error.
func (c *Client) Cancel(requestID string) (int, error) {
	if requestID == "" {
		return 0, &ArgumentError{Message: "requestID must not be empty"}
	}
	f, err := c.exchange(tagCancel, map[string]any{"request_id": requestID})
	if err != nil {
		return 0, err
	}
	if f.tag == tagError {
		return 0, &ServerError{Message: errorText(f.body), Status: "error"}
	}
	if f.tag != tagCancelOK {
		return 0, &ProtocolError{Message: fmt.Sprintf("expected CANCEL_OK, got tag %d", f.tag)}
	}
	var out struct {
		Cancelled int `json:"cancelled"`
	}
	if err := json.Unmarshal(f.body, &out); err != nil {
		return 0, &ProtocolError{Message: "malformed CANCEL_OK body: " + err.Error()}
	}
	return out.Cancelled, nil
}
