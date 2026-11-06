# Ghostgres

Are you tired of maintaining your relational database? Tired of having to write "syntactically correct" SQL queries? Tired of having queries fail because tables "don't exist"?

Look no further. [Ghostgres](https://ghostgres.com) is the database of the future. There's nothing to maintain. No query language. It doesn't return errors. The only limits on what you can query are the limits of your imagination.

The best part? It's Postgres wire-compatible, so you can plug it in wherever you use Postgres.

Just grab an API key for your favorite model provider and connect:

```bash
psql "postgres://openai:<OPENAI_API_KEY>@try.ghostgres.com/gpt-5.4?sslmode=verify-full&sslrootcert=system"
```

Then start querying:

```
gpt-5.4=> What is the best database?;
```

Just remember to end your queries with a semicolon (`;`).

Oh, and be careful with single (`'`) and double quotes (`"`).

**Built with <3 by [Ghost](https://ghost.build).**

## Connecting

Connect using `psql` or any Postgres client. The username is the LLM provider (`openai` or `anthropic`), the password is your API key, and the database is the model name.

```bash
psql "postgres://<provider>:<api_key>@try.ghostgres.com/<model>?sslmode=verify-full&sslrootcert=system"
```

Keep `sslmode=verify-full&sslrootcert=system` in the connection string: your API key is sent as the password, and these settings ensure it only goes to the real server over an encrypted connection. `sslrootcert=system` (which tells the client to trust the operating system's certificate authorities) requires libpq 16 or newer. With older clients, download the [ISRG Root X1](https://letsencrypt.org/certs/isrgrootx1.pem) certificate and pass its path as `sslrootcert` instead.

## Running Locally

You can run the Ghostgres server locally with:

```bash
go run github.com/timescale/ghostgres/cmd/ghostgres@latest
```

Then connect to localhost:

```bash
psql "postgres://openai:<OPENAI_API_KEY>@localhost/gpt-5.4"
```

### Flags:

The server accepts the following flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-host` | `""` (all interfaces) | Hostname/interface to bind to |
| `-port` | `5432` | Port to listen on |
| `-log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `-prompt` | Built-in prompt | Path to a file containing a custom system prompt |
| `-tls-cert` | (disabled) | Path to TLS certificate PEM file (requires `-tls-key`) |
| `-tls-key` | (disabled) | Path to TLS private key PEM file (requires `-tls-cert`) |
| `-acme-domain` | (disabled) | Domain to automatically obtain a Let's Encrypt certificate for (requires `-acme-cache-dir`) |
| `-acme-cache-dir` | `""` | Directory to persist ACME certificates and account key in |
| `-acme-tls-addr` | `:443` | Address to serve ACME TLS-ALPN-01 challenges on (must be reachable on port 443 of the domain) |

## Supported providers

### OpenAI

Username: `openai`.
Password: your [OpenAI API key](https://platform.openai.com/api-keys).
Database: any OpenAI model name (e.g. `gpt-5.4`, `gpt-4o`, `o3`).

**Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `reasoning_effort` | Reasoning effort level (e.g. `none`, `minimal`, `low`, `medium`, `high`). Valid values depend on the model. | Lowest supported for the model |

### Anthropic

Username: `anthropic`.
Password: your [Anthropic API key](https://platform.claude.com/settings/keys).
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

## SSL/TLS

Ghostgres supports SSL/TLS encryption via the PostgreSQL wire protocol's `SSLRequest` handshake. When TLS is enabled, it is required: clients that don't request SSL are rejected before any credentials are sent.

To enable TLS, provide a certificate and private key:

```bash
ghostgres -tls-cert /path/to/cert.pem -tls-key /path/to/key.pem
```

Or have Ghostgres obtain and renew a certificate from Let's Encrypt automatically. Port 443 of the domain must reach `-acme-tls-addr` to answer TLS-ALPN-01 challenges, and the cache directory should persist across restarts:

```bash
ghostgres -acme-domain db.example.com -acme-cache-dir /var/lib/ghostgres/acme
```

When TLS is not enabled, SSL requests from clients are denied and the connection continues in plaintext.

## Security note

Authentication uses cleartext passwords because the server needs the raw API key to call the LLM provider. When running remotely, enable TLS (see above) and connect with `sslmode=verify-full`. Weaker modes such as `prefer` or `require` don't verify the server's certificate, so an attacker on the network can intercept the connection and capture the API key. Without TLS, connect over localhost or use SSH tunneling.
