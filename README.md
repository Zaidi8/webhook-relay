# Webhook Relay

A backend service in Go that receives webhooks, stores them durably, forwards them to a
configured destination with retries and exponential backoff, and relays real-time messages
between connected clients over WebSocket.

## What it does

The service has three parts:

1. **Webhook receiver** — accepts incoming webhooks, verifies them (HMAC-SHA256), stores them
   in PostgreSQL, and returns `202` quickly. Duplicate events are de-duplicated.
2. **Async delivery worker** — a background worker picks up stored events and forwards them to a
   single configured target URL. Failures are retried with exponential backoff; events that
   exhaust their attempts are marked `dead`. Events can also be re-queued manually.
3. **Real-time relay** — clients connect over WebSocket identifying their user. A message names
   its recipient and is routed live to that user's session through an in-memory hub. Delivery is
   fire-and-forget: if the target isn't connected, the message is dropped.

## Architecture

- **Web framework:** Echo v5
- **Database:** PostgreSQL (via Docker Compose), accessed with `pgx`
- **WebSockets:** `coder/websocket`
- **Event lifecycle:** `received` → `processing` → `delivered`, or → `failed` (retry scheduled)
  → `dead` once `attempts` reaches `max_attempts`.
- The delivery worker runs as a goroutine alongside the HTTP server. On `SIGINT`/`SIGTERM` the
  service shuts down gracefully: it stops accepting new connections, stops the worker, and waits
  for any in-flight delivery to finish before exiting.

## Endpoints

| Method & path             | Purpose                                                        |
| ------------------------- | ------------------------------------------------------------- |
| `POST /webhooks/:source`  | Receive and store a webhook → `202` (signed, de-duplicated)   |
| `GET /events`             | List events; optional `?status=` filter                       |
| `GET /events/:id`         | Fetch one event's details                                     |
| `POST /events/:id/retry`  | Manually re-queue a `failed`/`dead` event                     |
| `GET /ws?user=<id>`       | Open a WebSocket session; relay `{ "to", "data" }` messages   |
| `GET /healthz`            | Liveness check (`200` healthy / `503` if the DB is down)      |

## Prerequisites

- **Go** 1.22+ (developed on 1.26.4)
- **Docker** (Docker Desktop) for the PostgreSQL container
- **websocat** (optional) for testing the WebSocket endpoint from the CLI

On macOS, install only what's missing (check first with `go version`, `docker --version`,
`websocat --version`):

```sh
brew install go
brew install --cask docker   # then start Docker Desktop once so the daemon runs
brew install websocat
```

## Setup

```sh
git clone https://github.com/Zaidi8/webhook-relay.git
cd webhook-relay
```

Create a `.env` file in the repo root (it is gitignored and not committed):

```sh
DATABASE_URL=postgres://postgres:postgres@localhost:5432/webhook_relay
WEBHOOK_SECRET=testsecret123
DESTINATION_URL=http://localhost:8080/sink
```

Start PostgreSQL and apply the schema:

```sh
docker compose up -d
docker compose exec -T db psql -U postgres -d webhook_relay < db/schema.sql
```

Run the service:

```sh
go run .
```

It listens on `:8080`. Smoke test:

```sh
curl -i localhost:8080/healthz   # → 200 healthy
```

## Configuration

| Variable          | Purpose                                                              |
| ----------------- | ------------------------------------------------------------------- |
| `DATABASE_URL`    | PostgreSQL connection string                                        |
| `WEBHOOK_SECRET`  | Shared secret for HMAC-SHA256 verification of incoming webhooks      |
| `DESTINATION_URL` | Target the delivery worker forwards events to                       |

`.env` is loaded automatically at startup; missing values fall back to the real environment.

## Trying it out

Send a signed webhook (the `X-Signature-256` header is the HMAC of the body):

```sh
BODY='{"id":"evt_1","type":"push"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | awk '{print $2}')
curl -i -X POST localhost:8080/webhooks/github \
  -H "Content-Type: application/json" \
  -H "X-Signature-256: $SIG" \
  -d "$BODY"
```

List events: `curl -s localhost:8080/events`

Relay a message between two WebSocket clients (note: quote URLs containing `?` in zsh):

```sh
# terminal 1 — bob listens
websocat "ws://localhost:8080/ws?user=bob"

# terminal 2 — alice sends to bob
websocat "ws://localhost:8080/ws?user=alice"
# then type:
{"to":"bob","data":{"msg":"hi bob"}}
```

bob's terminal receives `{"msg":"hi bob"}`.

## Notes & limitations

- For v1, events forward to a single configured target (`DESTINATION_URL`).
- The WebSocket origin check is left at its default — CLI clients work; browsers require an
  origin allowlist (`websocket.AcceptOptions`).
- `GET` endpoints are currently unauthenticated.
