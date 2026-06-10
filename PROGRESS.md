# Progress Log — Webhook Relay

Daily updates for review. Newest entry on top. Each entry states the day's goal, what was done, and how a reviewer can test it.

## Day 2 — Wed, 10 Jun 2026

**Goal:** Stand up PostgreSQL, connect from Go via pgx, create the `events` table, and accept + dedup incoming webhooks.

**Done:**
- PostgreSQL running via Docker Compose (`docker-compose.yml`), named volume for persistence.
- `pgx` connection pool with ping-on-startup (`connectionPool` in `main.go`); DSN read from `DATABASE_URL`.
- `events` table (`db/schema.sql`): `(source, source_event_id)` unique dedup constraint, status CHECK, `idx_events_due` index, sensible defaults.
- `POST /webhooks/:source` — reads JSON body, stores it as JSONB, returns `202`; duplicates skipped via `ON CONFLICT DO NOTHING`.
- `GET /healthz` — returns `200` healthy / `503` when the DB is unreachable.

**How to test:**
1. Start the DB: `docker compose up -d`
2. Export the DSN + run: `export DATABASE_URL=postgres://postgres:postgres@localhost:5432/webhook_relay && go run .`
3. Health check: `curl -i localhost:8080/healthz` → `200 healthy`
4. Send a webhook: `curl -i -X POST localhost:8080/webhooks/github -H 'Content-Type: application/json' -d '{"id":"evt_123","type":"push","data":{"hello":"world"}}'` → `202 stored`
5. Send the same one again → `202 duplicate` (dedup)
6. Confirm one row: `docker compose exec -T db psql -U postgres -d webhook_relay -c 'SELECT id, source, source_event_id, status FROM events;'` → exactly 1 row

**Next:** `GET /events`, `GET /events/:id`, HMAC signature verification, request validation.
**Blockers:** none.

## Day 1 — Tue, 9 Jun 2026

**Goal:** Set up the repository and a minimal running Echo server.

**Done:**
- `go mod init` done; project compiles.
- Echo "hello" server running on `:8080`.
- Repo scaffolding: `README.md`, `.gitignore`, `PROGRESS.md`; `go.mod`/`go.sum` committed; first commit pushed.

**How to test:**
1. Run the server: `go run .`
2. Hit the root route: `curl -i localhost:8080/` → `200` with the hello response.

**Next:** PostgreSQL in Docker, connect via `pgx`, create the `events` table, and start the webhook ingestion endpoint.
**Blockers:** none.
