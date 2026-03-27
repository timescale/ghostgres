# Ghostgres

Are you tired of maintaining your relational database? Tired of having to write "syntactically correct" SQL queries? Tired of having queries fail because tables "don't exist"?

Look no further. Ghostgres is the database of the future. There's nothing to maintain. No query language. It doesn't return errors. The only limits on what you can query are the limits of your imagination.

The best part? It's Postgres wire-compatible, so you can plug it in wherever you use Postgres.

Just start the server:

```bash
go run github.com/timescale/ghostgres/cmd/ghostgres@latest
```

Grab an API key for your favorite model provider, and connect:

```
psql "postgres://openai:<OPENAI_API_KEY>@localhost/gpt-5.4"
```

Then start querying:

```
gpt-5.4=> What is the best database?;
```

Just remember to end your queries with a semicolon (`;`).

Oh, and be careful with single (`'`) and double quotes (`"`).

## Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-host` | `""` (all interfaces) | Hostname/interface to bind to |
| `-port` | `5432` | Port to listen on |
| `-log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `-prompt` | Built-in prompt | Path to a file containing a custom system prompt |

## Connecting

Connect using `psql` or any Postgres client. The username is the LLM provider, the password is your API key, and the database is the model name.

```bash
psql "postgres://<provider>:<api_key>@localhost/<model>"
```

## Supported providers

### OpenAI

Username: `openai`.
Password: your OpenAI API key.
Database: any OpenAI model name (e.g. `gpt-5.4`, `gpt-4o`, `o3`).

**Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `reasoning_effort` | Reasoning effort level (e.g. `none`, `minimal`, `low`, `medium`, `high`). Valid values depend on the model. | Lowest supported for the model |

### Anthropic

Username: `anthropic`.
Password: your Anthropic API key.
Database: any Anthropic model name (e.g. `claude-sonnet-4-6`, `claude-opus-4-6`).

**Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `effort` | Output effort level (`low`, `medium`, `high`, `max`) | `low` |
| `thinking` | Extended thinking budget in tokens (minimum 1024) | Disabled |

### Setting options

Options are passed via the `options` connection parameter (`PGOPTIONS` env var or `options=` in the connection string). Multiple options are space-separated.

```bash
psql "postgres://openai:sk-...@localhost/gpt-5.4?options=reasoning_effort%3Dhigh"

# Or:

PGOPTIONS="reasoning_effort=high" psql "postgres://openai:sk-...@localhost/gpt-5.4"
```

## How it works

Each client connection gets its own LLM chat session. The server translates Postgres wire protocol messages into LLM API calls and converts the structured JSON responses back into proper Postgres result sets. Chat history is maintained per connection, so the LLM can stay consistent across queries within a session.

## Security note

Authentication uses cleartext passwords because the server needs the raw API key to call the LLM provider. Connect over localhost or use SSH tunneling when running remotely.
