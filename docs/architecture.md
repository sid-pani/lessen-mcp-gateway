# Architecture

## Design priorities

1. MCP compatibility and correct request routing.
2. Bounded memory under normal and failure conditions.
3. Low operational friction for a local developer.
4. One HTTP listener for protocol, UI, and management.
5. Replaceable persistence without changing MCP-facing behavior.

## Data flow

```text
AI host
  │
  │ JSON-RPC over /mcp or stdio
  ▼
Gateway protocol layer
  │
  ├── namespaced tool/resource/prompt routing
  ├── bounded global and per-server concurrency
  └── request deadlines and cancellation
  │
Capability registry
  │
  ├── small in-memory tool-name index
  ├── bounded full-schema LRU
  └── persistent metadata/schema store
  │
Backend manager
  ├── stdio subprocess
  ├── Streamable HTTP
  └── legacy SSE
```

## Persistence

SQLite is the zero-configuration default. The SQLite adapter stores full schemas and payloads on disk and exposes summary-only queries so the administration UI and routing index do not need to materialize every schema.

The optional PostgreSQL adapter is selected with `storage.type=postgres` and compiled with `-tags postgres`. Its schema stores model payloads as JSONB and uses one serialized libpq connection. The storage contract is intentionally small enough to support additional adapters later.

## Discovery

For each configured backend, the manager:

1. Lazily creates the transport client.
2. Probes `server/discover` using stateless MCP `2026-07-28` request metadata.
3. If the modern method is unavailable, performs the stateful `initialize`/`notifications/initialized` lifecycle using compatible 2025-era revisions from newest to oldest.
4. Walks cursor-based `tools/list`, `resources/list`, `resources/templates/list`, and `prompts/list` responses.
5. Adds the required per-request `_meta` envelope to every modern backend call.
6. Normalizes and namespaces tools and resource routes.
7. Atomically replaces the server's persisted capability shard.
8. Updates health and discovery metadata.

Only one server shard is replaced during a server-specific rediscovery. This avoids rebuilding an unrelated global schema graph during hot reload.

## Memory boundaries

Every potentially growing subsystem has a configured limit:

- Go runtime soft limit.
- Full-schema LRU byte limit.
- Log-ring byte limit.
- File-log size and rotation count.
- HTTP request and response limits.
- Per-event SSE limit.
- Global and per-server concurrency semaphores.
- Fixed-size discovery and health-check worker pools.
- HTTP idle and active connection limits.
- Discovery item limits per server.

The in-memory routing index contains only exposed tool names and backend/name references. Search is performed by the persistent store rather than by an embedding model or large in-memory document index.

## Stdio lifecycle

Lazy stdio servers are started for discovery or invocation and stopped after the configured idle timeout. Their last discovered capabilities remain persisted, so gateway startup does not require every child process to stay resident.

The stdio transport uses newline-delimited JSON-RPC messages, a bounded scanner, a pending-call map keyed by raw JSON-RPC IDs, and generation tracking so a restarted subprocess is re-negotiated before use.

## Protocol eras

MCP `2026-07-28` is handled as a stateless protocol era. Every request carries its protocol version, client identity, and client capabilities in `_meta`; HTTP requests additionally mirror the method and target name in routing headers. The gateway adds `resultType`, cache hints where required, and its server identity to modern responses.

Older protocol revisions retain their initialization handshake and optional HTTP session ID. The backend manager keeps this compatibility isolated from routing and persistence so both eras expose the same normalized registry to clients.

## Configuration reload

Configuration writes use a private temporary file, `fsync`, and atomic rename. The watcher polls file metadata once per second and validates a complete configuration before applying it.

Changed backend definitions get new clients. Removed backend clients and processes are closed. The HTTP listener and persistence adapter are process-level resources, so changes to their address/path or storage settings are logged as restart-required.

## UI

The UI is plain HTML, CSS, and JavaScript embedded with Go's `embed` package. There is no frontend framework or package runtime in production. It uses the same HTTP origin and management API, with a restrictive Content Security Policy.

Dark and light palettes are implemented with CSS custom properties. The preference is stored in the browser and falls back to the operating-system color scheme.
