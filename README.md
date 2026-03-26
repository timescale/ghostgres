# agentic-postgres

A wire-compatible PostgreSQL server that routes all queries to an LLM instead of executing them against a real database. Each connection maintains its own LLM context for consistency across queries.


## Install

```bash
go install github.com/timescale/agentic-postgres/cmd/agentic-postgres@latest
```

## Usage

Start the server:

```bash
agentic-postgres
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-host` | `""` (all interfaces) | Hostname/interface to bind to |
| `-port` | `5432` | Port to listen on |
| `-log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |

## Connecting

Connect using `psql` or any Postgres client. The username is the LLM provider, the password is your API key, and the database is the model name.

### psql flags

```bash
PGPASSWORD=<api_key> psql -h localhost -U <provider> -d <model>
```

Example:

```bash
PGPASSWORD="sk-..." psql -h localhost -U openai -d gpt-5.4
```

### Connection URI

```bash
psql "postgres://<provider>:<api_key>@localhost/<model>"
```

Example:

```bash
psql "postgres://openai:sk-...@localhost/gpt-5.4"
```

### Keyword/value connection string

```bash
psql "host=localhost user=<provider> password=<api_key> dbname=<model>"
```

Example:

```bash
psql "host=localhost user=openai password=sk-... dbname=gpt-5.4"
```

### Reasoning effort

By default, the server uses the lowest available reasoning effort for each model to minimize query latency. You can override this via the `options` connection parameter. Valid values depend on the model (e.g., `none`, `minimal`, `low`, `medium`, `high`).

```bash
PGPASSWORD="sk-..." PGOPTIONS="reasoning_effort=high" psql -h localhost -U openai -d gpt-5.4
```

```bash
psql "postgres://openai:sk-...@localhost/gpt-5.4?options=reasoning_effort%3Dhigh"
```

```bash
psql "host=localhost user=openai password=sk-... dbname=gpt-5.4 options=reasoning_effort=high"
```

## How it works

Each client connection gets its own LLM chat session. The server translates Postgres wire protocol messages into LLM API calls and converts the structured JSON responses back into proper Postgres result sets. Chat history is maintained per connection, so the LLM can stay consistent across queries within a session.

## Security note

Authentication uses cleartext passwords because the server needs the actual API key to call OpenAI. Connect over localhost or use SSH tunneling when running remotely.
