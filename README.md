# tricoredb-sdk-go

Official Go client for [TriCoreDB](https://hub.docker.com/r/trinesh14/tricoredb):
SQL, documents, vectors, graphs and cache over one native connection.

[![Go Reference](https://pkg.go.dev/badge/github.com/trinesh14/tricoredb-sdk-go.svg)](https://pkg.go.dev/github.com/trinesh14/tricoredb-sdk-go)
[![Go version](https://img.shields.io/github/go-mod/go-version/trinesh14/tricoredb-sdk-go?logo=go&label=go&cacheSeconds=3600)](go.mod)
[![License](https://img.shields.io/github/license/trinesh14/tricoredb-sdk-go?label=license&color=blue&cacheSeconds=3600)](LICENSE)

- **No dependencies.** It uses only the Go standard library.
- **Server-side parameters.** Values never become part of the SQL text.
- **Typed errors** that work with `errors.Is` and `errors.As`.
- **Transactions, connection pooling, TLS and mutual TLS.**

## Contents

- [Requirements](#requirements)
- [Installation](#installation)
- [Running a server](#running-a-server)
- [Quick start](#quick-start)
- [Connecting](#connecting)
- [SQL](#sql)
- [Transactions](#transactions)
- [Connection pool](#connection-pool)
- [Cache](#cache)
- [Documents](#documents)
- [Vectors](#vectors)
- [Graphs](#graphs)
- [LLM context](#llm-context)
- [Admin](#admin)
- [Errors](#errors)
- [TLS](#tls)
- [Testing](#testing)

## Requirements

- Go **1.21** or later
- A TriCoreDB server speaking protocol 1.0 (`tricore-server` 0.1.0-rc.1 or
  later). See [Running a server](#running-a-server).

## Installation

```bash
go get github.com/trinesh14/tricoredb-sdk-go
```

```go
import tricoredb "github.com/trinesh14/tricoredb-sdk-go"
```

The package name is `tricoredb`.

## Running a server

The quickest way is the official Docker image,
[`trinesh14/tricoredb`](https://hub.docker.com/r/trinesh14/tricoredb).

**Local development** (no TLS and no encryption, for this machine only). Set
`TRICORE_ADMIN_PASSWORD` in your shell first. Then create the admin and start
the server:

```bash
docker run --rm -v tricoredb-dev:/var/lib/tricoredb -e TRICORE_ADMIN_PASSWORD --entrypoint /usr/local/bin/tricore trinesh14/tricoredb:0.1.0-rc.1-r2 auth init-admin --user admin --password-env TRICORE_ADMIN_PASSWORD --data-dir /var/lib/tricoredb/data
docker run -d --name tricoredb-dev -p 127.0.0.1:8427:8427 -e TRICORE_TLS=off -e TRICORE_ENCRYPTION=off -e TRICORE_MODULES=all -v tricoredb-dev:/var/lib/tricoredb trinesh14/tricoredb:0.1.0-rc.1-r2
```

**Anything else:** by default the image runs with **TLS on** and an
**encrypted data volume**. Follow the quick start on the
[Docker Hub page](https://hub.docker.com/r/trinesh14/tricoredb) to create the
certificate and key, then connect with [TLS](#tls).

`TRICORE_MODULES=all` enables every data model. The image's default is `sql`,
`document` and `cache`. A call to a disabled model returns a `*ServerError`
whose `Code` is `engine.disabled`.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	tricoredb "github.com/trinesh14/tricoredb-sdk-go"
)

func main() {
	ctx := context.Background()
	db, err := tricoredb.Connect(ctx, tricoredb.Options{
		Host: "127.0.0.1", Port: 8427, User: "admin", Secret: "your-password",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Execute("CREATE TABLE IF NOT EXISTS users (id INT PRIMARY KEY, name TEXT)"); err != nil {
		log.Fatal(err)
	}
	if _, err := db.ExecuteParams("INSERT INTO users VALUES (?, ?)", 1, "O'Hara"); err != nil {
		log.Fatal(err)
	}
	rows, err := db.QueryParams("SELECT name FROM users WHERE id = ?", 1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(rows.Columns, rows.Rows) // [name] [[O'Hara]]

	if err := db.CacheSet("sessions", "u1", []byte("token")); err != nil {
		log.Fatal(err)
	}
	v, found, err := db.CacheGet("sessions", "u1")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("found=%v value=%q\n", found, v)
}
```

## Connecting

`Connect` opens one authenticated connection. `Close` releases it.

| `Options` field | Default | Meaning |
| --- | --- | --- |
| `Host` | `"127.0.0.1"` | Server host |
| `Port` | `8427` (`tricoredb.DefaultPort`) | Server port |
| `User` | `""` | User name. Credentials are sent only when it is set. |
| `Secret` | `""` | Password |
| `Database` | `"main"` | Database used by SQL calls |
| `ClientName` | `"tricoredb-go"` | Name reported to the server |
| `DialTimeout` | driver default | Time allowed to connect and complete the handshake |
| `ReadTimeout` | `0` (none) | Time allowed for each reply |
| `TLS` | `nil` (plaintext) | See [TLS](#tls) |
| `Features` | all features | Override the capability bitmap sent in the handshake |

A `*Client` runs one request at a time: overlapping calls on the same client
are refused instead of mixing their frames. For concurrent use, create a
[pool](#connection-pool).

## SQL

`Query` runs only `SELECT`. `Execute` runs everything else. The server enforces
the split: a write sent through `Query` is refused.

```go
_, err := db.Execute("INSERT INTO users VALUES (2, 'ada')")
rows, err := db.Query("SELECT id, name FROM users")
for _, r := range rows.Rows {
	fmt.Println(r) // []string, one entry per column in rows.Columns
}
```

`QueryParams` and `ExecuteParams` bind `?` placeholders **on the server**. The
values travel next to the statement, so a value can never be read as SQL
syntax, however it is spelled. Use `tricoredb.Decimal` to keep exact digits.

Binding needs the `SERVER_PARAMS` capability, which is agreed in the handshake
(`db.ServerParamsGranted()`). If the server did not grant it, these methods
fail with `ErrArgument` before anything is sent. They never fall back to
pasting values into the SQL text.

## Transactions

There are two kinds, and they are not interchangeable.

`Transaction` sends a whole `BEGIN … COMMIT` script in **one request**. It
works on every node and is the right choice when every statement is known up
front:

```go
res, err := db.Transaction(
	tricoredb.Stmt("UPDATE accounts SET balance = balance - ? WHERE id = ?", 10, 1),
	tricoredb.Stmt("UPDATE accounts SET balance = balance + ? WHERE id = ?", 10, 2),
)
// res.Outcome == "committed"
```

`Begin`, `Commit` and `Rollback` keep a transaction open **across requests on
this connection**, so a later statement can depend on what an earlier one
read. `WithTransaction` commits when the function returns `nil` and rolls back
on an error or a panic:

```go
err := db.WithTransaction(func(tx *tricoredb.Client) error {
	if _, err := tx.ExecuteParams("UPDATE accounts SET balance = balance - ? WHERE id = ?", 10, 1); err != nil {
		return err
	}
	_, err := tx.ExecuteParams("UPDATE accounts SET balance = balance + ? WHERE id = ?", 10, 2)
	return err
})
```

Session transactions need the `SESSION_TXN` capability
(`db.SessionTxnGranted()`). Without it, `Begin` fails with `ErrArgument` rather
than silently running each statement on its own. The transaction belongs to
this connection: another connection cannot commit it, and a dropped connection
rolls it back.

## Connection pool

A pool is built from the same `Options`, so every pooled connection uses the
same TLS settings.

```go
pool, err := tricoredb.NewPool(tricoredb.Options{
	Host: "db.internal", Port: 8427, User: "admin", Secret: "your-password",
}, 8)
if err != nil {
	log.Fatal(err)
}
defer pool.Close()

err = pool.Use(ctx, func(c *tricoredb.Client) error {
	_, err := c.ExecuteParams("INSERT INTO users VALUES (?, ?)", 3, "grace")
	return err
})
```

`Pool.Use` never returns a connection with an open transaction to the pool. It
rolls the transaction back first, and discards the connection if that
rollback fails.

## Cache

Values are byte slices. `found` tells a miss apart from an empty value.

```go
db.CacheSet("sessions", "u1", []byte("token"))
db.CacheSetTTL("sessions", "u2", []byte("token"), 30_000) // expires in 30 s
v, found, err := db.CacheGet("sessions", "u1")
n, err := db.CacheIncr("counters", "hits", 1)

db.CacheLPush("queue", "jobs", []byte("a"), []byte("b"))
db.CacheSAdd("tags", "post:1", []byte("go"), []byte("db"))
db.CacheHSetText("user:1", "profile", map[string]string{"name": "ada"})
id, err := db.CacheXAddText("events", "log", map[string]string{"msg": "hi"}, "")
```

| Family | Methods |
| --- | --- |
| Keys | `CacheGet`, `CacheSet`, `CacheSetTTL`, `CacheSetNx`, `CacheDelete`, `CacheExists`, `CacheTTL`, `CacheExpire`, `CachePersist`, `CacheIncr`, `CacheKeys`, `CacheClearNamespace`, `CachePing` |
| Lists | `CacheLPush`, `CacheRPush`, `CacheLPop`, `CacheRPop`, `CacheLRange`, `CacheLLen`, `CacheLIndex` |
| Sets | `CacheSAdd`, `CacheSRem`, `CacheSIsMember`, `CacheSCard`, `CacheSMembers` |
| Hashes | `CacheHSet`, `CacheHSetText`, `CacheHGet`, `CacheHDel`, `CacheHGetAll`, `CacheHExists`, `CacheHLen` |
| Streams | `CacheXAdd`, `CacheXAddText`, `CacheXLen`, `CacheXRange`, `CacheXRead`, `CacheXDel`, `CacheXTrim` |

## Documents

Filters and aggregation stages are built with helper functions (`FilterEq`,
`FilterGt`, `FilterAnd`, `StageMatch`, `StageGroup`, …), so you never write the
request JSON by hand.

```go
db.DocumentCreateCollection("products")
id, err := db.DocumentInsert("products", map[string]any{"name": "widget", "price": 9})
doc, found, err := db.DocumentGet("products", id)
docs, err := db.DocumentFind("products", tricoredb.FilterGt("price", 5))
err = db.DocumentUpdateOne("products", id, tricoredb.DocumentUpdate{
	Inc: map[string]any{"price": 1},
})
```

Also available: `DocumentInsertWithID`, `DocumentFindLimit`, `DocumentUpdate`,
`DocumentUpsertOne`, `DocumentUpdateMany`, `DocumentDelete`,
`DocumentListCollections`, `DocumentDropCollection`, `DocumentCreateIndex`,
`DocumentDropIndex`, `DocumentListIndexes`, `DocumentAnalyze` and
`DocumentAggregate`.

## Vectors

Fixed-dimension collections with cosine, dot or L2 distance, metadata filters
and optional int8 quantization.

```go
db.VectorCreateCollection("embeddings", 3, tricoredb.MetricCosine)
db.VectorUpsert("embeddings", "a", []float32{0.1, 0.2, 0.3}, map[string]any{"kind": "doc"})
res, err := db.VectorSearch("embeddings", []float32{0.1, 0.2, 0.3}, 5)
for _, hit := range res.Results {
	fmt.Println(hit.ID, hit.Score, hit.Metadata)
}
```

Also available: `VectorCreateCollectionQuantized`, `VectorSearchFiltered`,
`VectorGet`, `VectorDelete`, `VectorListCollections`,
`VectorDescribeCollection`, `VectorListVectors` and `VectorDropCollection`.

## Graphs

Labelled nodes, typed edges, traversal, shortest paths and a read-only Cypher
subset.

```go
db.GraphCreate("social")
db.GraphAddNode("social", "u1", []string{"User"}, map[string]any{"name": "ada"})
db.GraphAddNode("social", "u2", []string{"User"}, nil)
db.GraphAddEdge("social", "e1", "u1", "u2", "FOLLOWS", nil)

neighbors, err := db.GraphNeighbors("social", "u1", tricoredb.NeighborOptions{
	Direction: tricoredb.DirectionOutgoing,
})
path, err := db.GraphShortestPath("social", "u1", "u2", tricoredb.PathOptions{})
// path.Found, path.Hops, path.NodePath
```

Also available: `GraphGetNode`, `GraphGetEdge`, `GraphDeleteNode`,
`GraphDeleteEdge`, `GraphList`, `GraphDrop`, `GraphTraverse`,
`GraphWeightedShortestPath`, `GraphDegree`, `GraphListNodes`,
`GraphListEdges` and `GraphQuery`.

## LLM context

Read-only export of query results and schema, in TOON, JSON, Markdown or the
native format.

```go
sources := []tricoredb.LlmSource{
	tricoredb.SQLSource("SELECT id, name FROM users"),
	tricoredb.DocumentFindSource("products", tricoredb.FilterAll(), 0),
}
bundle, err := db.LlmContext(sources, tricoredb.FormatTOON, nil)
schema, err := db.LlmSchema(tricoredb.FormatMarkdown, nil)
```

## Admin

```go
err := db.AdminPing()
status, err := db.AdminStatus()
```

Admin calls need the cluster module enabled on the server, even on a single
node. `db.Ping()` checks the connection itself.

## Errors

Every error returned by the client unwraps to one sentinel, so check with
`errors.Is`:

```go
_, err := db.Query("SELECT * FROM missing")
switch {
case errors.Is(err, tricoredb.ErrArgument): // refused by the client; nothing was sent
case errors.Is(err, tricoredb.ErrAuth):     // the server refused the credentials
case errors.Is(err, tricoredb.ErrServer):   // the request arrived; the operation failed
case errors.Is(err, tricoredb.ErrProtocol): // the peer did not speak the protocol
case errors.Is(err, tricoredb.ErrTimeout):  // no reply in time
}
```

The concrete types are `*ArgumentError`, `*AuthError`, `*ServerError`,
`*ProtocolError` and `*TimeoutError`. Do not retry an `ErrArgument`: the same
call will fail the same way.

`*ServerError` has a `Code` field with the server's machine-readable error
code. Branch on `Code`, not on the message text.

**Leader redirects.** In a cluster, a write that reaches a follower fails with
`Code == "not_leader"`, and `IsNotLeader()` returns true. When the leader is
known, `LeaderHint` holds its `host:port`. An empty hint means the leader is
not known yet (for example during an election), so wait and retry. The client
does not follow redirects for you: where to resend a write is your
application's decision.

```go
var se *tricoredb.ServerError
if errors.As(err, &se) && se.IsNotLeader() && se.LeaderHint != "" {
	// reconnect to se.LeaderHint and retry
}
```

## TLS

TLS is off unless `Options.TLS` is set. Without TLS the password is sent in
plain text, so use TLS for anything other than local development.

```go
db, err := tricoredb.Connect(ctx, tricoredb.Options{
	Host: "db.internal", Port: 8427, User: "admin", Secret: "your-password",
	TLS: &tricoredb.TLSOptions{
		CAFile:     "/etc/tricore/ca.pem", // CA that signed the server certificate
		ServerName: "db.internal",         // name checked in the certificate
	},
})
```

With TLS on, the server certificate and host name are always verified. If
`CAFile` is not set, the trust store is **empty**: the client does not fall
back to the operating system's certificates, so a wrong setting fails instead
of trusting an unrelated CA.

For mutual TLS, set both `ClientCertFile` and `ClientKeyFile`. Setting only one
is an error.

| `TLSOptions` field | Default | Meaning |
| --- | --- | --- |
| `CAFile` | `""` (empty trust store) | PEM file used to verify the server |
| `ServerName` | `"localhost"` | Expected server name (SNI and certificate check) |
| `ClientCertFile` | `""` | PEM client certificate chain, for mutual TLS |
| `ClientKeyFile` | `""` | PEM private key for that certificate |
| `DangerAcceptInvalidCerts` | `false` | **Development only.** Skips all verification. |

Error messages name certificate and key *paths*, never their contents.

## Testing

```bash
go vet ./...
go test ./...
```

The root package's tests need no server. The `e2e_hardening` and `e2e_models`
suites start a real `tricore-server`; point `TRICORE_SERVER_BIN` at the binary
to run them. Without it, those tests are skipped.

## License

[Apache License 2.0](LICENSE)
