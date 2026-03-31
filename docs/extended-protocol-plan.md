# Extended Query Protocol - Implementation Plan

## Background

The PostgreSQL wire protocol has two query execution modes:

1. **Simple Query Protocol** (currently implemented): Client sends a `Query` message containing raw SQL. Server responds with results and `ReadyForQuery`.

2. **Extended Query Protocol**: Splits query execution into discrete steps — Parse, Bind, Execute — allowing prepared statements, parameterized queries, and result format control. Most client libraries (libpq, pgx, JDBC, psycopg2) use this by default for parameterized queries.

Without extended query protocol support, many client libraries either fall back to simple query (losing parameterization) or fail entirely.

## Protocol Overview

The extended query protocol uses these client (frontend) messages:

| Message | Purpose |
|---------|---------|
| **Parse** | Prepare a statement: associates a query string (possibly with `$1`, `$2` placeholders) with a statement name. May include parameter type OIDs. |
| **Bind** | Bind parameter values to a prepared statement, producing a named portal. Specifies parameter format codes and result format codes. |
| **Describe** | Request metadata about a prepared statement (`'S'`) or portal (`'P'`). |
| **Execute** | Execute a portal, optionally limiting the number of rows returned. |
| **Close** | Close a prepared statement (`'S'`) or portal (`'P'`). |
| **Sync** | End of an extended query sequence. Server responds with `ReadyForQuery`. |
| **Flush** | Request the server to flush its output buffer (without ending the sequence). |

And these server (backend) responses:

| Message | Purpose |
|---------|---------|
| **ParseComplete** | Acknowledgement of a successful Parse. |
| **BindComplete** | Acknowledgement of a successful Bind. |
| **CloseComplete** | Acknowledgement of a successful Close. |
| **ParameterDescription** | Describes parameter types for a prepared statement (response to Describe 'S'). |
| **RowDescription** | Describes result columns (response to Describe 'S' or 'P'). |
| **NoData** | Indicates the statement produces no rows (response to Describe for non-SELECT). |
| **DataRow** | A row of data (during Execute). |
| **CommandComplete** | Statement execution complete (during Execute). |
| **ErrorResponse** | Error occurred; all remaining messages until Sync are skipped. |
| **ReadyForQuery** | Sent only in response to Sync. |

## Key Design Decisions

### How the LLM Fits In

The core challenge: we don't have a real SQL parser or query planner. The LLM is our "database engine." We use the LLM at two points in the extended query lifecycle:

1. **Describe time** — Ask the LLM to predict parameter types and/or result column types for a query, without producing row data. This metadata is cached and returned to the client.
2. **Execute time** — Send the full query with bound parameters (and cached column type info) to the LLM to get actual result rows.

This ensures clients get accurate type metadata from Describe, and the LLM's Execute response is constrained to match the shape we already promised the client.

### LLM Prompt Modes

We need four distinct prompt modes for the LLM user message, each requesting different information:

#### Mode 1: Simple Query (existing behavior)
- **Used by**: Simple query protocol (`handleQuery`)
- **Input**: Raw SQL query text
- **LLM returns**: Column definitions + row data + command tags
- **No changes needed** — this is what we do today.

#### Mode 2: Describe Prepared Statement
- **Used by**: Describe with ObjectType `'S'`
- **Input**: Query template (with `$1`, `$2` placeholders) + any parameter type OIDs from the Parse message (clients sometimes provide type hints)
- **LLM returns**: Parameter types (as `Column` structs — name like "$1", type name, length) + result column definitions (name, type, length). No row data. Parameter OIDs for `ParameterDescription` are derived via `typeNameToOID()` at response time.
- **Cached on**: `PreparedStatement`

#### Mode 3: Describe Portal
- **Used by**: Describe with ObjectType `'P'`
- **Input**: Query template + actual bound parameter values
- **LLM returns**: Result column definitions (name, type, length). No row data.
- **Cached on**: `Portal`
- **Optimization**: If the parent `PreparedStatement` already has cached column info (from a prior Describe statement call), return that directly without an LLM call. This prompt mode is only needed when a client goes Parse → Bind → Describe(P) without ever Describing the statement.
- **Note**: Result columns could theoretically differ based on bound parameter values (e.g. `SELECT $1` where the result type depends on the parameter type). In practice this is rare, and for an LLM-backed system the optimization of reusing the statement's cached columns is a reasonable tradeoff. The LLM may produce slightly different metadata when given concrete parameter values vs a template — if this causes issues, the optimization can be disabled.

#### Mode 4: Execute
- **Used by**: Execute message handler
- **Input**: Query template + bound parameter values + cached column type info (from a prior Describe call, if available)
- **LLM returns**: Row data + command tag only. No column definitions in the response.
- **Key property**: Cached column types are sent *to* the LLM in the prompt to constrain its output shape (e.g. "return rows with columns: id int4, name text"), but the response schema only contains rows and a command tag. The client already received column metadata from a prior Describe call. If no cached column info exists (client never called Describe), the prompt omits column constraints and the LLM returns rows freely.

### LLM Response Schemas

Each prompt mode asks the LLM for different information, so each has its own Go struct and JSON schema to constrain the response:

**Mode 1 — Simple Query** (existing `LLMResponse` — no rename needed):
```go
type LLMResponse struct {
    Results []ResultSet `json:"results"` // multiple result sets for multi-statement queries
}
```
No changes needed — this is the current behavior.

**Mode 2 — Describe Statement**:
```go
type DescribeStatementResponse struct {
    Parameters []Column `json:"parameters"` // parameter types ($1, $2, etc.)
    Columns    []Column `json:"columns"`    // result column definitions (empty for non-SELECT)
}
```
Returns only metadata — no rows, no command tag.

**Mode 3 — Describe Portal**:
```go
type DescribePortalResponse struct {
    Columns []Column `json:"columns"` // result column definitions (empty for non-SELECT)
}
```
Parameters are already bound, so only result columns are needed.

**Mode 4 — Execute**:
```go
type ExecuteResponse struct {
    Rows       [][]*string `json:"rows"`        // row data
    CommandTag string      `json:"command_tag"` // e.g. "SELECT 3"
}
```
A single result set (not an array). No `Columns` field — cached column types are sent *to* the LLM in the prompt to constrain its output shape, but the LLM does not return them. The client already received column metadata from a prior Describe call. If the query contains multiple statements, the LLM returns the result of the final statement only.

### Parameter Substitution Strategy

When Bind provides parameter values for a query like `SELECT * FROM users WHERE id = $1 AND name = $2`, we textually substitute the parameters into the query before sending it to the LLM:

```
SELECT * FROM users WHERE id = '42' AND name = 'Alice'
```

This is simple and works well because the LLM doesn't actually execute SQL — it just needs to understand the intent. We quote string values and leave NULLs as `NULL`.

The substitution handles:
- `$N` placeholders replaced with the corresponding parameter value
- NULL parameters become the literal `NULL` (unquoted)
- Non-NULL parameters are single-quoted (with internal single quotes escaped by doubling: `'` → `''`)
- Parameters in text format (format code 0) are used as-is; binary format (format code 1) is an error for now

### Error Handling in Extended Protocol

PostgreSQL's extended protocol has specific error semantics:
- If an error occurs during Parse/Bind/Execute, the server sends `ErrorResponse` and then **skips all subsequent messages until it receives a Sync**.
- Upon receiving Sync, the server sends `ReadyForQuery` (and the transaction is aborted if one was active).

We track an `errorState` flag per connection. When any handler encounters an error, it sends `ErrorResponse` and sets `errorState = true`. While `errorState` is set, all incoming messages are discarded until Sync is received.

## In-Memory State

### Prepared Statements Cache

```go
type PreparedStatement struct {
    Name          string     // empty string = unnamed statement
    Query         string     // original query with $1, $2 placeholders
    ParameterOIDs []uint32   // from Parse message (client-provided type hints)
    // Cached from Describe (statement) LLM call:
    ParamTypes    []Column   // parameter type info (name like "$1", type, length)
    ResultColumns []Column   // result column definitions
}
```

- Stored in a `map[string]*PreparedStatement` on the Connection struct.
- The unnamed statement (`""`) is special: it's implicitly closed whenever a new unnamed Parse is received.
- Named statements persist until explicitly closed or the connection ends.
- `ParamTypes` and `ResultColumns` start as nil (not yet described). On first Describe, both are populated from the LLM response and cached. A nil slice means "not yet described"; an empty slice means "described, but has no parameters / returns no columns" (e.g. a non-SELECT statement).

### Portals Cache

```go
type Portal struct {
    Name              string
    Statement         *PreparedStatement
    Parameters        [][]byte          // bound parameter values
    ParameterFormats  []int16           // format codes for parameters
    ResultFormats     []int16           // format codes for results
    MaterializedQuery string            // query with parameters substituted in
    // Cached from Describe (portal) LLM call or inherited from Statement:
    ResultColumns     []Column          // result column definitions
}
```

- Stored in a `map[string]*Portal` on the Connection struct.
- The unnamed portal (`""`) is implicitly closed whenever a new unnamed Bind is received, and also when any next Parse is received (per the protocol spec).
- Named portals persist until explicitly closed or the connection ends.
- `ResultColumns` starts as nil. Populated from the parent statement's cache at Bind time (if available), or from a portal-level Describe LLM call. Nil means "not yet described"; empty means "described, returns no columns."

### Connection-Level State

Add to the `Connection` struct:

```go
type Connection struct {
    // ... existing fields ...
    statements map[string]*PreparedStatement
    portals    map[string]*Portal
    errorState bool  // true = skip messages until Sync
}
```

## Message Handling

### Parse

1. If `errorState`, discard and return.
2. If the statement name is `""` (unnamed), delete any existing unnamed statement and its dependent unnamed portal from the maps.
3. If the statement name is non-empty and already exists in the cache, send `ErrorResponse` (SQLSTATE 42P05, `duplicate_prepared_statement`), set `errorState = true`, and return.
4. Store the prepared statement in the cache (with query and any client-provided parameter OIDs).
5. Send `ParseComplete`.

### Bind

1. If `errorState`, discard and return.
2. Look up the prepared statement by name. If not found, send `ErrorResponse`, set `errorState = true`, and return.
3. If the portal name is `""` (unnamed), delete any existing unnamed portal from the map.
4. Perform parameter substitution: replace `$1`, `$2`, ... in the query with the bound values.
5. Store the portal in the cache. If the parent statement has cached `ResultColumns`, copy them to the portal.
6. Send `BindComplete`.

### Describe

**For ObjectType `'S'` (prepared statement):**

1. If `errorState`, discard and return.
2. Look up the prepared statement. If not found, send `ErrorResponse`, set `errorState = true`, and return.
3. If `ParamTypes` is non-nil, the statement has already been described — use cached values. Otherwise, call the LLM with prompt mode 2 (query template + parameter OID hints) and cache the results on the statement. After the LLM call, `ParamTypes` and `ResultColumns` are set (possibly to empty slices for statements with no parameters or no result columns).
4. Send `ParameterDescription` with OIDs derived from `ParamTypes` via `typeNameToOID()`. If the client provided non-zero OIDs in Parse, use those directly instead of the LLM's response for the corresponding parameters.
5. If `ResultColumns` is non-empty, send `RowDescription` with format codes set to 0 (text) for all columns — since Bind has not yet been issued, the result format is not yet known. If empty (non-SELECT statement), send `NoData`.

**For ObjectType `'P'` (portal):**

1. If `errorState`, discard and return.
2. Look up the portal. If not found, send `ErrorResponse`, set `errorState = true`, and return.
3. If `ResultColumns` is non-nil, the portal has already been described (or inherited columns from the statement) — use cached values. Otherwise, call the LLM with prompt mode 3 (query + bound parameter values) and cache the results on the portal.
4. If `ResultColumns` is non-empty, send `RowDescription` with format codes set from the portal's `ResultFormats` (provided during Bind). If empty (non-SELECT statement), send `NoData`.

### Execute

1. If `errorState`, discard and return.
2. Look up the portal by name. If not found, send `ErrorResponse`, set `errorState = true`, and return.
3. Send the materialized query to the LLM with prompt mode 4 (query + parameters + cached column types if available).
4. Send results: `DataRow` for each row, then `CommandComplete`. Do NOT send `RowDescription` — the client gets column metadata from a prior Describe call, not from Execute.
5. Do NOT send `ReadyForQuery` — that only comes from Sync.

### Close

1. If `errorState`, discard and return.
2. If ObjectType is `'S'`, remove the statement from the cache (and any portals that reference it). If the statement does not exist, this is a no-op (not an error).
3. If ObjectType is `'P'`, remove the portal from the cache. If the portal does not exist, this is a no-op (not an error).
4. Send `CloseComplete` (always, regardless of whether the object existed).

### Sync

1. Clear `errorState`.
2. Send `ReadyForQuery{TxStatus: 'I'}`.
3. Call `backend.Flush()`.

**Flushing policy**: All other handlers (Parse, Bind, Describe, Execute, Close) queue messages via `backend.Send()` but do NOT flush. Messages are buffered and sent to the client only when Sync or Flush is received.

### Flush

1. Call `backend.Flush()` to send any buffered messages immediately. This is NOT skipped during `errorState` — it must still flush any buffered `ErrorResponse` to the client.
2. No response message is sent for Flush itself.

## Implementation Plan

### Phase 1: Core Infrastructure

**Files to modify:**
- `internal/connection.go` — Add statement/portal caches, error state, and new message type cases to the query loop.
- `internal/protocol.go` — Add parameter substitution helper.

**New file:**
- `internal/extended.go` — Handler functions for Parse, Bind, Describe, Execute, Close, Sync, Flush messages. Also contains `PreparedStatement` and `Portal` types.

**Steps:**
1. Define `PreparedStatement` and `Portal` types in extended.go.
2. Add `statements`, `portals`, and `errorState` fields to `Connection`. Initialize maps in `Handle()`.
3. Add `substituteParams(query string, params [][]byte, formatCodes []int16) (string, error)` to protocol.go.
4. Implement handler functions in extended.go.
5. Add cases for `*pgproto3.Parse`, `*pgproto3.Bind`, `*pgproto3.Describe`, `*pgproto3.Execute`, `*pgproto3.Close`, `*pgproto3.Sync`, `*pgproto3.Flush` to the type switch in `Connection.Handle()`.

### Phase 2: LLM Prompt Modes

**Files to modify:**
- `internal/llm.go` — Extend the `LLMClient` interface with new methods or add a prompt mode parameter.
- `internal/prompt.md` (or equivalent) — Add prompt templates for modes 2, 3, and 4.
- Provider implementations (openai, anthropic) — Implement the new prompt modes.

**Steps:**
1. Design the LLM interface extensions. Options:
   - Add separate methods: `DescribeStatement(ctx, query, paramOIDs)`, `DescribePortal(ctx, query, params)`, `Execute(ctx, query, params, cachedColumns)`
   - Or add a mode/options parameter to the existing `Query` method.
2. Implement prompt templates that instruct the LLM what to return for each mode.
3. Wire up the Describe and Execute handlers to use the appropriate prompt mode.

### Phase 3: Refactor Result Sending

The current `handleQuery` sends RowDescription + DataRows + CommandComplete + ReadyForQuery + flush. For extended query, Execute must only send DataRows + CommandComplete (no RowDescription, no ReadyForQuery, no flush). Extract two separate helpers:

```go
// sendSimpleQueryResults sends RowDescription, DataRow, and CommandComplete
// for each result set in a simple query response.
// Does NOT send ReadyForQuery or flush.
func sendSimpleQueryResults(ctx context.Context, backend *pgproto3.Backend, response *LLMResponse) error

// sendExecuteResults sends DataRow and CommandComplete for a single
// ExecuteResponse. Does NOT send RowDescription, ReadyForQuery, or flush.
func sendExecuteResults(ctx context.Context, backend *pgproto3.Backend, response *ExecuteResponse) error
```

`handleQuery` (simple query protocol) calls `sendSimpleQueryResults`, then sends `ReadyForQuery` and flushes. The extended query Execute handler calls `sendExecuteResults` and does not flush — flushing happens at Sync/Flush.

### Phase 4: Testing

Test with real client libraries to verify compatibility:
- **psql**: `PREPARE` / `EXECUTE` SQL statements (these go through simple query protocol, but good sanity check).
- **pgx (Go)**: Parameterized `conn.Query()` / `conn.QueryRow()` — uses extended protocol by default.
- **psycopg2 (Python)**: `cursor.execute("SELECT ... WHERE id = %s", (42,))` — uses extended protocol.
- **JDBC**: PreparedStatement with parameter binding.

## Client Compatibility Notes

- **psql**: Uses simple query protocol for interactive queries. Extended protocol is not used directly by psql.
- **libpq / pgx / JDBC / psycopg2**: Use extended query protocol for parameterized queries (`$1` placeholders). Non-parameterized queries may use either protocol depending on the driver and settings.
- **pgx (Go)**: Uses extended protocol by default for `conn.Query()`/`conn.QueryRow()` with arguments. Falls back to simple protocol with `conn.Exec()` for simple statements or when explicitly configured.
- **ORMs (GORM, SQLAlchemy, etc.)**: Always use parameterized queries via extended protocol.

## Open Questions

1. **Describe LLM prompt design**: The prompts for modes 2 and 3 need to be carefully designed so the LLM returns only metadata (not row data), and so the metadata is consistent with what the LLM will produce at Execute time. The prompts should instruct the LLM to default to `text` for ambiguous parameter types (matching PostgreSQL's behavior — e.g. `SELECT $1` with no type hints returns a `text` column). This may take some iteration.

## Future Follow-ups

1. **MaxRows / Portal Suspension**: The Execute message has a `MaxRows` field that limits the number of rows returned, with `PortalSuspended` sent when there are remaining rows. Most clients use MaxRows=0 (all rows). If needed, this could be implemented by caching the full LLM response on the Portal, tracking a cursor position, and resuming on subsequent Execute calls.

2. **Binary format parameters**: Some clients may send parameters in binary format. We error on binary format initially and can add support later if needed (would require understanding the binary encoding for each PostgreSQL type).

3. **Transaction state tracking**: The extended protocol's error handling assumes transaction state (`'I'` idle, `'T'` in transaction, `'E'` failed transaction). We currently always send `'I'`. If clients start using `BEGIN`/`COMMIT`, we may need to track this.
