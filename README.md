# agentic-postgres

Are you tired of maintaining your SQL database? Tired of having to write "syntactically correct" SQL queries? Tired of having to query tables that "actually exist"?

Well look no further. We built agentic-postgres to be the database of the future. There's nothing to maintain. No special query language to learn. It never returns errors. And the only limits on what you can query are the limits of your own imagination.

And the best part? It's Postgres wire-compatible, so you can plug it in wherever you currently use Postgres.

Just grab an API key for your favorite AI model provider, connect, and start querying. It's really that simple.

## What is it?

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
| `-prompt` | Built-in prompt | Path to a file containing a custom system prompt |

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

## Supported providers

### OpenAI

Username: `openai`. Password: your OpenAI API key. Database: any OpenAI model name (e.g. `gpt-5.4`, `gpt-4o`, `o3`).

```bash
PGPASSWORD="sk-..." psql -h localhost -U openai -d gpt-5.4
```

**Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `reasoning_effort` | Reasoning effort level (e.g. `none`, `minimal`, `low`, `medium`, `high`). Valid values depend on the model. | Lowest supported for the model |

### Anthropic

Username: `anthropic`. Password: your Anthropic API key. Database: any Anthropic model name (e.g. `claude-sonnet-4-6`, `claude-opus-4-6`).

```bash
PGPASSWORD="sk-ant-..." psql -h localhost -U anthropic -d claude-sonnet-4-6
```

**Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `effort` | Output effort level (`low`, `medium`, `high`, `max`) | `low` |
| `thinking` | Extended thinking budget in tokens (minimum 1024) | Disabled |

### Setting options

Options are passed via the `options` connection parameter (`PGOPTIONS` env var or `options=` in the connection string). Multiple options are space-separated.

```bash
PGPASSWORD="sk-..." PGOPTIONS="reasoning_effort=high" psql -h localhost -U openai -d gpt-5.4
```

```bash
PGPASSWORD="sk-ant-..." PGOPTIONS="effort=high thinking=10000" psql -h localhost -U anthropic -d claude-opus-4-6
```

```bash
psql "postgres://openai:sk-...@localhost/gpt-5.4?options=reasoning_effort%3Dhigh"
```

```bash
psql "host=localhost user=anthropic password=sk-ant-... dbname=claude-sonnet-4-6 options=effort=high"
```

## How it works

Each client connection gets its own LLM chat session. The server translates Postgres wire protocol messages into LLM API calls and converts the structured JSON responses back into proper Postgres result sets. Chat history is maintained per connection, so the LLM can stay consistent across queries within a session.

## Security note

Authentication uses cleartext passwords because the server needs the raw API key to call the LLM provider. Connect over localhost or use SSH tunneling when running remotely.
