# Agentic Postgres - Implementation Plan

## Project Overview

A wire-compatible Postgres server that sends all queries to an LLM (OpenAI)
instead of executing them against a real database. Each connection maintains its
own LLM context for consistency across queries.

## Architecture

```
Client (psql, etc.)
    ↓ Postgres Wire Protocol
Server (agentic-postgres)
    ↓ OpenAI API (structured outputs)
LLM (GPT-4, etc.)
```

## Components

### 1. Main Entry Point (`cmd/agentic-postgres/main.go`)
- **Very thin wrapper** - delegates all logic to internal package
- Parse command-line flags (host, port, log level, shutdown timeout)
- Set up context with cancellation for graceful shutdown
- Initialize logger (slog.Logger)
- Add logger to context: `ctx = internal.ContextWithLogger(ctx, logger)`
- Create `internal.Server` via `internal.NewServer(host, port)`
  - Does NOT pass logger to constructor - logger comes from context
- Start server in goroutine: `go server.Start(ctx)`
  - Start() runs accept loop and blocks until ctx cancelled
- Wait for shutdown signal (SIGINT, SIGTERM) via signal.Notify
- Cancel context to stop accepting new connections (Server.Start() will return)
- Call `server.Close()` to terminate all active connections and wait for cleanup
  - Close() cancels all connection contexts and waits for them to finish
- Log shutdown complete and exit

### 2. Server (`internal/server.go`)
- Server struct holds: host, port, wg sync.WaitGroup, connCtx context.Context, connCancel context.CancelFunc
  - Does NOT store listener or logger (listener is local to Start, logger from context)
- `NewServer(host string, port int)` constructor
  - Does NOT take logger - logger comes from context
- `Start(ctx context.Context)` method:
  - Extract logger from context: `logger := LoggerFromContext(ctx)`
  - Create child context for all connections: `s.connCtx, s.connCancel = context.WithCancel(ctx)`
  - Create TCP listener on specified host:port (e.g., net.Listen("tcp", fmt.Sprintf("%s:%d", host, port)))
  - Log listening address
  - Defer listener.Close() for cleanup
  - Accept incoming connections in a loop:
    - Check if ctx is cancelled, if so break loop
    - Accept connection (with timeout check)
    - For each connection:
      - Create child logger with remote address field:
        ```go
        connCtx := ContextWithLogger(s.connCtx, logger.With("remote_addr", conn.RemoteAddr().String()))
        ```
      - Use `s.wg.Go()` to spawn goroutine and automatically track with WaitGroup:
        ```go
        s.wg.Go(func() {
            conn := NewConnection(acceptedConn)
            if err := conn.Handle(connCtx); err != nil {
                LoggerFromContext(connCtx).Error("connection error", "error", err)
            }
        })
        ```
      - WaitGroup automatically incremented/decremented by `Go()` method
  - When ctx cancelled, stop accepting new connections (break loop)
  - Return from Start()
- `Close()` method:
  - Cancel all connection contexts: `s.connCancel()`
  - Wait for all connections to finish: `s.wg.Wait()`
  - Connections will detect cancellation and terminate gracefully with ErrorResponse

### 3. Connection Handler (`internal/connection.go`)
- `Connection` struct holds: net.Conn, *pgproto3.Backend, *LLMClient
  - Does NOT store logger or context - both come via function parameters
- `NewConnection(conn net.Conn)` constructor
  - Creates pgproto3.Backend: `pgproto3.NewBackend(conn, conn)`
  - Stores net.Conn and backend on struct
  - Returns the Connection (caller is responsible for invoking Handle)
- `Handle(ctx context.Context)` method (main connection loop):
  - Defer cleanup: close connection, log disconnection
  - Defer sending termination message if context cancelled during operation
  - Calls authentication flow functions (passing ctx)
  - Uses pgproto3.Backend to receive/send messages (no manual protocol handling)
  - Validates username is "openai"
  - Extracts database name to determine LLM model (default: gpt-4o-2024-08-06 if empty)
  - Adds connection-specific fields to logger:
    ```go
    logger := LoggerFromContext(ctx).With("username", username, "database", database, "model", model)
    ctx = ContextWithLogger(ctx, logger)
    logger.Info("connection authenticated")
    ```
  - Creates per-connection LLM client with API key from password
  - Enters query loop:
    - Check `ctx.Done()` before each receive - if cancelled, send ErrorResponse and exit:
      ```go
      select {
      case <-ctx.Done():
          backend.Send(&pgproto3.ErrorResponse{
              Severity: "FATAL",
              Code:     "57P01",  // admin_shutdown
              Message:  "server shutting down",
          })
          backend.Flush()
          return nil
      default:
      }
      ```
    - `msg, err := backend.Receive()`
    - Type switch on message type (Query, Terminate, etc.)
    - For Query messages:
      ```go
      queryCtx := ContextWithLogger(ctx, LoggerFromContext(ctx).With("query", queryString))
      handleQuery(queryCtx, backend, llmClient, queryString)
      ```
  - Handles graceful shutdown (respects context cancellation in receive loop)
  - Cleans up resources on exit (close connection, cleanup LLM client)
  - **Note**: `backend.Receive()` is a blocking call and won't immediately respond to context cancellation. For MVP, we check context before calling Receive(). Future enhancement: consider setting connection deadlines for more responsive cancellation.

### 4. Authentication (`internal/auth.go`)
- **Note on Cleartext Password**: We use cleartext password authentication because we need the actual OpenAI API key to make LLM requests. Hash-based methods (SCRAM-SHA-256, MD5) cannot be used since they don't provide the plaintext password. Users should connect over localhost or use SSH tunneling for security.
- Helper functions called by Connection.Handle()
- `authenticate(ctx context.Context, backend *pgproto3.Backend) (username, password, database string, error)`:
  - Extract logger from ctx for logging
  - `msg, err := backend.ReceiveStartupMessage()` - Get StartupMessage
  - Type assert to `*pgproto3.StartupMessage`
  - Extract username and database from `msg.Parameters` map
  - Send auth request: `backend.Send(&pgproto3.AuthenticationCleartextPassword{})`
  - `backend.Flush()`
  - `msg, err = backend.Receive()` - Get password
  - Type assert to `*pgproto3.PasswordMessage`
  - Validate username == "openai" (return error if not)
  - Log authentication attempt
  - Return username, password, database
- `sendStartupMessages(ctx context.Context, backend *pgproto3.Backend) error`:
  - Extract logger from ctx for logging
  - Send multiple messages using pgproto3 types:
    - `&pgproto3.AuthenticationOk{}`
    - `&pgproto3.BackendKeyData{ProcessID: rand.Int32(), SecretKey: rand.Int32()}`
    - `&pgproto3.ParameterStatus{}` for each parameter
    - `&pgproto3.ReadyForQuery{TxStatus: 'I'}`
  - Call `backend.Flush()` once at end
  - Log startup sequence completion

### 5. Query Handler (`internal/query.go`)
- Helper function called by Connection.Handle() when Query message received
- `handleQuery(ctx context.Context, backend *pgproto3.Backend, llmClient *LLMClient, queryString string) error`:
  - Extract logger from ctx for logging
  - Query string already extracted from `*pgproto3.Query` message
  - Log query received
  - Call LLM via `llmClient.Query(ctx, queryString)` → returns `*LLMResponse`
    - If context cancelled during LLM call, return error immediately
  - Log LLM response received
  - For each result set in response:
    - **If columns non-empty**, build and send RowDescription:
      ```go
      fields := make([]pgproto3.FieldDescription, len(resultSet.Columns))
      for i, col := range resultSet.Columns {
          fields[i] = pgproto3.FieldDescription{
              Name:                 []byte(col.Name),
              TableOID:             0,
              TableAttributeNumber: 0,
              DataTypeOID:          typeNameToOID(col.Type),
              DataTypeSize:         col.Length,
              TypeModifier:         -1,
              Format:               0,  // text format
          }
      }
      backend.Send(&pgproto3.RowDescription{Fields: fields})
      ```
    - **For each row**, build and send DataRow:
      ```go
      values := make([][]byte, len(row))
      for i, val := range row {
          if val == nil {
              values[i] = nil  // NULL
          } else {
              values[i] = []byte(val.(string))
          }
      }
      backend.Send(&pgproto3.DataRow{Values: values})
      ```
    - **Send CommandComplete**:
      ```go
      backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(resultSet.CommandTag)})
      ```
  - **After all result sets**:
    ```go
    backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
    backend.Flush()
    ```
  - **On error**: send ErrorResponse, then ReadyForQuery
  - LLMClient automatically maintains chat history internally

### 6. LLM Integration (`internal/llm.go`)
- `LLMClient` struct holds: OpenAI client, model name, chat history slice
- `NewLLMClient(apiKey string, model string) *LLMClient`:
  - Creates OpenAI client with API key
  - Initializes chat history with system prompt as first message
  - Stores model name
- `Query(ctx context.Context, queryString string) (*LLMResponse, error)`:
  - Extract logger from ctx for logging
  - Appends user query to chat history
  - Log LLM API call start
  - Calls OpenAI Chat Completions API with:
    - Context for cancellation/timeout
    - Messages: full chat history
    - Model: stored model name
    - ResponseFormat with structured output JSON schema (see Schema Generation below)
  - Log LLM API call completion (with duration, token count if available)
  - Parses response into LLMResponse struct
  - Appends assistant response to chat history
  - Returns parsed response or error
- **Schema Generation**: Use `github.com/google/jsonschema-go/jsonschema` to generate JSON schema from Go structs:
  ```go
  import "github.com/google/jsonschema-go/jsonschema"

  // Generate schema from LLMResponse struct using jsonschema.For[T]()
  schema := jsonschema.For[LLMResponse]()

  // Use schema in OpenAI API call:
  chat, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
      ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
          OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
              JSONSchema: schema, // Generated schema
          },
      },
      Model: model,
  })
  ```
  - The `jsonschema.For[T]()` function uses reflection to generate a JSON schema from the LLMResponse struct
  - Supports standard JSON tags and additional schema annotations
  - Automatically generates proper schema for nested structs (ResultSet, Column)
- Structured output JSON schema definition:
  ```json
  {
    "results": [
      {
        "columns": [{"name": "string", "type": "string", "length": "integer"}],
        "rows": [["mixed types, nulls allowed"]],
        "command_tag": "string"
      }
    ]
  }
  ```
- Handle API errors gracefully (network, rate limits, invalid responses)
- Log all errors before returning

### 7. Protocol Helpers (`internal/protocol.go`)
- Package-level `pgtype.Map` instance created with `pgtype.NewMap()`
- `typeNameToOID(typeName string) uint32` - Uses `typeMap.TypeForName()` to get OID
- Helper functions to build pgproto3 messages (if needed for code clarity):
  - `buildRowDescription(columns []Column) *pgproto3.RowDescription`
  - `buildDataRow(row []any) *pgproto3.DataRow`
  - `buildErrorResponse(severity, code, message string) *pgproto3.ErrorResponse`
- **Note**: Most message construction happens inline using pgproto3 types directly

### 8. Logging (`internal/logging.go`)
- **CRITICAL PATTERN**: Logger is ALWAYS passed via context, NEVER stored on structs
- Context key and helper functions:
  - `type loggerKey struct{}` - unexported key type for context
  - `ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context` - adds logger to context
  - `LoggerFromContext(ctx context.Context) *slog.Logger` - extracts logger from context
- **ALL** internal functions receive context and extract logger via `LoggerFromContext(ctx)`
- **NO** structs should store logger fields
- Pattern for adding fields to logger:
  ```go
  // Add fields and put back in context
  logger := LoggerFromContext(ctx).With("username", username, "database", database)
  ctx = ContextWithLogger(ctx, logger)

  // Now use logger and/or pass ctx to other functions
  logger.Info("authenticated")
  handleQuery(ctx, ...)
  ```
- Example usage in Connection.Handle():
  ```go
  // After authentication:
  logger := LoggerFromContext(ctx).With("username", username, "database", database, "model", model)
  ctx = ContextWithLogger(ctx, logger)

  // In query loop:
  queryCtx := ContextWithLogger(ctx, LoggerFromContext(ctx).With("query", queryString))
  handleQuery(queryCtx, backend, llmClient, queryString)
  ```
- All log events use structured fields (not string concatenation)
- Logger lifecycle:
  1. Created in main.go
  2. Added to root context via ContextWithLogger
  3. Functions extract with LoggerFromContext, add fields with .With(), put back with ContextWithLogger
  4. Each function extracts logger from its context parameter

## Using pgproto3 for Wire Protocol

We use the **pgproto3** library to handle all low-level wire protocol details. We don't implement message encoding/decoding ourselves.

**Note**: For MVP, we only implement the **Simple Query Protocol**. The **Extended Query Protocol** (prepared statements, parameter binding, etc.) is a post-MVP enhancement.

### Backend Type Usage

Create a Backend instance to handle server-side protocol:
```go
backend := pgproto3.NewBackend(conn, conn)  // conn is net.Conn
```

**Key Methods:**
- `backend.ReceiveStartupMessage()` - Receive initial connection message
- `backend.Receive()` - Receive any message from client
- `backend.Send(msg)` - Queue a message to send to client
- `backend.Flush()` - Actually send all queued messages

### Startup Sequence with pgproto3

1. `msg, err := backend.ReceiveStartupMessage()`
   - Returns `*pgproto3.StartupMessage` with `.Parameters` map
   - Extract `user` and `database` from parameters
2. `backend.Send(&pgproto3.AuthenticationCleartextPassword{})`
3. `backend.Flush()`
4. `msg, err := backend.Receive()` - Get PasswordMessage
   - Type assert: `passwordMsg := msg.(*pgproto3.PasswordMessage)`
   - Extract password: `passwordMsg.Password`
5. Send success messages:
   ```go
   backend.Send(&pgproto3.AuthenticationOk{})
   backend.Send(&pgproto3.BackendKeyData{ProcessID: rand.Int32(), SecretKey: rand.Int32()})
   backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0 (agentic-postgres)"})
   backend.Send(&pgproto3.ParameterStatus{Name: "server_encoding", Value: "UTF8"})
   backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
   backend.Send(&pgproto3.ParameterStatus{Name: "DateStyle", Value: "ISO, MDY"})
   backend.Send(&pgproto3.ParameterStatus{Name: "TimeZone", Value: "UTC"})
   backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})  // 'I' = idle
   backend.Flush()
   ```

### Query Loop with pgproto3

1. `msg, err := backend.Receive()`
2. Type switch on message:
   ```go
   switch msg := msg.(type) {
   case *pgproto3.Query:
       queryString := msg.String
       // Process with LLM, send responses
   case *pgproto3.Terminate:
       // Client disconnecting
       return
   }
   ```

### Sending Query Results with pgproto3

For each result set from LLM:

1. **If columns exist (SELECT-like):**
   ```go
   fields := make([]pgproto3.FieldDescription, len(columns))
   for i, col := range columns {
       fields[i] = pgproto3.FieldDescription{
           Name:                 []byte(col.Name),
           TableOID:             0,
           TableAttributeNumber: 0,
           DataTypeOID:          typeNameToOID(col.Type),  // Use pgtype constants
           DataTypeSize:         col.Length,
           TypeModifier:         -1,
           Format:               0,  // 0 = text format
       }
   }
   backend.Send(&pgproto3.RowDescription{Fields: fields})
   ```

2. **For each row:**
   ```go
   values := make([][]byte, len(row))
   for i, val := range row {
       if val == nil {
           values[i] = nil  // NULL
       } else {
           values[i] = []byte(val.(string))  // Convert to bytes
       }
   }
   backend.Send(&pgproto3.DataRow{Values: values})
   ```

3. **Command complete:**
   ```go
   backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(resultSet.CommandTag)})
   ```

4. **After all result sets:**
   ```go
   backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
   backend.Flush()
   ```

### Error Responses with pgproto3

```go
backend.Send(&pgproto3.ErrorResponse{
    Severity: "ERROR",
    Code:     "XX000",  // SQLSTATE code
    Message:  "LLM API error",
    Detail:   err.Error(),
})
backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
backend.Flush()
```

## Using pgtype for Type Lookups

We use **pgtype.Map** to look up PostgreSQL types by name and get their OIDs:

```go
typeMap := pgtype.NewMap()  // Pre-populated with all standard PostgreSQL types
typ, ok := typeMap.TypeForName("int4")
if ok {
    oid := typ.OID  // Returns 23 (INT4OID)
}
```

This handles all standard type names and aliases automatically. We don't need pgtype's encoding/decoding features since we work with text format only and the LLM gives us strings.

## LLM Integration Strategy

### System Prompt
```
You are a PostgreSQL database that responds to SQL queries. For each query,
return results that are consistent with your previous responses in this session.
Remember what data you've returned before and maintain consistency.

Return your response as structured JSON with a "results" array. Each element represents one
SQL statement's result and contains:
- columns: array of {name, type, length} where type is a PostgreSQL type name and length is -1 for variable-length types
- rows: array of arrays where each value is a string in PostgreSQL text format, or null for NULL values
- command_tag: string like "SELECT 3", "INSERT 0 1", "UPDATE 2", "DELETE 1", "CREATE TABLE", etc.

IMPORTANT: All non-NULL values in rows must be strings, even for numbers. Use PostgreSQL text format.
For example: integer 42 becomes "42", boolean true becomes "t", NULL becomes null (JSON null).

For column types, prefer variable-length types (text, varchar) over fixed-length types when appropriate.
For variable-length types, always use length = -1. For fixed-length types like int4, use the standard size (e.g., 4 for int4).

For multi-statement queries (separated by semicolons), return multiple result sets in the array.

Supported types: text, int4, int8, float8, float4, int2, bool, timestamp, date, uuid, varchar
For NULL values, use JSON null (not the string "NULL").
For non-SELECT queries (INSERT, UPDATE, DELETE, CREATE, etc.), return empty columns and rows arrays,
but always provide an appropriate command_tag.

Example for "SELECT 1; INSERT INTO foo VALUES (1); SELECT 2;":
{
  "results": [
    {"columns": [{"name": "?column?", "type": "int4", "length": 4}], "rows": [["1"]], "command_tag": "SELECT 1"},
    {"columns": [], "rows": [], "command_tag": "INSERT 0 1"},
    {"columns": [{"name": "?column?", "type": "int4", "length": 4}], "rows": [["2"]], "command_tag": "SELECT 1"}
  ]
}
```

### Chat History Management
- Store messages in a slice per connection
- Include system prompt as first message
- Append user queries and assistant responses
- Consider truncating old history if it gets too long (future enhancement)

### Structured Output Schema
```go
type LLMResponse struct {
    Results []ResultSet `json:"results"` // Array of result sets (one per statement)
}

type ResultSet struct {
    Columns    []Column `json:"columns"`     // Column definitions (empty for non-SELECT)
    Rows       [][]any  `json:"rows"`        // Row data: any allows string or JSON null for NULL values
    CommandTag string   `json:"command_tag"` // e.g., "SELECT 3", "INSERT 0 1"
}

type Column struct {
    Name   string `json:"name"`
    Type   string `json:"type"`
    Length int16  `json:"length"` // -1 for variable length types, matches pgproto3.FieldDescription.DataTypeSize
}
```

### Type Mapping (LLM type name → Postgres OID)

Use `pgtype.Map` to look up types by name:

```go
import "github.com/jackc/pgx/v5/pgtype"

// Create once, reuse for all lookups (store on Connection or as package var)
var typeMap = pgtype.NewMap()

func typeNameToOID(typeName string) uint32 {
    typ, ok := typeMap.TypeForName(typeName)
    if !ok {
        // Fallback to text for unknown types
        typ, _ = typeMap.TypeForName("text")
    }
    return typ.OID
}
```

**Benefits:**
- Leverages pgtype's built-in knowledge of all PostgreSQL types
- Automatically handles type aliases (int, integer, int4 all work)
- No manual switch statement to maintain
- If LLM uses standard Postgres type names, they just work

**Note:** We only use pgtype for type lookups. We don't use its encoding/decoding features since we work with text format only (format code 0) and the LLM provides string values.

## Error Handling

### Authentication Errors
- Wrong username: Send ErrorResponse with SQLSTATE "28000" (invalid authorization), then close connection
- Missing password: Send ErrorResponse with SQLSTATE "28P01" (invalid password), then close connection
- Invalid API key: Connection will fail on first LLM query, send ErrorResponse with SQLSTATE "XX000"
- Auth errors during startup cause immediate connection closure (no ReadyForQuery sent)

### Query Errors
- OpenAI API error: Send ErrorResponse with SQLSTATE "XX000" (internal error)
- Invalid JSON from LLM: Send ErrorResponse with SQLSTATE "XX000"
- Network timeout: Send ErrorResponse with SQLSTATE "57014" (query canceled)
- Always send ReadyForQuery after error to keep connection alive

### Connection Errors
- Log error and close connection cleanly
- Ensure goroutine cleanup

## Graceful Shutdown

1. Signal handler (in main.go) catches SIGINT/SIGTERM via signal.Notify
2. Signal handler cancels root context passed to Server.Start()
3. Server.Start() breaks accept loop when context cancelled, returns
4. Main calls `server.Close()`:
   - Server cancels all connection contexts (via `s.connCancel()`)
   - Waits for all connections to finish (via `s.wg.Wait()`)
5. Each Connection.Handle() detects context cancellation:
   - Sends ErrorResponse with SQLSTATE "57P01" (admin_shutdown)
   - Message: "server shutting down"
   - Closes connection cleanly
6. All connections finish quickly (not waiting for client input since context cancelled)
7. Log shutdown complete after all connections finished

**Key Point**: Server.Close() actively terminates connections, doesn't just wait passively. Connections are cancelled and send proper Postgres shutdown messages.

## Configuration

Command-line flags:
- `--host` / `-h`: Hostname/interface to bind to (default: "" - bind to all interfaces)
- `--port` / `-p`: Port to listen on (default: 5432)
- `--log-level`: Log level (debug, info, warn, error) (default: info)

Model selection:
- Specified via database name in connection string (e.g., `-d gpt-4o`)
- If database name is empty or whitespace, use default: `gpt-4o-2024-08-06`
- Database name is used exactly as provided (e.g., `gpt-4o-mini`, `gpt-4`, etc.)
- Must be a model that supports structured outputs (OpenAI will error otherwise)
- Example connections:
  - `psql -h localhost -U openai -d gpt-4o` → uses gpt-4o model
  - `psql -h localhost -U openai -d gpt-4o-mini` → uses gpt-4o-mini model
  - `psql -h localhost -U openai` → uses default gpt-4o-2024-08-06

## Testing Strategy

### Manual Testing
Use `psql` to connect:
```bash
# Use database name to specify the OpenAI model
PGPASSWORD="sk-..." psql -h localhost -p 5432 -U openai -d gpt-4o

# Or use a different model
PGPASSWORD="sk-..." psql -h localhost -p 5432 -U openai -d gpt-4o-mini
```

Test queries:
```sql
SELECT * FROM users WHERE id = 1;
SELECT name, email FROM customers LIMIT 10;
INSERT INTO logs VALUES (1, 'test');
CREATE TABLE foo (id int);
```

### Automated Testing (Future)
- Unit tests for protocol helpers
- Integration tests with mock LLM responses
- Connection lifecycle tests

## Implementation Phases

### Phase 1: Basic Server & Protocol
1. Create project structure (cmd/, internal/ directories)
2. Initialize go.mod and add dependencies (pgx/v5, openai-go)
3. Implement logging helpers (logging.go with context-based logger)
4. Implement Server (TCP listener, connection management, WaitGroup)
5. Implement Connection struct and Handle() method skeleton with pgproto3.Backend
6. Implement authentication flow using pgproto3 message types (auth.go)
7. Implement main.go (thin wrapper with signal handling)
8. Test with `psql` connection (should successfully authenticate and see ReadyForQuery)

### Phase 2: Simple Query Protocol
1. Implement query loop in Connection.Handle() to receive Query messages
2. Implement protocol helpers (typeNameToOID using pgtype.Map.TypeForName)
3. Create hardcoded LLMResponse for testing
4. Implement handleQuery() to send RowDescription, DataRow, CommandComplete using pgproto3
5. Test with `psql` queries (should see hardcoded results)

### Phase 3: LLM Integration
1. Implement OpenAI client with structured outputs
2. Build system prompt and chat history
3. Connect query handler to LLM
4. Parse LLM response and create wire protocol messages
5. Test end-to-end

### Phase 4: Error Handling & Refinement
1. Add comprehensive error handling
2. Implement graceful shutdown
3. Add logging throughout
4. Test error scenarios
5. Polish and documentation

## Open Questions

1. **Type Inference**: ✅ RESOLVED
   - Let LLM infer types, protocol.go validates and maps to supported OIDs
   - Unsupported types fall back to text (OID 25)

2. **NULL Handling**: ✅ RESOLVED
   - LLM returns JSON null, we convert to NULL in DataRow (length = -1)

3. **CommandComplete Tag**: ✅ RESOLVED
   - LLM includes "command_tag" field in structured output
   - Schema: `{columns, rows, command_tag}`

4. **Transaction Statements**: ✅ RESOLVED
   - LLM acknowledges them with appropriate CommandComplete tags
   - We don't maintain real transaction state (no rollback capability)
   - System prompt should inform LLM to treat these as no-ops that return success

5. **Multi-statement Queries**: ✅ RESOLVED
   - Structured output is now an array of result sets
   - Each result set corresponds to one SQL statement
   - Server sends wire protocol messages for each result set in sequence

6. **Chat History Limits**: ✅ RESOLVED
   - Keep unlimited history for MVP (LLM context window is large enough)
   - Future enhancement: Add truncation if context window issues arise
   - Future enhancement: Add --max-history-messages flag

7. **Table OID**: ✅ RESOLVED
   - Use 0 for table OID (indicates not a real table column)

8. **Column Attribute Number**: ✅ RESOLVED
   - Use 0 for attribute number (not a real table column)

9. **Type Modifier**: ✅ RESOLVED
   - Use -1 for type modifier (no modifier) for MVP

## Files Structure

```
agentic-postgres/
├── cmd/
│   └── agentic-postgres
│       └── main.go                # Main entry point (thin wrapper)
├── internal/
│   ├── server.go                  # Server: TCP listener, connection management, WaitGroup
│   ├── connection.go              # Connection: per-connection logic, LLM context
│   ├── auth.go                    # Authentication flow
│   ├── query.go                   # Query handling
│   ├── llm.go                     # OpenAI integration
│   ├── protocol.go                # Protocol helpers and constants
│   └── logging.go                 # Logging utilities
├── go.mod
├── go.sum
├── README.md
└── plan.md                        # This file
```

## Next Steps

After approval:
1. Initialize Go module
2. Add dependencies
3. Implement Phase 1 (server & auth)
4. Test authentication with psql
5. Continue through remaining phases

---

## Post-MVP Follow-up Tasks

These enhancements can be added after the MVP is working:

### Protocol Enhancements
1. **Extended Query Protocol**: Support for prepared statements, parameter binding, and binary format
   - Requires handling Parse, Bind, Execute, Describe messages
   - Would enable parameterized queries and potentially better performance
   - Allows clients to use features like `$1`, `$2` placeholders

### Performance & Scalability
2. **Chat History Truncation**: Add `--max-history-messages` flag to limit context window usage
   - Truncate old messages when limit reached
   - Keep system prompt and recent N messages
   - Prevents unbounded memory growth and context window overflow

3. **Connection Deadline Handling**: Set connection read deadlines for more responsive cancellation
   - Use `conn.SetReadDeadline()` before `backend.Receive()`
   - Allows breaking out of blocking receive calls during shutdown
   - Improves graceful shutdown responsiveness

### Security & Operations
4. **TLS Support**: Add optional TLS/SSL for encrypted connections
   - Command-line flags: `--tls-cert`, `--tls-key`
   - More secure than cleartext over network
   - Still need cleartext password internally for API key

5. **Rate Limiting**: Add optional rate limiting for OpenAI API calls
   - Prevent cost overruns
   - Per-connection or global limits
   - Configurable via flags

6. **Metrics & Monitoring**: Export metrics for observability
   - Connection count, query latency, LLM token usage
   - Integration with Prometheus or similar
   - Cost tracking for OpenAI API usage

### Features
7. **Multi-Provider Support**: Support other LLM providers beyond OpenAI
   - Anthropic Claude, Google Gemini, local models
   - Abstract LLM interface
   - Provider selection via database name prefix (e.g., `claude:opus`, `openai:gpt-4o`)

8. **Result Caching**: Cache LLM responses for identical queries
   - Reduces cost and latency
   - Configurable TTL
   - Consider cache invalidation strategy

---

## Dependencies

### External Libraries

**Postgres Wire Protocol & Types**
- `github.com/jackc/pgx/v5` - PostgreSQL driver and toolkit
  - **`pgproto3` package**: Low-level Postgres wire protocol v3 implementation
    - Provides `Backend` type for server-side protocol handling
    - Message types: Query, RowDescription, DataRow, CommandComplete, ErrorResponse, etc.
    - Handles all message encoding/decoding automatically
    - Documentation: https://pkg.go.dev/github.com/jackc/pgx/v5/pgproto3
  - **`pgtype` package**: PostgreSQL type mapping and data type support
    - Provides `Map` type with `TypeForName()` method for type lookups
    - Pre-populated with all standard PostgreSQL types and their OIDs
    - We use it for type name → OID lookups (not encoding/decoding)
    - Documentation: https://pkg.go.dev/github.com/jackc/pgx/v5/pgtype
  - Latest version: v5.7.2+

**OpenAI API Client**
- `github.com/openai/openai-go/v3` - Official OpenAI Go SDK
  - Supports structured outputs (required for our use case)
  - Latest version: v3.0.0+
  - Documentation: https://pkg.go.dev/github.com/openai/openai-go/v3
  - API Reference: https://platform.openai.com/docs/api-reference

**JSON Schema Generation**
- `github.com/google/jsonschema-go/jsonschema` - JSON Schema generator for Go types
  - Generates JSON Schema from Go structs using reflection
  - Used to create structured output schema for OpenAI API
  - Key function: `jsonschema.For[T]()` generates schema for type T
  - Documentation: https://pkg.go.dev/github.com/google/jsonschema-go/jsonschema

### Standard Library

- `context` - Context propagation for cancellation and timeouts
- `log/slog` - Structured logging (pass logger via context)
- `net` - TCP listener and connection handling
- `sync` - WaitGroup for connection tracking, synchronization primitives
- `os` - Signal handling (SIGINT, SIGTERM)
- `os/signal` - Signal notification
- `flag` - Command-line argument parsing
- `fmt` - String formatting
- `encoding/json` - JSON parsing for LLM responses
- `time` - Timeouts and timestamps
- `errors` - Error handling

---

## References

### PostgreSQL Wire Protocol Documentation

1. **Protocol Overview**
   - URL: https://www.postgresql.org/docs/current/protocol-overview.html
   - Topics: Connection phases, message format, query execution modes, data formats

2. **Protocol Message Flow**
   - URL: https://www.postgresql.org/docs/current/protocol-flow.html
   - Topics: Startup sequence, authentication flow, Simple Query Protocol, Extended Query Protocol

3. **Protocol Message Formats**
   - URL: https://www.postgresql.org/docs/current/protocol-message-formats.html
   - Topics: Complete reference for all message types and their binary format

4. **Password Authentication**
   - URL: https://www.postgresql.org/docs/current/auth-password.html
   - Topics: SCRAM-SHA-256, MD5, cleartext password authentication methods

### OpenAI Documentation

5. **Structured Outputs Guide**
   - URL: https://platform.openai.com/docs/guides/structured-outputs
   - Topics: How to use structured outputs, JSON schema, response_format parameter

6. **Chat Completions API**
   - URL: https://platform.openai.com/docs/api-reference/chat
   - Topics: Chat completion endpoint, message format, parameters

### Additional Resources

7. **pgproto3 Package Documentation**
   - URL: https://pkg.go.dev/github.com/jackc/pgx/v5/pgproto3
   - Topics: Backend/Frontend types, message encoding/decoding, protocol implementation

8. **PostgreSQL OID Reference**
   - URL: https://www.postgresql.org/docs/current/datatype-oid.html
   - Topics: Object identifiers for data types (needed for RowDescription messages)
