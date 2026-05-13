# claude-code-transformer

`claude-code-transformer` is a Go/Gin protocol adapter. It exposes an Anthropic Messages-compatible interface locally, converts incoming requests, and forwards them to an upstream OpenAI-compatible Responses API so clients that speak the Anthropic/Claude protocol can reuse model gateways built around the Responses API.

Chinese version: [README_CN.md](README_CN.md)

## Features

- Supports the core Anthropic Messages API route: `/anthropic/v1/messages`.
- Supports both streaming and non-streaming response conversion.
- Converts Claude messages, system prompts, tool calls, tool results, images, thinking/reasoning content, and related fields into OpenAI Responses API requests.
- Converts upstream Responses API or Chat Completions-style responses back into Anthropic message/content block structures.
- Supports built-in `web_search` tool mapping, function tool mapping, reasoning summary, encrypted reasoning content, and normalized usage fields.
- Accepts the upstream API key from query parameters, headers, or the `Authorization` header.
- Provides a lightweight `/messages/count_tokens` endpoint for approximate token estimation.
- Includes CORS, authentication, request logging, and panic recovery middleware.
- Uses `logrus` + `lumberjack` for console output and rotating file logs.

## How It Works

The main request flow looks like this:

![Claude Messages to OpenAI Responses flow](images/project-flow.png)

Core implementation files:

| Module | Description |
| --- | --- |
| `main.go` | Loads config, initializes logging, creates the Gin engine, and starts the HTTP server. |
| `src/router/router.go` | Normalizes `base_path` and registers Anthropic-compatible routes. |
| `src/middleware/auth.go` | Extracts the upstream API key from `ak`, `api-key`, `x-api-key`, `Authorization`, and related inputs. |
| `src/handler/claude-messages.go` | Handles `/messages`, parses Anthropic requests, converts them, and forwards them to the upstream Responses API. |
| `src/handler/count_tokens.go` | Implements approximate token counting for `/messages/count_tokens`. |
| `src/claude/conversion/request_converter.go` | Converts Claude requests into OpenAI Responses requests. |
| `src/claude/conversion/response_converter.go` | Converts upstream responses and SSE events back into Anthropic-compatible responses. |
| `src/openai/client.go` | OpenAI-compatible HTTP/SSE client used to call the upstream service and normalize errors. |
| `src/config/log.go` | Configures stdout + file logging and log rotation. |

## API Routes

With the default configuration, `base_path` is empty and the server listens on port `7777`, so the effective routes are:

| Method | Route | Description |
| --- | --- | --- |
| `POST` | `/anthropic/v1/messages` | Anthropic Messages-compatible endpoint. |
| `POST` | `/anthropic/v1/messages/count_tokens` | Anthropic `count_tokens`-compatible endpoint using a character-count approximation. |

If you change `base_path` in `conf/config.yaml`, the actual routes will change accordingly.

## Requirements

- Go `1.18+`
- An upstream service compatible with the OpenAI Responses API
- A valid API key for that upstream service

## Configuration

The default configuration file is `conf/config.yaml`:

```yaml
server_port: 7777
server_addr: "0.0.0.0"
base_path: ""

openai_base_url: "https://your-openai-compatible-upstream.example.com"

log:
  filename: "./logs/server.log"
  max_size: 100
  max_backups: 10
  max_age: 7
  compress: true
  level: "info"
```

Important fields:

| Key | Description |
| --- | --- |
| `server_addr` | Address the HTTP server binds to. |
| `server_port` | Port the HTTP server listens on. |
| `base_path` | API route prefix. Can be empty or a path starting with `/`. |
| `openai_base_url` | Base URL of the upstream OpenAI-compatible service. The code sends requests to `{openai_base_url}/responses`. |
| `log` | Log file path, retention policy, and log level settings. |

If you publish this project publicly, replace `openai_base_url` with your own upstream gateway and never commit internal endpoints or secrets.

## Quick Start

### 1. Clone the repository

```bash
git clone <your-repo-url>
cd claude-code-transformer
```

### 2. Download dependencies

```bash
go mod download
```

### 3. Update the configuration

Edit `conf/config.yaml` and at minimum verify:

- `openai_base_url` points to a reachable OpenAI-compatible Responses API upstream.
- `server_addr`, `server_port`, and `base_path` match your local or deployment environment.
- The log directory is writable.

### 4. Start the server

```bash
go run main.go -conf ./conf/config.yaml
```

By default, the server listens on:

```text
0.0.0.0:7777
```

## Example Requests

### Non-streaming Messages request

```bash
curl -X POST "http://localhost:7777/anthropic/v1/messages" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${UPSTREAM_API_KEY}" \
  -d '{
    "model": "gpt-4.1",
    "max_tokens": 1024,
    "messages": [
      {
        "role": "user",
        "content": "Hello, introduce yourself briefly."
      }
    ]
  }'
```

### Streaming Messages request

```bash
curl -N -X POST "http://localhost:7777/anthropic/v1/messages" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${UPSTREAM_API_KEY}" \
  -d '{
    "model": "gpt-4.1",
    "max_tokens": 1024,
    "stream": true,
    "messages": [
      {
        "role": "user",
        "content": "Write a short haiku about distributed systems."
      }
    ]
  }'
```

### Token estimation request

```bash
curl -X POST "http://localhost:7777/anthropic/v1/messages/count_tokens" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${UPSTREAM_API_KEY}" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {
        "role": "user",
        "content": "Count these tokens approximately."
      }
    ]
  }'
```

## Authentication

The service extracts the upstream API key from the following locations and uses it when sending the upstream request:

1. Query parameter: `?ak=...`
2. Headers: `api-key`, `API-KEY`, `x-api-key`, `X-API-Key`
3. Header: `Authorization: Bearer ...`
4. Header: `Authorization: ...`

For production usage, prefer `Authorization: Bearer ...` or `x-api-key`, and deploy the service behind HTTPS.

## Custom Request Overrides

`/messages` supports an `X-Custom-Json-Params` header containing a JSON object. The service uses it to override selected output settings before forwarding the upstream request. This header is removed before the upstream call and is not passed through.

Supported fields:

| Field | Type | Description |
| --- | --- | --- |
| `effort` / `reasoning_effort` | string | Overrides reasoning effort. |
| `summary` / `reasoning_summary` | string | Overrides reasoning summary. |
| `force_streaming` | bool/string/number | Forces streaming mode on. |
| `text_verbosity` / `verbosity` | string | Overrides Responses API text verbosity. |
| `include_encrypted_content` | bool/string/number | Controls whether encrypted reasoning content is included. |

Example:

```bash
curl -X POST "http://localhost:7777/anthropic/v1/messages" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${UPSTREAM_API_KEY}" \
  -H 'X-Custom-Json-Params: {"reasoning_effort":"medium","text_verbosity":"low","force_streaming":true}' \
  -d '{
    "model": "gpt-4.1",
    "max_tokens": 1024,
    "messages": [{"role":"user","content":"Explain the request flow."}]
  }'
```

## Development Commands

```bash
# Format code
go fmt ./...

# Static analysis
go vet ./...

# Run all tests
go test ./...

# Run tests for a single package
go test ./src/claude/conversion

# Build all packages
go build ./...
```

## Testing

The project uses Go's standard testing framework together with `stretchr/testify`. Existing tests cover request/response conversion, the OpenAI client, logging configuration, and the custom formatter.

Common commands:

```bash
go test ./...
go test ./src/openai -run TestName
go test ./src/claude/conversion -run TestName
```

## Project Structure

```text
.
├── conf/
│   └── config.yaml              # Default runtime configuration
├── images/
│   └── project-flow.png         # Request flow diagram
├── main.go                      # Application entry point
├── src/
│   ├── claude/
│   │   ├── conversion/          # Claude <-> OpenAI Responses conversion logic
│   │   ├── model/               # Anthropic/Claude request models
│   │   └── constants/           # Protocol constants
│   ├── config/                  # Config loading and log writers
│   ├── formatter/               # logrus formatter
│   ├── handler/                 # HTTP handlers
│   ├── middleware/              # Gin middleware
│   ├── openai/                  # Upstream OpenAI-compatible client
│   ├── router/                  # Route registration
│   └── utils/                   # Shared helpers
├── go.mod
└── go.sum
```
