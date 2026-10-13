package tricoredb

import (
	"context"
	"testing"
)

// BEGIN inside an open transaction opens a nested block, and the COMMIT or
// ROLLBACK that closes it answers with nested_depth instead of an outcome. The
// transaction around it is still open, and a client that read any COMMIT or
// ROLLBACK reply as the end would hand the connection on mid-transaction.

func nestedReply(rid string, data map[string]any) map[string]any {
	return map[string]any{
		"request_id": rid, "status": "ok", "data": map[string]any{"Json": data}, "diagnostics": peerDiagnostics(nil),
	}
}

func TestClosingANestedBlockLeavesTheTransactionOpen(t *testing.T) {
	// BEGIN, BEGIN, ROLLBACK, BEGIN, COMMIT, COMMIT.
	p := startPeer(t, func(rid string, n int) map[string]any {
		switch n {
		case 1:
			return nestedReply(rid, map[string]any{"transaction": "began"})
		case 2, 4:
			return nestedReply(rid, map[string]any{"nested_depth": 1, "rows_affected": 0})
		case 3, 5:
			return nestedReply(rid, map[string]any{"nested_depth": 0, "rows_affected": 0})
		}
		return nestedReply(rid, map[string]any{"transaction": "committed", "committed_writes": 1})
	})
	db := connectTo(t, p, Options{User: "u", Secret: "s"})

	if _, err := db.Begin(); err != nil || !db.InTransaction() {
		t.Fatalf("begin: %v, open %v", err, db.InTransaction())
	}
	inner, err := db.Begin()
	if err != nil || inner.NestedDepth == nil || *inner.NestedDepth != 1 {
		t.Fatalf("a second BEGIN nests: %+v, %v", inner, err)
	}
	undone, err := db.Rollback()
	if err != nil || undone.NestedDepth == nil || *undone.NestedDepth != 0 {
		t.Fatalf("nested rollback: %+v, %v", undone, err)
	}
	if !db.InTransaction() {
		t.Fatal("a ROLLBACK that closed a nested block read as the transaction ending")
	}
	if _, err := db.Begin(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Commit(); err != nil {
		t.Fatal(err)
	}
	if !db.InTransaction() {
		t.Fatal("a COMMIT that released a nested block read as the transaction ending")
	}
	done, err := db.Commit()
	if err != nil || done.Outcome != "committed" || done.NestedDepth != nil {
		t.Fatalf("commit: %+v, %v", done, err)
	}
	if db.InTransaction() {
		t.Fatal("the outer COMMIT did not end the transaction")
	}
}

func TestAPoolRollsBackEveryBlockACallbackLeftOpen(t *testing.T) {
	// BEGIN and a nested BEGIN from the callback; then the pool's own
	// rollbacks: the first closes the nested block, the second the transaction.
	p := startPeer(t, func(rid string, n int) map[string]any {
		switch n {
		case 1:
			return nestedReply(rid, map[string]any{"transaction": "began"})
		case 2:
			return nestedReply(rid, map[string]any{"nested_depth": 1, "rows_affected": 0})
		case 3:
			return nestedReply(rid, map[string]any{"nested_depth": 0, "rows_affected": 0})
		}
		return nestedReply(rid, map[string]any{"transaction": "rolled_back"})
	})
	pool, err := NewPool(Options{Host: "127.0.0.1", Port: p.port(), User: "u", Secret: "s"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	err = pool.Use(context.Background(), func(db *Client) error {
		if _, err := db.Begin(); err != nil {
			return err
		}
		_, err := db.Begin()
		return err
	})
	if err == nil {
		t.Fatal("a callback that leaves a block open is told so")
	}
	if _, requests := p.counts(); requests != 4 {
		t.Fatalf("want one rollback for the nested block and one for the transaction, saw %d requests", requests)
	}
	open := true
	if err := pool.Use(context.Background(), func(db *Client) error {
		open = db.InTransaction()
		return nil
	}); err != nil || open {
		t.Fatalf("the next borrower starts clean: open %v, %v", open, err)
	}
}
