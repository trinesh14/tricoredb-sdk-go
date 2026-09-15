// Package tricoredb is the Go driver for TriCoreDB's native "tricore" wire
// protocol.
//
// It speaks the protocol directly over TCP (optionally TLS) and depends on
// nothing beyond the standard library. One [Client] is one connection; one
// [Pool] is a bounded set of them for concurrent use.
//
//	ctx := context.Background()
//	db, err := tricoredb.Connect(ctx, tricoredb.Options{
//		Host: "127.0.0.1", Port: 8427, User: "admin", Secret: "pw",
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer db.Close()
//
//	// SQL, with ? placeholders bound by the server.
//	_, err = db.ExecuteParams("INSERT INTO t (id, name) VALUES (?, ?)", 1, "ada")
//	rows, err := db.QueryParams("SELECT name FROM t WHERE id = ?", 1)
//	// rows.Rows == [["ada"]]
//
//	// Cache values are bytes; found distinguishes a miss from an empty value.
//	err = db.CacheSet("sessions", "u1", []byte("token"))
//	v, found, err := db.CacheGet("sessions", "u1")
//
// # Data models
//
// SQL ([Client.Execute], [Client.QueryParams], [Client.Transaction],
// [Client.Begin]), cache ([Client.CacheSet] and the list, set, hash and stream
// families), documents ([Client.DocumentInsert], [Client.DocumentFind],
// [Client.DocumentAggregate]), vectors ([Client.VectorUpsert],
// [Client.VectorSearch]), graphs ([Client.GraphAddNode],
// [Client.GraphShortestPath], [Client.GraphQuery]) and LLM context export
// ([Client.LlmContext]).
//
// # Negotiated capabilities
//
// Server-side parameters and session transactions are optional protocol
// features negotiated in the handshake. When the server did not grant one, the
// methods that need it fail with [ErrArgument] before anything is sent; they
// never fall back to a weaker behaviour silently. See [FeatureServerParams] and
// [FeatureSessionTxn].
//
// # Errors
//
// Every error unwraps to one of [ErrAuth], [ErrServer], [ErrProtocol],
// [ErrArgument] or [ErrTimeout]. A write refused by a Raft follower is a
// [*ServerError] whose [ServerError.IsNotLeader] is true; the driver does not
// follow redirects itself.
package tricoredb
