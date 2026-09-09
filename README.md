# mmmcp

<p align="center">
  <img src="assets/branding/mmmcp-icon.png" alt="mmmcp icon" width="240">
</p>

`mmmcp` (pronounced “mmm-c-p”) combines multiple MCP servers behind one MCP
endpoint. Components may be remote Streamable HTTP servers or local commands
that speak MCP over stdio. The root Go package is embeddable; `cmd/mmmcp` is the
production command.

## Configuration

Configuration is one strict YAML document. Unknown fields, missing referenced
environment variables, duplicate component names, invalid overrides, failed
component discovery, and final identity collisions stop startup.

```yaml
name: company-mcp
version: 1.0.0
listen: 127.0.0.1:8080
idleTimeout: 30s
servers:
  - name: github
    prefix: gh
    url: https://example.invalid/mcp
    headers:
      Authorization: Bearer ${GITHUB_TOKEN}
    passthroughHeaders:
      - X-Request-ID
      - X-Tenant
    tools:
      - name: search_code
        overrideName: search
        overrideDescription: Search configured repositories.
        enabled: true

  - name: local-files
    command: /usr/local/bin/files-mcp
    args: ["--root", "${PROJECT_ROOT}"]
    workingDirectory: ${PROJECT_ROOT}
    env:
      PROJECT_MODE: ${PROJECT_MODE}
    resources:
      - uri: file:///private-notes
        enabled: false
```

`${NAME}` interpolation is supported in component URLs and headers,
commands, arguments, working directories, and explicit environment values.
Missing variables expand to an empty string. `$$` emits a literal dollar sign.
The `name`, `version`, and `listen` fields, prefixes, override metadata, and
component names are not interpolated. When exactly one component is configured,
the frontend reports that component server's MCP name and version. With multiple
components, the optional top-level `name` and `version` are reported, defaulting
to `mmmcp` and `dev` when omitted.
For remote components, `passthroughHeaders` copies the named headers from each
incoming HTTP request to the downstream MCP request. Explicit values in
`headers` take precedence when both settings name the same header.

With one component and no explicit `prefix`, tool names, prompt names, resource
URIs, and resource templates are exposed without namespace wrapping (after any
configured overrides). With multiple components, tool and prompt identities
are exposed as `<prefix>__<local-name>`, while
resource URIs and templates use `mmmcp+<prefix>:<original-or-overridden-uri>`.
An explicit `prefix` is always honored; otherwise each component name is
sanitized deterministically when namespacing is required.
Overrides identify the original component-local feature, apply before the
namespace, and default to enabled. Only an explicit `enabled: false` hides a
feature. Supported MCP resource URI fields are rewritten on routed results;
arbitrary tool JSON is not inspected.

When a server has tool overrides, only tools with enabled overrides can be listed
or called. Without overrides, all discovered tools are available. Set the server's
`disableTools: true` to expose no tools, regardless of overrides. Prompts and
resources are unaffected.

## Running

Build and run the HTTP frontend:

```sh
go build -o mmmcp ./cmd/mmmcp
./mmmcp -config ./mmmcp.yaml -transport http
```

The HTTP frontend defaults to `127.0.0.1:8080` when neither YAML, environment,
nor flags provide an address. It serves MCP at `/`; a reverse proxy may mount
the handler at another path.

`GET /healthz` is a dependency-free liveness probe. `GET /readyz` checks the
default catalog and pings the default event store with a short timeout. A
failed catalog refresh reports `degraded` with HTTP 200 while the
last-known-good catalog remains usable; an unavailable catalog or store returns
HTTP 503. Both
endpoints support `HEAD`, disable caching, and expose stable reason codes rather
than dependency errors or configuration values. Request-scoped configurations
and DSNs are intentionally outside the global readiness check.

Run one persistent frontend session over stdin/stdout:

```sh
./mmmcp -config ./mmmcp.yaml -transport stdio
```

Flags take precedence over environment variables, which take precedence over
YAML values:

| Flag | Environment | Purpose |
|---|---|---|
| `-config` | `MMMCP_CONFIG` | YAML path; defaults to `mmmcp.yaml` |
| `-transport` | `MMMCP_TRANSPORT` | `http` or `stdio`; defaults to `http` |
| `-listen` | `MMMCP_LISTEN` | HTTP address override |
| `-dsn` | `MMMCP_DSN` | storage DSN; an explicit empty value selects default SQLite |

SIGINT and SIGTERM stop accepting HTTP traffic, drain the HTTP server, close
frontend and downstream MCP sessions, terminate owned command components, and
close database pools. Logs do not print configuration values or DSNs.

## Session Model

Current MCP `2026-07-28` HTTP requests are stateless. Every routed operation
opens a one-off downstream client session; command components therefore start
and stop one process per operation. Modern interactive exchanges retain MCP
multi-round-trip input requests, responses, and request state.

Legacy Streamable HTTP and stdio frontends are stateful. Each frontend session,
complete configuration fingerprint, and component receives an isolated
downstream session. Command processes start lazily, remain active while calls or
frontend streams are active, retire after `idleTimeout` (30 seconds by default),
and restart on the next operation. Sampling, elicitation, roots, progress,
logging, resource updates, and list changes stay bound to the originating
frontend session.

Command children never inherit the complete host environment. The baseline is
limited to `PATH`, `PWD`, `TMPDIR`, `TMP`, `TEMP`, `LANG`, `LC_ALL`, `LC_CTYPE`,
and `TZ`; Windows also includes `SystemRoot`, `WINDIR`, `ComSpec`, and `PATHEXT`.
Explicit component `env` entries are appended last and win on duplicate names.
There is no shell expansion or OS-level sandboxing.

## Library

```go
cfg, err := config.LoadFile("mmmcp.yaml", config.LoadOptions{LookupEnv: os.LookupEnv})
if err != nil {
    return err
}

composite, err := mmmcp.New(ctx, cfg, mmmcp.Options{Logger: slog.Default()})
if err != nil {
    return err
}
defer composite.Close()

http.Handle("/mcp", composite.HTTPHandler())
```

`ContextWithConfig` attaches a complete immutable configuration snapshot. It
replaces the default configuration for that request or stdio process session;
components, credentials, overrides, and timeouts are never merged. Storage is
selected independently with `Options.DSN` or `ContextWithDSN`.

```go
request = request.WithContext(mmmcp.ContextWithConfig(request.Context(), tenantConfig))
```

For tool-change notifications with the stateless protocol, also attach a stable
configuration ID to every request, including `subscriptions/listen`:

```go
ctx := mmmcp.ContextWithConfigID(request.Context(), serverID)
ctx = mmmcp.ContextWithConfig(ctx, tenantConfig)
request = request.WithContext(ctx)
```

Choose the ID in trusted application middleware. All requests sharing an ID must
see the same configuration; include the tenant or user ID when their tool views
differ. Keep the ID unchanged when editing overrides. The ID is scoped to one
`Composite` instance and is not an MCP session ID or a client-supplied header.

While listeners are connected, the next request that observes changed tools
notifies all tool-change subscribers for that ID, even when it is `tools/list`.
Unrelated configuration changes do not notify. Tracking is removed when the last
listener disconnects. Without a nonempty ID, stateless requests do not share
change tracking. Legacy sessions still track changes individually and suppress
the notification when the triggering request is `tools/list`.

Use `Composite.RunStdio(ctx)` to serve stdin/stdout. A configuration attached to
that context applies to the complete stdio frontend session.

## Storage

Set storage with `Options.DSN` (or `-dsn`/`MMMCP_DSN` in the CLI). An empty DSN
selects a local SQLite database. PostgreSQL URLs and keyword DSNs,
MySQL driver DSNs and `mysql://` URLs, SQLite `file:` and `sqlite://` URLs, and
plain SQLite paths are supported. The selected database is opened, pinged, and
migrated before serving. Stateful SSE event streams remain bound to the store
on which they opened even if a later request selects another configuration.

Examples:

```text
postgres://user:password@db.example/mmmcp?sslmode=require
host=db.example dbname=mmmcp user=mmmcp sslmode=require
user:password@tcp(db.example:3306)/mmmcp?parseTime=true
mysql://user:password@db.example:3306/mmmcp?parseTime=true
file:/var/lib/mmmcp/mmmcp.db
./mmmcp.db
```

## Container

```sh
docker build -t mmmcp .
docker run --rm -p 127.0.0.1:8080:8080 \
  -v "$PWD/mmmcp.yaml:/etc/mmmcp/config.yaml:ro" \
  -v mmmcp-data:/var/lib/mmmcp \
  mmmcp -config /etc/mmmcp/config.yaml -listen 0.0.0.0:8080
```

The runtime user is non-root. `/var/lib/mmmcp` is the writable persistence
volume and contains the default `mmmcp.db`. The image contains CA certificates
and the `mmmcp` binary only in addition to the minimal Alpine runtime. Build a
derivative image when command-backed MCP component binaries are needed:

```dockerfile
FROM ghcr.io/obot-platform/mmmcp:latest
COPY --chown=mmmcp:mmmcp files-mcp /usr/local/bin/files-mcp
```

Tagged releases publish Linux, macOS, and Windows archives, checksums, and a
Linux amd64/arm64 image at `ghcr.io/obot-platform/mmmcp`.

## Current Limitations
- `mmmcp` doesn't properly propogate OAuth
- Windows support is untested
