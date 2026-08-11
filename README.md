# Lessen MCP Gateway

A lightweight, bounded-memory Model Context Protocol gateway that presents multiple MCP servers through one local endpoint. It discovers and namespaces backend capabilities, persists full schemas outside the hot path, supervises local stdio servers, translates HTTP transports, and includes a compact administration interface with dark and light modes.

> **Project status:** early alpha. The core gateway, storage, transports, discovery, CLI, REST API, and administration UI are functional. Protocol conformance coverage and advanced compact-tool virtualization are still being expanded.

## What is included

- One HTTP server for the MCP endpoint, management API, admin UI, health, and readiness.
- Backend support for `stdio`, Streamable HTTP, and legacy HTTP+SSE.
- Native stateless MCP `2026-07-28` request metadata and `server/discover`, with stateful initialization fallback for `2025-11-25`, `2025-06-18`, and `2024-11-05` servers.
- Lazy stdio process lifecycle: start on demand, retain discovered metadata, and stop after an idle timeout.
- Embedded SQLite storage by default, using the system SQLite library without a separate database service.
- Optional PostgreSQL storage for deployments that want an external/shared database.
- Bounded schema cache, bounded log ring, bounded request/response bodies, bounded connection pools, and bounded call/discovery/health-check worker pools.
- Atomic JSON configuration writes and hot application of server configuration changes.
- Inline or environment-backed secrets, with redaction in the management API.
- A dependency-free, embedded web UI with a dense server/tool view and persistent dark/light theme selection.
- Local binary and Docker deployment paths.

## Interface

The default server listens only on loopback:

```text
http://127.0.0.1:4444/mcp       MCP JSON-RPC endpoint
http://127.0.0.1:4444/admin/    Administration UI
http://127.0.0.1:4444/api/v1/*  Management API
http://127.0.0.1:4444/healthz   Liveness
http://127.0.0.1:4444/readyz    Readiness
```

Tools are exposed with collision-safe names:

```text
github__create_issue
postgres__query
filesystem__read_file
```

The original backend tool name is restored when the call is routed upstream.

## Memory model

The gateway is designed around explicit ceilings rather than unbounded caches:

```text
Persistent store
  Full tool schemas
  Resource/prompt payloads
  Discovery state

Bounded RAM
  Small tool-name routing index
  Schema LRU (16 MB default)
  Log ring (2 MB default)
  Connection pools and queues with hard limits
  Discovery and health workers with hard limits
```

The default configuration applies a Go runtime soft memory limit of 128 MB. It is a guardrail, not a reservation. An empty local gateway typically uses only a fraction of that limit; actual RSS depends on libc, SQLite, active backend processes, schema sizes, and traffic.

Discovery and health checks use fixed-size worker pools rather than creating one goroutine per configured server. The defaults allow four concurrent discovery jobs and eight concurrent health checks, both configurable under `discovery`.

The largest practical memory savings usually come from lazy lifecycle management because Node.js or Python stdio MCP child processes may use considerably more memory than the Go gateway itself.

## Build locally

### Requirements

- Go 1.23 or newer.
- A C compiler.
- SQLite 3 development headers and library.
- Optional: PostgreSQL `libpq` development headers when building the external database adapter.

Debian/Ubuntu:

```bash
sudo apt-get install build-essential libsqlite3-dev
```

Build and test:

```bash
make check
make build
./bin/mcp-gate version
```

The generated binary embeds the admin UI. Node.js is not required at runtime.

### PostgreSQL-enabled build

The default build intentionally omits `libpq`. Enable the optional adapter with a build tag:

```bash
sudo apt-get install libpq-dev
make build-postgres
```

Then configure:

```json
{
  "storage": {
    "type": "postgres",
    "dsn": "${MCP_GATEWAY_DATABASE_URL}"
  }
}
```

The PostgreSQL adapter uses one serialized connection to keep its socket and memory footprint predictable. SQLite remains the recommended default for a developer workstation.

## Run locally

Create a private configuration file:

```bash
./bin/mcp-gate init
```

Start the gateway:

```bash
./bin/mcp-gate serve
```

Open `http://127.0.0.1:4444/admin/`.

The gateway can also expose its frontend over stdio instead of HTTP:

```bash
./bin/mcp-gate serve --stdio
```

## Configure servers

A minimal stdio backend:

```json
{
  "mcpServers": {
    "filesystem": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."],
      "lifecycle": {
        "mode": "lazy",
        "idleTimeout": "5m"
      }
    }
  }
}
```

A Streamable HTTP backend with an environment-backed token:

```json
{
  "mcpServers": {
    "github": {
      "type": "streamable-http",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${GITHUB_TOKEN}"
      }
    }
  }
}
```

An inline token is also accepted for developer-local use:

```json
{
  "headers": {
    "Authorization": "Bearer token-value"
  }
}
```

Inline values are stored in the JSON file by design. The gateway writes the file with mode `0600` on Unix, never returns the literal through `/api/v1/config`, and does not log request headers or tool arguments by default.

See [`mcp-gateway-config.example.json`](mcp-gateway-config.example.json) for all major settings.

## CLI

```bash
mcp-gate init [--config PATH]
mcp-gate serve [--config PATH] [--stdio]
mcp-gate server list [--config PATH]
mcp-gate server add NAME --type stdio --command node --args '["server.js"]'
mcp-gate server add NAME --type streamable-http --url https://example.com/mcp
mcp-gate server remove NAME [--config PATH]
mcp-gate discover NAME [--address http://127.0.0.1:4444]
mcp-gate version
```

The UI and CLI both use atomic file replacement. The running process watches the file and applies only changed server definitions; HTTP address and storage-backend changes require a restart.

## Docker

Build and run the default SQLite image:

```bash
docker build -t lessen-mcp-gateway .
docker run --rm \
  -p 127.0.0.1:4444:4444 \
  --memory=192m --memory-swap=192m \
  -v mcp-gateway-config:/config \
  -v mcp-gateway-data:/data \
  lessen-mcp-gateway
```

Or use Compose:

```bash
docker compose up --build
```

Build with PostgreSQL support:

```bash
docker build --build-arg BUILD_TAGS=postgres -t lessen-mcp-gateway:postgres .
```

A stdio backend launched by a gateway inside Docker must exist inside that same container. For separately containerized MCP servers, expose them to the gateway using Streamable HTTP or legacy SSE.

## Management API

Selected routes:

```text
GET    /api/v1/status
GET    /api/v1/config
GET    /api/v1/servers
POST   /api/v1/servers
GET    /api/v1/servers/{id}
PUT    /api/v1/servers/{id}
DELETE /api/v1/servers/{id}
POST   /api/v1/servers/{id}/discover
GET    /api/v1/tools
GET    /api/v1/tools/{server}/{tool}
GET    /api/v1/resources
GET    /api/v1/resource-templates
GET    /api/v1/prompts
GET    /api/v1/logs
```

## Security defaults

- Binds to `127.0.0.1` by default.
- Warns when listening on a non-loopback interface because built-in remote-user authentication is not implemented yet.
- Applies a restrictive Content Security Policy to the embedded UI.
- Redacts inline secrets and environment-derived credentials from management responses.
- Does not log tool arguments, tool results, headers, or prompt contents by default.
- Enforces request/response byte limits and concurrency limits before forwarding work.

For remote or shared deployment, place the gateway behind an authenticated TLS reverse proxy until first-class gateway authentication is implemented.

## Current protocol scope

Implemented now:

- JSON-RPC 2.0 request, notification, response, and batch handling.
- Stateless MCP `2026-07-28` discovery and per-request metadata, including `Mcp-Method`/`Mcp-Name` HTTP routing headers, result discrimination, cache hints, and server identity metadata.
- Compatibility fallback to the stateful initialize/initialized lifecycle for `2025-11-25`, `2025-06-18`, and `2024-11-05` servers and clients.
- Tool, resource, resource-template, and prompt discovery with cursors.
- Tool calls, resource reads, and prompt retrieval.
- Deterministically ordered gateway catalogs and private TTL hints for cacheable MCP list/read responses.
- Stateless HTTP POST frontend responses with origin and header/body consistency checks.
- Streamable HTTP backend JSON and request-scoped SSE responses.
- Legacy SSE backend endpoint discovery and response correlation.
- Cancellation through Go contexts and bounded request deadlines.

Planned next:

- Formal MCP conformance-suite integration.
- Upstream `listChanged` subscription-driven invalidation.
- Resumable legacy Streamable HTTP event streams and richer pre-2026 session compatibility.
- Optional compact mode (`search_tools`, `describe_tool`, `invoke_tool`) for aggressive token reduction.
- OAuth authorization flows and policy-based per-tool access controls.
- Pass-through support and tests for newer MCP extensions such as apps/tasks.

## Repository layout

```text
cmd/mcp-gate/                 CLI and process entrypoint
internal/app/                 Backend manager and discovery lifecycle
internal/config/              Configuration, secrets, atomic writes, watcher
internal/gateway/             MCP-facing aggregation server
internal/httpapi/             One-port HTTP and management API
internal/logbuf/              Bounded logs and file rotation
internal/registry/            Routing index and schema LRU
internal/storage/             Storage contract and memory implementation
internal/storage/sqlite/      Embedded SQLite adapter
internal/storage/postgres/    Optional external PostgreSQL adapter
internal/transport/           Stdio, Streamable HTTP, and legacy SSE clients
web/                          Embedded dependency-free admin UI
```

More detail is available in [`docs/architecture.md`](docs/architecture.md).
