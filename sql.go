package tricoredb

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// SQL conveniences layered on Execute/Query: parameter binding and
// transactions.

// Arg is one bound value. It exists so ExecuteParams/QueryParams can take a
// variadic list without shadowing the plain Execute/Query signatures.
type Arg = any

// ExecuteParams runs a write, binding ? placeholders from args.
//
// # The values are bound by the server, not rendered into the statement
//
// args travel beside the SQL as a typed array and the server substitutes them
// at value positions its grammar has already fixed. A value therefore cannot
// become syntax however it is spelled: a quote, a backslash, or an argument
// that is itself a complete SQL statement is stored as the text it is.
//
// This requires [FeatureServerParams], negotiated at HELLO. When the server did
// not grant it — one too old to negotiate — this fails by name with
// [ErrArgument] *before writing anything*, rather than falling back to
// rendering the values into the statement text. The fallback is what the driver
// used to do unconditionally, and doing it silently would mean the same call
// binds on one connection and escapes on the next with nothing to tell them
// apart. A caller that genuinely wants the old behaviour asks for it by name
// with [BindParams].
//
// Zero args need no capability: with nothing to bind this is exactly
// [Client.Execute].
func (c *Client) ExecuteParams(sql string, args ...Arg) (*Response, error) {
	body, err := c.sqlBody(sql, args)
	if err != nil {
		return nil, err
	}
	return c.request(map[string]any{"Sql": map[string]any{"Exec": body}})
}

// QueryParams runs a read, binding ? placeholders from args server-side.
// See [Client.ExecuteParams] for what that means and what it requires.
func (c *Client) QueryParams(sql string, args ...Arg) (*Rows, error) {
	body, err := c.sqlBody(sql, args)
	if err != nil {
		return nil, err
	}
	resp, err := c.request(map[string]any{"Sql": map[string]any{"Query": body}})
	if err != nil {
		return nil, err
	}
	return rowsOf(resp)
}

// sqlBody builds the Query/Exec body, binding args server-side.
//
// Placeholder arity is deliberately left to the server: it counts `?` against
// the parameters it was given and refuses a mismatch by name, and it also
// understands `$n` placeholders, which a client-side scanner counting `?` would
// mis-report. One authority on what a statement's placeholders are is the point.
func (c *Client) sqlBody(sql string, args []Arg) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{"sql": sql}, nil
	}
	if !c.ServerParamsGranted() {
		return nil, &ArgumentError{Message: errServerParamsNotGranted}
	}
	params := make([]any, len(args))
	for i, a := range args {
		v, err := SQLParam(a)
		if err != nil {
			return nil, err
		}
		params[i] = v
	}
	return map[string]any{"sql": sql, "params": params}, nil
}

// The refusal text for an ungranted [FeatureServerParams], shared by every
// entry point that binds, so one wording is searchable.
const errServerParamsNotGranted = "this server did not grant server-side parameters " +
	"(SERVER_PARAMS is not in the granted feature set), so this driver will not bind `?` " +
	"placeholders on this connection; it will not silently render the values into the " +
	"statement text instead, because escaping and binding are not the same guarantee. " +
	"Upgrade the server, or call BindParams(sql, args...) explicitly to accept client-side " +
	"rendering"

// SQLParam renders one Go value as a server-side parameter.
//
// The wire carries plain JSON, because five SDKs in five languages build this
// payload; the server maps each JSON scalar to a typed SQL value and refuses,
// by name and by index, anything it cannot represent. So this function's job is
// only the types encoding/json would otherwise get wrong or refuse:
//
//   - []byte becomes 0x-prefixed hex, which is what a BLOB column parses.
//     encoding/json would have written base64 — a different string, silently
//     stored as text. (The old client-side path was worse still: it put the
//     bytes through string() into a quoted literal, which mangles any value
//     that is not valid UTF-8 and cannot survive a BLOB round trip at all.)
//   - time.Time becomes the sortable UTC text a TIMESTAMP column accepts,
//     unchanged from what this driver has always sent.
//   - NaN and ±Inf are refused here rather than failing inside encoding/json
//     with a message about the frame instead of about the parameter.
//   - An exact decimal ([Decimal], *big.Rat, *big.Float) becomes a JSON string
//     of plain digits with no exponent, which a DECIMAL column parses exactly.
//     A JSON number would be held as a double, and an exponent reads as DOUBLE.
//     A *big.Rat with no finite decimal expansion (1/3) is refused.
//
// Everything else passes through: integers are exact in JSON at any width Go
// has, and a JSON string is a TEXT parameter whatever it contains.
func SQLParam(value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return hexBytes(v), nil
	case Decimal, *big.Rat, *big.Float:
		d, isNull, err := decimalText(v)
		if err != nil || isNull {
			return nil, err
		}
		return d, nil
	case float32:
		return realParam(float64(v))
	case float64:
		return realParam(v)
	case time.Time:
		return v.UTC().Format("2006-01-02 15:04:05.000"), nil
	case bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		return v, nil
	default:
		return nil, &ArgumentError{Message: fmt.Sprintf(
			"no SQL parameter form for %T. Convert it explicitly — passing it through would "+
				"put a JSON object or array where the server expects a scalar.", value)}
	}
}

func realParam(f float64) (any, error) {
	// JSON has no spelling for these, so encoding/json would refuse the whole
	// frame. Naming the parameter here is the difference between a diagnosable
	// error and one about a frame the caller never built by hand.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, &ArgumentError{Message: fmt.Sprintf("`%v` has no SQL parameter form", f)}
	}
	return f, nil
}

// Statement is one statement of a transaction script, with optional bound
// arguments.
type Statement struct {
	// SQL is the statement text, with ? placeholders.
	SQL string
	// Args are the values bound to SQL's placeholders, in order.
	Args []Arg
}

// Stmt is shorthand for building a Statement.
func Stmt(sql string, args ...Arg) Statement { return Statement{SQL: sql, Args: args} }

// TransactionResult is what the server did with a transaction script.
type TransactionResult struct {
	// Statements is the number of statements the server ran.
	Statements int64 `json:"statements"`
	// CommittedWrites is the number of writes made durable.
	CommittedWrites int64 `json:"committed_writes"`
	// DiscardedWrites is the number of buffered writes thrown away.
	DiscardedWrites int64 `json:"discarded_writes"`
	// Outcome is "began", "committed" or "rolled_back".
	Outcome string `json:"transaction"`
}

// Transaction runs a pre-declared script atomically, in one request.
//
// The server runs the BEGIN ... COMMIT script against an MVCC snapshot and
// flushes it as one atomic batch; ROLLBACK, or any error mid-script, discards
// the buffer. This is the right tool when every statement is known up front: one
// round trip, one replication event, and it works on every node — including the
// sharded and forwarding ones that withhold [FeatureSessionTxn].
//
//	db.Transaction(
//		tricoredb.Stmt("INSERT INTO t VALUES (?, ?)", 1, "ada"),
//		tricoredb.Stmt("INSERT INTO t VALUES (?, ?)", 2, "bob"),
//	)
//
// When a later statement depends on what an earlier one read, use
// [Client.Begin]/[Client.Commit]/[Client.Rollback] or [Client.WithTransaction]
// instead.
func (c *Client) Transaction(statements ...Statement) (*TransactionResult, error) {
	script, args, err := buildTransactionRequest(statements)
	if err != nil {
		return nil, err
	}
	resp, err := c.ExecuteParams(script, args...)
	if err != nil {
		return nil, err
	}
	var out TransactionResult
	if err := decodeJSON(resp, "transaction", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Begin opens a session transaction on this connection.
//
// Every statement sent on this connection until [Client.Commit] or
// [Client.Rollback] runs inside it, at one snapshot, and is invisible to other
// connections until committed. The block belongs to this connection's socket: it
// cannot be committed from another connection, a pooled one included, and a
// dropped socket rolls it back.
//
// Requires the server to have granted [FeatureSessionTxn] in the handshake (see
// [Client.SessionTxnGranted]). If it did not — an older server, or a sharded /
// forwarding node — this fails by name *before writing anything*, rather than
// sending a BEGIN the server would run as a one-statement autocommit script; the
// returned error unwraps to [ErrArgument], and [Client.Transaction] works
// everywhere.
//
// What the server enforces inside a block, in its own words when it happens: a
// failed statement aborts the block and every statement after it is refused
// until ROLLBACK; a second Begin is refused (no savepoints); DDL and
// multi-statement scripts are refused; a block left idle past the server's
// idle-in-transaction window (60s by default) is rolled back and the connection
// closed; and a block that buffers more than max_pending_writes_per_transaction
// writes is aborted.
//
// The returned result's Outcome is "began".
func (c *Client) Begin() (*TransactionResult, error) {
	if !c.SessionTxnGranted() {
		return nil, &ArgumentError{Message: "this server did not grant session transactions " +
			"(SESSION_TXN is not in the granted feature set), so Begin/Commit/Rollback cannot " +
			"open a rollback boundary on this connection; use Transaction(...) to send the " +
			"whole unit as one `BEGIN; <statements>; COMMIT` request"}
	}
	return c.txnControl("BEGIN")
}

// Commit commits the block opened by [Client.Begin], durably and as one atomic
// batch. The returned result's Outcome is "committed".
//
// A refusal is the server's own and ends the block either way: after a failed
// statement the server has already rolled it back and says so; a
// first-committer-wins conflict discards it.
func (c *Client) Commit() (*TransactionResult, error) {
	return c.txnControl("COMMIT")
}

// Rollback discards the block opened by [Client.Begin]. The returned result's
// Outcome is "rolled_back".
func (c *Client) Rollback() (*TransactionResult, error) {
	return c.txnControl("ROLLBACK")
}

// WithTransaction runs Begin, then fn, then Commit — or Rollback and the
// original error if fn returns one. A panic inside fn rolls the block back and
// re-panics, so no path leaves a block open.
//
//	err := db.WithTransaction(func(tx *tricoredb.Client) error {
//		if _, err := tx.ExecuteParams("UPDATE accounts SET balance = balance - ? WHERE id = ?", 10, 1); err != nil {
//			return err
//		}
//		_, err := tx.ExecuteParams("UPDATE accounts SET balance = balance + ? WHERE id = ?", 10, 2)
//		return err
//	})
//
// fn must issue its statements on the client it is given — this one — and only
// this one. A statement on any other connection, a pooled one included, is
// outside the block: the server binds the transaction to this socket and refuses
// COMMIT from any other by name.
func (c *Client) WithTransaction(fn func(*Client) error) error {
	if fn == nil {
		return &ArgumentError{Message: "WithTransaction needs a non-nil callback"}
	}
	if _, err := c.Begin(); err != nil {
		return err
	}
	// A panic must not leave the block open either: the recover re-panics after
	// the rollback, so the caller's stack trace is preserved.
	committed := false
	defer func() {
		if r := recover(); r != nil {
			c.abandonBlock()
			panic(r)
		}
		if !committed && c.InTransaction() {
			c.abandonBlock()
		}
	}()

	if err := fn(c); err != nil {
		// The caller's error is the one to report. If the socket is already gone
		// the server has rolled the block back on its own.
		c.abandonBlock()
		return err
	}
	if _, err := c.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// abandonBlock rolls an open block back, best-effort. Used on the failure paths,
// where the caller's own error is the one that must survive.
func (c *Client) abandonBlock() {
	if c.InTransaction() {
		_, _ = c.Rollback()
	}
}

// txnControl sends one transaction-control keyword.
//
// Control travels as Exec: the server authorizes it as a write and refuses it on
// Query. txnOpen follows what the server answered — every COMMIT/ROLLBACK reply,
// a refusal included, means the block is over, because the server ends it either
// way. The one exception is a request that never left this process, which this
// driver reports as [ErrArgument] (the in-flight guard, or a refusal made before
// a byte was written).
func (c *Client) txnControl(keyword string) (*TransactionResult, error) {
	resp, err := c.Execute(keyword)
	if err != nil {
		if keyword != "BEGIN" && !errors.Is(err, ErrArgument) {
			c.txnOpen.Store(false)
		}
		return nil, err
	}
	c.txnOpen.Store(keyword == "BEGIN")
	var out TransactionResult
	if err := decodeJSON(resp, keyword, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BuildTransactionScript assembles BEGIN; ...; COMMIT from a list of
// statements.
//
// Caller-supplied transaction control is refused rather than passed through:
// nesting a second BEGIN, or committing early, changes what the script means in
// a way the caller almost certainly did not intend.
func BuildTransactionScript(statements []Statement) (string, error) {
	script, args, err := buildTransactionRequest(statements)
	if err != nil {
		return "", err
	}
	if len(args) == 0 {
		return script, nil
	}
	// This function returns a *string*, so the only way to honour arguments is
	// to render them — which is the client-side binding [Client.Transaction] no
	// longer does. Kept for callers that build a script to send themselves.
	return BindParams(script, args...)
}

// buildTransactionRequest assembles the script and the flat, positional
// parameter list that goes with it.
//
// The parameters of every statement are concatenated in statement order, which
// is exactly how the server binds a multi-statement script: it walks the script
// left to right and consumes one parameter per `?`. So the script keeps its
// placeholders instead of having values pasted into it, and [Client.Transaction]
// inherits the same guarantee a single ExecuteParams has.
func buildTransactionRequest(statements []Statement) (string, []Arg, error) {
	if len(statements) == 0 {
		return "", nil, &ArgumentError{Message: "a transaction needs at least one statement"}
	}
	parts := make([]string, 0, len(statements))
	var args []Arg
	for _, s := range statements {
		text := s.SQL
		args = append(args, s.Args...)
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ";"))
		if text == "" {
			return "", nil, &ArgumentError{Message: "each transaction statement must be non-empty SQL"}
		}
		first := strings.ToUpper(strings.Fields(text)[0])
		switch first {
		case "BEGIN", "START", "COMMIT", "ROLLBACK":
			return "", nil, &ArgumentError{
				Message: fmt.Sprintf("Transaction brackets the script itself — remove the `%s` statement", first),
			}
		}
		parts = append(parts, text)
	}
	return "BEGIN; " + strings.Join(parts, "; ") + "; COMMIT", args, nil
}

// BindParams renders sql with each ? replaced by the corresponding argument,
// escaped as a SQL literal.
//
// # This is client-side binding
//
// TriCoreDB V1 has no server-side prepared statements: the wire carries one sql
// string and nothing else. Parameters are therefore rendered into that string
// here, in the driver, before it is sent. That is a real and useful feature —
// it removes the hand-rolled concatenation where injection bugs actually come
// from — but it is not the guarantee a server-side bind gives, and this comment
// is where that distinction is stated rather than blurred.
//
// Guarantees: strings are escaped by doubling every ', which is the only escape
// the server's tokenizer recognises (there is no backslash escape to smuggle a
// quote past); numbers render in a fixed form; only a closed set of types is
// accepted, so a struct never reaches the statement via %v; placeholder and
// argument counts must match; and a ? inside a string literal is left alone.
//
// It cannot protect an identifier. Table and column names are not values and
// are not bindable — build those from a whitelist you control.
func BindParams(sql string, args ...Arg) (string, error) {
	var b strings.Builder
	b.Grow(len(sql) + len(args)*8)
	next := 0
	inString := false

	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\'' {
			// `''` inside a literal is an escaped quote, not the end of one.
			if inString && i+1 < len(runes) && runes[i+1] == '\'' {
				b.WriteString("''")
				i++
				continue
			}
			inString = !inString
			b.WriteRune(ch)
			continue
		}
		if ch == '?' && !inString {
			if next >= len(args) {
				return "", &ArgumentError{Message: fmt.Sprintf(
					"SQL has more `?` placeholders than the %d argument(s) supplied", len(args))}
			}
			lit, err := SQLLiteral(args[next])
			if err != nil {
				return "", err
			}
			b.WriteString(lit)
			next++
			continue
		}
		b.WriteRune(ch)
	}

	if inString {
		return "", &ArgumentError{Message: "SQL ends inside an unterminated string literal"}
	}
	if next != len(args) {
		return "", &ArgumentError{Message: fmt.Sprintf(
			"SQL has %d `?` placeholder(s) but %d argument(s) were supplied", next, len(args))}
	}
	return b.String(), nil
}

// SQLLiteral renders one Go value as a SQL literal.
func SQLLiteral(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "TRUE", nil
		}
		return "FALSE", nil
	case string:
		return QuoteSQL(v), nil
	case []byte:
		// Quoted hex text, which a BLOB column parses. string(v) would put raw
		// bytes into the statement and corrupt anything that is not UTF-8.
		return QuoteSQL(hexBytes(v)), nil
	case Decimal, *big.Rat, *big.Float:
		d, isNull, err := decimalText(v)
		if err != nil {
			return "", err
		}
		if isNull {
			return "NULL", nil
		}
		return QuoteSQL(d), nil
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float32:
		return realLiteral(float64(v))
	case float64:
		return realLiteral(v)
	case time.Time:
		// The server has no date type, so a timestamp is stored as text and
		// must round-trip in a sortable form.
		return QuoteSQL(v.UTC().Format("2006-01-02 15:04:05.000")), nil
	default:
		return "", &ArgumentError{Message: fmt.Sprintf(
			"no SQL literal form for %T. Convert it explicitly — falling back to %%v would put a "+
				"value's debug rendering into the statement.", value)}
	}
}

func realLiteral(f float64) (string, error) {
	// The parser has no literal for these, so emitting one produces a statement
	// the server rejects with a syntax error far from the cause.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", &ArgumentError{Message: fmt.Sprintf("`%v` has no SQL literal form", f)}
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}

// QuoteSQL escapes and quotes a string: double every ', which is the only
// escape the server's tokenizer recognises.
func QuoteSQL(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
