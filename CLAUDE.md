# CLAUDE.md

## What This Is

A wire-compatible PostgreSQL server that routes all queries to an LLM instead of a real database. Clients connect with any Postgres client (psql, etc.) using the LLM provider as username, API key as password, and model name as database. Each connection maintains its own LLM chat session for cross-query consistency.

## Build & Run

```bash
go install ./...                    # build (binary: ghostgres)
ghostgres                    # run on default port 5432
ghostgres -port 5433 -log-level debug  # custom port + debug logging
go fmt ./...                        # always run after editing Go code
```

There are no tests yet.

## Connecting

```bash
PGPASSWORD="sk-..." psql -h localhost -U openai -d gpt-5.4
```

Username = LLM provider (currently only "openai"), password = API key, database = model name. Reasoning effort can be set via `PGOPTIONS="reasoning_effort=high"`.

## Design

See [docs/plan.md](docs/plan.md) for the detailed implementation plan, protocol specifics, and post-MVP roadmap.

## Architecture

```
Client (psql) → Postgres Wire Protocol → Server → OpenAI API (structured outputs) → LLM
```

All code lives in two packages:
- `cmd/ghostgres/main.go` — thin entry point: flag parsing, signal handling, graceful shutdown
- `internal/` — all server logic

### Request Flow

1. **server.go** — TCP listener, accepts connections, manages goroutine lifecycle via `sync.WaitGroup`
2. **connection.go** — per-connection handler: auth → startup → query loop. Creates an `LLMClient` per connection
3. **auth.go** — cleartext password auth (needed to get the raw API key), sends startup parameter messages
4. **query.go** — receives SQL, calls `LLMClient.Query()`, translates `LLMResponse` into pgproto3 wire messages (RowDescription, DataRow, CommandComplete)
5. **llm.go** — OpenAI client wrapper. Maintains chat history per connection. Uses structured outputs (JSON schema generated from `LLMResponse` via `github.com/google/jsonschema-go`). Auto-selects lowest reasoning effort per model family
6. **protocol.go** — helpers: `typeNameToOID` (via `pgtype.Map`), `buildRowDescription`, `buildDataRow`, `buildErrorResponse`
7. **logging.go** — logger is always passed via `context.Context`, never stored on structs. Use `LoggerFromContext(ctx)` / `ContextWithLogger(ctx, logger)`

### Key Patterns

- **Logger via context**: All functions receive `context.Context` and extract the logger. Fields are added with `.With()` and the enriched logger is put back into context.
- **Simple Query Protocol only**: No extended query protocol (no prepared statements/parameter binding).
- **Structured outputs**: LLM returns JSON matching `LLMResponse` struct (array of `ResultSet` with columns, rows as `[]*string` for null support, and command tags).
- **Model reasoning effort defaults**: `modelReasoningEffortPrefixes` in llm.go maps model prefixes to their lowest reasoning effort. Non-reasoning models get no effort set.

### Key Dependencies

- `github.com/jackc/pgx/v5/pgproto3` — Postgres wire protocol (Backend type for server-side)
- `github.com/jackc/pgx/v5/pgtype` — type name → OID lookups
- `github.com/openai/openai-go/v3` — OpenAI API client with structured outputs
- `github.com/google/jsonschema-go` — JSON schema generation from Go structs
