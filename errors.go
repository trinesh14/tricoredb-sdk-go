package tricoredb

import "errors"

// Sentinel errors for use with errors.Is. Each concrete error type below
// (AuthError, ServerError, ProtocolError) unwraps to one of these, so callers
// can write `errors.Is(err, tricoredb.ErrAuth)` without caring about the
// concrete type or its message text.
var (
	// ErrAuth means the server rejected the AUTH frame, or answered
	// AUTH_OK with ok=false — the wire spec calls that out explicitly as
	// still a refusal despite the "OK" tag name.
	ErrAuth = errors.New("tricoredb: authentication refused")

	// ErrServer means a REQUEST came back with status "error" (or
	// "not_implemented") — the round trip worked, the operation didn't.
	ErrServer = errors.New("tricoredb: server error")

	// ErrProtocol means the peer didn't speak the tricore wire protocol
	// as documented (wrong tag, malformed frame, connection closed
	// mid-frame, unparseable JSON body).
	ErrProtocol = errors.New("tricoredb: protocol error")

	// ErrArgument means the caller passed something this driver refused
	// before any frame was written — an empty member list, a value with no
	// SQL literal form, a mismatched placeholder count.
	//
	// Kept distinct from ErrServer on purpose: a caller retrying on a
	// server error is doing something reasonable, and retrying on a bad
	// argument is an infinite loop.
	ErrArgument = errors.New("tricoredb: invalid argument")

	// ErrTimeout means a reply did not arrive within the connection's
	// ReadTimeout, or the handshake did not finish within the connect budget.
	//
	// Fatal to the connection rather than a retryable hiccup: the reply this
	// driver stopped waiting for may still be in flight, and reusing the socket
	// would read it as the answer to the *next* request. A timed-out connection
	// is poisoned, and a Pool retires it.
	ErrTimeout = errors.New("tricoredb: timed out")
)

// ArgumentError reports a request this driver refused to send.
type ArgumentError struct {
	Message string
}

func (e *ArgumentError) Error() string { return "tricoredb: invalid argument: " + e.Message }

func (e *ArgumentError) Unwrap() error { return ErrArgument }

// AuthError reports an authentication failure. Message is the server's
// explanation, if it supplied one.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string { return "tricoredb: auth failed: " + e.Message }

// Unwrap lets errors.Is(err, ErrAuth) succeed without exposing *AuthError.
func (e *AuthError) Unwrap() error { return ErrAuth }

// ServerError reports a REQUEST that the server processed but did not
// complete: any RESPONSE whose status is not "ok".
//
// The kernel has three statuses — "ok", "error" and "not_implemented", the last
// meaning "a recognized hook that was NOT run"
// (crates/tricore_core/src/response/mod.rs). This driver compares against "ok"
// rather than enumerating the bad ones, so a status added to the kernel later
// fails closed instead of being handed to a caller as a success.
type ServerError struct {
	Message string
	// Status is the raw status string from the RESPONSE frame; kept for
	// callers that need to distinguish the cases rather than treat them
	// identically.
	Status string

	// Code is the server's machine-readable reason
	// (`diagnostics.error_code`), or "" when it sent none.
	//
	// This is the field to branch on. The message is prose and stays free to be
	// reworded; substring-matching it is how an authorization denial once
	// reached clients as something else entirely, which is the failure the code
	// was added to end.
	Code string

	// LeaderHint is a `host:port` this request should have gone to, carried
	// only with a Code of [ErrorCodeNotLeader].
	//
	// It is an **address a client can dial**, not a node id: the server resolves
	// the leader's id through `[[raft.peers]].address`, which names that node's
	// native protocol listener — the same endpoint this driver already speaks,
	// not a separate consensus port.
	//
	// Empty means "no address was named", which is NOT the same as "this is not
	// a redirect". Three situations produce a code with no hint: a refusal
	// raised mid-election, a node running without Raft, and a leader whose id
	// has no `[[raft.peers]]` entry — where the server deliberately sends
	// nothing rather than a bare id, because a label in an address field is a
	// connection attempt to a host that does not exist. All three mean the same
	// thing to a caller: wait and retry.
	//
	// The refusal's Message still names the leader's **node id**, on purpose:
	// prose is for a human reading logs and the hint is for a machine dialling.
	// Never scrape the id out of the message and use it as a destination — that
	// is exactly what this field replaces.
	//
	// Test [ServerError.Code] for the redirect and treat an empty hint as an
	// unknown destination.
	LeaderHint string
}

func (e *ServerError) Error() string {
	// The code is appended rather than replacing the server's prose: the prose
	// is what an operator reads, and the code is what a program branches on.
	if e.Code == "" {
		return "tricoredb: server error: " + e.Message
	}
	if e.LeaderHint == "" {
		return "tricoredb: server error [" + e.Code + "]: " + e.Message
	}
	return "tricoredb: server error [" + e.Code + ", leader " + e.LeaderHint + "]: " + e.Message
}

func (e *ServerError) Unwrap() error { return ErrServer }

// IsNotLeader reports whether this refusal is a leader redirect: the request was
// valid, and this node is not the one that may serve it.
//
// A true result says nothing about whether [ServerError.LeaderHint] is set —
// see that field. This driver deliberately does not follow the redirect itself:
// re-sending a write to another address is a policy decision (which endpoints
// are reachable, which credentials apply there, whether the operation is safe to
// repeat) that belongs to the caller, not to a connection object.
func (e *ServerError) IsNotLeader() bool { return e.Code == ErrorCodeNotLeader }

// ErrorCodeNotLeader is the server's `diagnostics.error_code` for a write or
// linearizable read that reached a Raft follower.
const ErrorCodeNotLeader = "not_leader"

// ProtocolError reports a violation of the wire protocol itself: an
// unexpected tag, a truncated frame, or a body that doesn't parse as the
// JSON shape the spec promises.
type ProtocolError struct {
	Message string
}

func (e *ProtocolError) Error() string { return "tricoredb: protocol error: " + e.Message }

func (e *ProtocolError) Unwrap() error { return ErrProtocol }

// TimeoutError reports that a read (or the connect phase) exceeded its budget.
//
// A distinct type rather than the bare net.Error the standard library returns,
// so `errors.Is(err, tricoredb.ErrTimeout)` works and so a timeout cannot
// escape this driver's error vocabulary the way a raw os.ErrDeadlineExceeded
// would.
type TimeoutError struct {
	Message string
}

func (e *TimeoutError) Error() string { return "tricoredb: timed out: " + e.Message }

func (e *TimeoutError) Unwrap() error { return ErrTimeout }

// Timeout and Temporary together satisfy net.Error, so code already branching on
// that interface keeps working — before this type existed a read deadline
// surfaced as a raw *net.OpError, which is a net.Error, and silently losing that
// would break callers that were handling it correctly.
//
// Temporary is false: this driver poisons a timed-out connection, because the
// reply it stopped waiting for may still be in flight and reusing the socket
// would read it as the answer to the next request. Retrying on the same
// connection is exactly what must not happen. (net.Error.Temporary is deprecated
// but remains part of the interface, so it has to be implemented to satisfy it.)
func (e *TimeoutError) Timeout() bool { return true }

// Temporary reports false. See Timeout.
func (e *TimeoutError) Temporary() bool { return false }
