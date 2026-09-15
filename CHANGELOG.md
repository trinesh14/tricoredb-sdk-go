# Changelog

All notable changes to this module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/trinesh14/tricoredb-sdk-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/trinesh14/tricoredb-sdk-go/releases/tag/v0.1.0
