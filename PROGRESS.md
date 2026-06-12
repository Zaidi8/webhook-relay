# Progress Log — Webhook Relay

Daily updates for review. Newest entry on top. Each entry states the day's goal, what was done, and how a reviewer can test it.

## Day 4 — Fri, 12 Jun 2026

**Goal:** Actually *relay* — a background worker that picks up stored events, delivers them over HTTP, and retries failures with exponential backoff until they succeed or die.

**Done:**
- Background delivery worker (`startWorker`) — a goroutine started in `main()` (alongside the HTTP server), driven by a `time.Ticker` every 5s. Idle ticks are silent; DB errors are logged and the loop survives them.
- Atomic job claim (`claimDueEvent`) — `UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING …`. Grabs the oldest-due `received`/`failed` event, flips it to `processing`, and returns its payload — all in one statement so two workers can never grab the same row. `pgx.ErrNoRows` → `(nil, nil)` = "nothing due".
- Delivery (`deliver`) — POSTs the payload to `DESTINATION_URL`. 2xx → `markDelivered`; transport error or non-2xx → `markFailed` (the real error/status is recorded in `last_error`).
- Status state machine: `received`/`failed` → `processing` → `delivered`, or → `failed` (retry scheduled) → `dead` once `attempts >= max_attempts`.
- Exponential backoff in `markFailed`: `next_attempt_at = now() + (2^attempts) seconds`, parameterised interval.
- Local `POST /sink` route as a delivery target for offline end-to-end testing.

**How to test:**
1. `.env` has `DESTINATION_URL=http://localhost:8080/sink`. Start: `docker compose up -d && go run .`
2. Happy path — make events claimable, then watch them deliver:
   ```
   docker compose exec -T db psql -U postgres -d webhook_relay -c "UPDATE events SET status='received', attempts=0, next_attempt_at=now();"
   ```
   → within ~5s/event the server logs `sink recieved …`; then `SELECT id, status, attempts FROM events;` shows all `delivered`, `attempts=0`.
3. Failure path — point at a dead port (`DESTINATION_URL=http://localhost:9999/nope`), reset rows, restart:
   → events go `failed`, `attempts` climbs each retry (backoff grows), `last_error` shows `connection refused`, and after 5 attempts → `dead`. Restore `.env` to `/sink` after.

## Day 3 — Thu, 11 Jun 2026

**Goal:** Read events back out, and make webhook intake trustworthy with signature verification and request validation.

**Done:**
- `.env` auto-loaded at startup via `godotenv` (no more manual `export DATABASE_URL`); missing `.env` is non-fatal.
- `GET /events` — lists events as JSON, with an optional `?status=` filter (parameterised query, no string concatenation). Returns `[]` (not `null`) when empty.
- `GET /events/:id` — fetches one event via `QueryRow`; `404` when not found (`pgx.ErrNoRows`), `400` on a non-numeric id (parsed with `strconv`), full `payload` included.
- HMAC-SHA256 signature verification on `POST /webhooks/:source` (`validateSignature` helper): hashes the raw body with the shared secret (`WEBHOOK_SECRET`), constant-time compare via `hmac.Equal`, `401` on mismatch/missing.
- Request validation on intake: requires `Content-Type: application/json` (`400` otherwise) and caps the body at 1 MB via `http.MaxBytesReader`.

**How to test:**
1. Start the DB + run: `docker compose up -d && go run .` (DSN now comes from `.env`)
2. Seed a valid signed event:
   ```
   BODY='{"id":"evt_v1","type":"push"}'
   SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | awk '{print $2}')
   curl -i -X POST localhost:8080/webhooks/github -H "Content-Type: application/json" -H "X-Signature-256: $SIG" -d "$BODY"
   ```
   → `202 stored`
3. Wrong signature: same as above with `-H "X-Signature-256: deadbeef"` → `401 invalid signature`
4. Wrong content-type: `curl -i -X POST localhost:8080/webhooks/github -d '{"id":"x"}'` → `400 invalid request`
5. List: `curl -s localhost:8080/events` → JSON array; `curl -s "localhost:8080/events?status=delivered"` → `[]`
6. Get one: `curl -s localhost:8080/events/1` → that row; `curl -s localhost:8080/events/9999` → `404 event not found`; `curl -s localhost:8080/events/abc` → `400 invalid id`

**Next:** background delivery worker — pick due events (`status='received'`, `next_attempt_at <= now()`), POST to a destination, retry with backoff, mark `delivered`/`failed`/`dead`.
**Blockers:** none. `WEBHOOK_SECRET` lives only in `.env` (gitignored) — anyone cloning must set their own.

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
