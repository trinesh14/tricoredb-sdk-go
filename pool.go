package tricoredb

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrPoolClosed is returned by a Pool whose Close has already run.
var ErrPoolClosed = errors.New("tricoredb: pool is closed")

// ErrPoolTimeout is returned when no pooled connection became available before
// the caller's context expired.
var ErrPoolTimeout = errors.New("tricoredb: no pooled connection available")

// Pool is a bounded, concurrency-safe pool of connections.
//
// # Why this exists
//
// A [Client] is a single request/response stream: two goroutines sharing one
// would interleave frames and read each other's replies. So concurrency needs
// one connection per concurrent caller — and the server scales ~3.3x across 8
// concurrent connections (see benchmarks/concurrency/RESULTS.md), which is only
// reachable if an application actually opens several. Doing that per request
// costs a TCP connect plus HELLO and AUTH round trips every time (~14.5ms
// measured, against ~127µs pooled).
//
// # Exclusive ownership
//
// [Pool.Use] runs a callback with a connection that is exclusively the
// callback's for its duration. That is deliberately a callback rather than an
// Acquire/Release pair returning a *Client: a returned handle is something a
// caller can retain and share across goroutines, which is exactly the
// corruption this type exists to prevent. (Release-on-panic is also handled,
// which a manual pair invites callers to get wrong.)
//
//	pool, err := tricoredb.NewPool(tricoredb.Options{Host: "127.0.0.1", Port: 8427,
//		User: "admin", Secret: "pw"}, 8)
//	err = pool.Use(ctx, func(c *tricoredb.Client) error {
//		_, err := c.Execute("INSERT INTO t VALUES (1, 'ada')")
//		return err
//	})
//	pool.Close()
type Pool struct {
	opts Options
	size int

	// A token must be held to own a connection, so the pool cannot exceed
	// `size`. A buffered channel is Go's natural bounded semaphore, and it makes
	// "wait for a free slot, but respect the caller's context" a plain select.
	tokens chan struct{}

	mu     sync.Mutex
	idle   []*Client
	closed bool
}

// NewPool builds a pool of at most `size` connections. Connections are created
// lazily: a pool sized for peak load should not pay for peak load at startup.
func NewPool(opts Options, size int) (*Pool, error) {
	if size < 1 {
		return nil, fmt.Errorf("tricoredb: pool size must be >= 1, got %d", size)
	}
	p := &Pool{opts: opts, size: size, tokens: make(chan struct{}, size)}
	for i := 0; i < size; i++ {
		p.tokens <- struct{}{}
	}
	return p, nil
}

// Size is the maximum number of connections the pool may hold.
func (p *Pool) Size() int { return p.size }

// Stats reports (idle, inUse) at this instant. For tests and diagnostics.
//
// A checked-out connection holds a token; an idle one does not. So the number
// in use is exactly the number of tokens taken — idle must NOT be subtracted
// again, or the count goes negative the moment a connection is returned.
func (p *Pool) Stats() (idle, inUse int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle), p.size - len(p.tokens)
}

// Use borrows a connection for the duration of fn.
//
// It blocks until one is free or ctx is done, returning ErrPoolTimeout in the
// latter case rather than growing past `size` — an unbounded pool does not fix
// overload, it relocates it to the server.
//
// A connection is retired rather than returned when fn reports a transport or
// protocol failure, because the stream may be mid-frame and reusing it would
// hand the next caller someone else's bytes.
//
// A session transaction ([Client.Begin]) is bound to the connection, so one must
// never go back to the pool mid-block: the next borrower would be writing into
// somebody else's transaction. If fn returns with a block still open it is
// rolled back and Use returns an error naming it; if fn returns an error with
// one open it is rolled back and fn's error is what propagates; and a connection
// whose rollback fails is retired rather than reused. Use
// [Client.WithTransaction] inside the callback and none of this arises.
func (p *Pool) Use(ctx context.Context, fn func(*Client) error) error {
	// Take a slot first: holding a token is what bounds the pool.
	select {
	case <-p.tokens:
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrPoolTimeout, ctx.Err())
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.tokens <- struct{}{}
		return ErrPoolClosed
	}
	var c *Client
	if n := len(p.idle); n > 0 {
		c, p.idle = p.idle[n-1], p.idle[:n-1]
	}
	p.mu.Unlock()

	if c == nil {
		var err error
		c, err = Connect(ctx, p.opts)
		if err != nil {
			p.tokens <- struct{}{} // give the slot back or the pool shrinks
			return err
		}
	}

	// The connection must go back even if fn panics, or the pool leaks a slot
	// and eventually deadlocks every caller.
	broken := false
	defer func() {
		if r := recover(); r != nil {
			p.put(c, true)
			panic(r)
		}
		p.put(c, broken)
	}()

	if err := fn(c); err != nil {
		broken = isConnectionBroken(err) || c.Poisoned()
		if !broken && c.InTransaction() {
			// The caller's error is the one to propagate; the block still has to
			// go, and the connection is retired if it will not.
			broken = !abandonBlock(c)
		}
		return err
	}
	if c.InTransaction() {
		// Rolled back here rather than returned to the pool mid-block.
		broken = !abandonBlock(c)
		return &ArgumentError{Message: "the callback returned with a session transaction still " +
			"open on the pooled connection; it has been rolled back rather than returned to the " +
			"pool mid-block. Commit or Rollback inside the callback, or use " +
			"Client.WithTransaction."}
	}
	return nil
}

// abandonBlock rolls back a block a pooled callback left open. False when the
// connection must be retired instead — a rollback this driver could not complete
// leaves a session whose state no next borrower can assume anything about.
func abandonBlock(c *Client) bool {
	if _, err := c.Rollback(); err != nil {
		return false
	}
	return true
}

// put returns a connection to the pool, or retires it.
func (p *Pool) put(c *Client, broken bool) {
	// Never idle a connection mid-block, whatever path led here.
	if c.InTransaction() {
		broken = true
	}
	p.mu.Lock()
	if broken || p.closed {
		p.mu.Unlock()
		_ = c.Close()
		p.tokens <- struct{}{}
		return
	}
	p.idle = append(p.idle, c)
	p.mu.Unlock()
	p.tokens <- struct{}{}
}

// isConnectionBroken reports whether an error means the stream can no longer be
// trusted to be frame-aligned. A server-side error (bad SQL, say) is a perfectly
// good response on a healthy connection and must NOT retire it — doing so would
// churn the pool on ordinary application errors.
// The connection's own Poisoned() is consulted alongside this at the call site,
// which is the part that cannot fall out of date: a driver that refused a frame,
// timed out, or failed a write knows it is un-resynchronisable whether or not
// the error happens to unwrap to one of the sentinels named here. Before that,
// a bare *net.OpError from a reset socket unwrapped to neither ErrProtocol nor
// anything else listed, so a dead connection went back into the pool.
func isConnectionBroken(err error) bool {
	return errors.Is(err, ErrProtocol) || errors.Is(err, ErrTimeout)
}

// Close closes every idle connection. Connections currently in use are closed
// when their Use returns.
func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	var firstErr error
	for _, c := range idle {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
