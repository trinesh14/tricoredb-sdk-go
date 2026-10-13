# Changelog

All notable changes to this module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-10-13

Every method added here needs `tricore-server` **0.1.0-rc.3** or later. An
older server does not know these operations and answers each with an error;
the methods that existed before are unchanged and work against it as before.

### Added

- Cache, 53 operations: `CacheMGet`, `CacheMSet`, `CacheWarm`, `CacheSetIf`,
  `CacheCompareAndSwap`, `CacheGetDel`, `CacheGetSet`, `CacheGetEx`,
  `CacheAppend`, `CacheExpireAt`; stale-while-revalidate (`CacheSetFresh`,
  `CacheGetFresh`, `CacheSetMissing`); sorted sets (`CacheZAdd`, `CacheZRem`,
  `CacheZScore`, `CacheZCard`, `CacheZRank`, `CacheZRange`,
  `CacheZRangeByScore`, their `WithScores` forms, `CacheZIncrBy`); locks with
  fencing tokens (`CacheLockAcquire`, `CacheLockRenew`, `CacheLockRelease`);
  bitmaps (`CacheSetBit`, `CacheGetBit`, `CacheBitCount`, `CacheBitPos`,
  `CacheBitOp`); HyperLogLog (`CachePfAdd`, `CachePfCount`, `CachePfMerge`);
  `CacheLMove`, `CacheLTrim`, `CacheBLPop`, `CacheBRPop`, `CacheSUnion`,
  `CacheSInter`, `CacheSDiff`, `CacheSRandMember`, `CacheSRandMembers`,
  `CacheHIncrBy`; pipelines, transactions and scripts (`CachePipeline`,
  `CacheTransaction`, `CacheScriptLoad`, `CacheScriptRun`, built from
  `CacheCommand` values); `CachePublish`, `CachePoll`; and `CacheStats`,
  `CacheHotKeys`, `CachePurgeExpired`, `CacheFlushDatabase`,
  `CacheSetNamespaceLimit`, `CacheGetNamespaceLimit`.
- Documents, 13 operations: `DocumentReplace`, `DocumentInsertMany`,
  `DocumentDeleteMany`, `DocumentBulkWrite` with `BulkOp` for its items,
  `DocumentRenameCollection`, `DocumentPurgeExpired`, `DocumentExplain`,
  `DocumentRebuildIndex`, `DocumentSetCollectionOptions`,
  `DocumentGetCollectionOptions`, and transactions spanning requests
  (`DocumentBeginTransaction`, `DocumentCommitTransaction`,
  `DocumentRollbackTransaction`).
- Vectors, 21 operations: `VectorUpsertMany`, `VectorUpsertText`,
  `VectorUpsertNamed`, `VectorAddNamedVector`, `VectorDeleteMany`,
  `VectorDeleteByFilter`, `VectorUpdateByFilter`, `VectorCount`,
  `VectorSearchBatch`, `VectorSearchMulti`, `VectorSearchByExamples`,
  `VectorEmbed`, `VectorSetAlias`, `VectorListAliases`,
  `VectorListPartitions`, `VectorRenameCollection`, `VectorCopyCollection`,
  `VectorSetCollectionConfig`, `VectorRebuildIndex`, `VectorIndexStats` and
  `VectorMeasureRecall`. `VectorHit` gains `Vector` and `Reason`.
- Graphs, 29 operations: `GraphAddMany`, `GraphRemoveMany`, `GraphUpdateNode`,
  `GraphUpdateEdge`, `GraphCopy`, `GraphImportCSV`, `GraphDfs`,
  `GraphSubgraph`, `GraphKShortestPaths`, `GraphAllPaths`,
  `GraphConnectedComponents`, `GraphDetectCycle`, `GraphCentrality`,
  `GraphCommunities`, `GraphTriangleCount`, `GraphNodeSimilarity`,
  `GraphPageRank`, `GraphStats`, `GraphSchema`, `GraphCheckConsistency`,
  `GraphCreateNodeIndex`, `GraphCreateConstraint`, `GraphDropSchemaObject`,
  `GraphListSchemaObjects`, `GraphFindNodes`, `GraphExecute`, and transactions
  (`GraphBeginTransaction`, `GraphCommitTransaction`,
  `GraphRollbackTransaction`).
- `Client.SetRequestTxn` and `Client.RequestTxn`: the document or graph
  transaction named on every later request. Committing or rolling a
  transaction back through this client clears it.

### Fixed

- A `BEGIN` sent inside an open transaction opens a nested block, and the
  `COMMIT` or `ROLLBACK` that closes it leaves the transaction open.
  `InTransaction` reported false after it, so a `Pool` could return the
  connection mid-transaction. It now follows the reply: one carrying
  `nested_depth` closed a nested block only. `TransactionResult` gains
  `NestedDepth`, and a pool rolls back every block a callback left open.
- The `Version` constant said `0.1.0` through the 0.2.0 release. It is `0.3.0`.

### Notes

- `CacheHotKeys` returns the server's payload as raw JSON rather than a typed
  result. The server answers it only when `[cache] hot_keys` is above zero, and
  the server this release was tested against had it off, so its answer was
  never observed.
- The `e2e_models` suite can run against a server that is already running: set
  `TRICORE_TEST_ADDR`, `TRICORE_TEST_USER` and `TRICORE_TEST_SECRET`.

## [0.2.0] - 2026-09-17

### Added

- Following the leader: a request refused with `not_leader` is sent again,
  to the leader the refusal names (over a new connection that replaces the
  current one) or, while no leader is known, to the same node after a pause.
  On by default; `Options.DisableFollowLeader` restores the previous
  behaviour, and `LeaderRetries` (5) and `LeaderRetryDelay` (250ms) bound it.
  Never inside a session transaction.
- `ErrorCodeOutcomeUnknown`: a request a follower handed to the leader and
  lost the answer to. It is never sent again.
- `Client.Address()` and `ServerError.Retried`.

## [0.1.0] - 2026-09-15

First release as a standalone Go module.

### Added

- Native TriCoreDB protocol client for Go 1.21 and later, using only the
  standard library: no dependencies.
- SQL: `Query`, `Execute`, and `QueryParams` / `ExecuteParams` with `?`
  placeholders bound by the server. `Decimal` keeps exact digits.
- Transactions: one-request scripts (`Transaction`) and session blocks
  (`Begin`, `Commit`, `Rollback`, `WithTransaction`).
- Connection pool (`NewPool`, `Pool.Use`) that never returns a connection with
  an open transaction.
- Documents, vectors, graphs, cache (keys, lists, sets, hashes, streams), LLM
  context and admin operations.
- Typed errors that unwrap to `ErrAuth`, `ErrServer`, `ErrProtocol`,
  `ErrArgument` or `ErrTimeout`, with server error codes and leader hints
  (`ServerError.Code`, `ServerError.IsNotLeader`, `ServerError.LeaderHint`).
- TLS and mutual TLS through `TLSOptions`.

### Security

- Operations that need a server capability which was not granted (server-side
  parameters, session transactions) fail with `ErrArgument` before anything is
  sent, instead of silently falling back.
- With TLS and no `CAFile`, the trust store is empty: the client never falls
  back to the operating system's root certificates.

[Unreleased]: https://github.com/trinesh14/tricoredb-sdk-go/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/trinesh14/tricoredb-sdk-go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/trinesh14/tricoredb-sdk-go/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/trinesh14/tricoredb-sdk-go/releases/tag/v0.1.0
