package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

type Event struct {
	Id            int64     `json:"id"`
	Source        string    `json:"source"`
	SourceEventId string    `json:"source_event_id"`
	EventType     string    `json:"event_type"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	CreatedAt     time.Time `json:"created_at"`
}
type EventWithPayload struct {
	Id          int64  `json:"id"`
	Source      string `json:"source"`
	EventType   string `json:"event_type"`
	Payload     []byte `json:"payload"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
}
type client struct {
	conn *websocket.Conn
	send chan []byte
}
type Hub struct {
	mu    sync.Mutex
	conns map[string]*client
}
type Envelope struct {
	To   string          `json:"to"`
	Data json.RawMessage `json:"data"`
}

func connectionPool(ctx context.Context) (*pgxpool.Pool, error) {
	dbURL := os.Getenv("DATABASE_URL")
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func validateSignature(secret string, body []byte, header string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	received, err := hex.DecodeString(header)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, received)

}

func claimDueEvent(ctx context.Context, pool *pgxpool.Pool) (*EventWithPayload, error) {
	query := `UPDATE events SET status='processing',updated_at = now() 
	WHERE id =(
	SELECT id FROM events	
	WHERE status IN ('received','failed')
	AND next_attempt_at <= now()
	ORDER BY next_attempt_at
	FOR UPDATE SKIP LOCKED
	LIMIT 1
	)
	RETURNING id, source, event_type, payload, attempts, max_attempts;
	`

	row := pool.QueryRow(ctx, query)
	event := EventWithPayload{}
	err := row.Scan(&event.Id, &event.Source, &event.EventType, &event.Payload, &event.Attempts, &event.MaxAttempts)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func markDelivered(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	query := `UPDATE events SET status='delivered', updated_at=now() WHERE id=$1`
	_, err := pool.Exec(ctx, query, id)
	return err
}

func markFailed(ctx context.Context, pool *pgxpool.Pool, ev *EventWithPayload, deliveryErr string) error {
	newAttempts := ev.Attempts + 1
	status := "failed"
	query := `UPDATE events SET status=$1, attempts=$2, last_error=$3, next_attempt_at=now() + ($4 * interval '1 second'), updated_at=now() WHERE id=$5`
	backoffSeconds := 1 << newAttempts
	if newAttempts >= ev.MaxAttempts {
		status = "dead"
	}
	_, err := pool.Exec(ctx, query, status, newAttempts, deliveryErr, backoffSeconds, ev.Id)
	return err
}

func deliver(ctx context.Context, pool *pgxpool.Pool, ev *EventWithPayload) {
	destURL := os.Getenv("DESTINATION_URL")
	resp, err := http.Post(destURL, "application/json", bytes.NewReader(ev.Payload))
	if err != nil {
		markFailed(ctx, pool, ev, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		markDelivered(ctx, pool, ev.Id)
		return
	}
	markFailed(ctx, pool, ev, fmt.Sprintf("got status %d", resp.StatusCode))

}

func startWorker(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		event, err := claimDueEvent(ctx, pool)
		if err != nil {
			slog.Error("event not claimed", "error", err)
			continue
		}
		if event == nil {
			continue
		}
		deliver(ctx, pool, event)
	}
}

func (h *Hub) register(user string, cl *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[user] = cl
}
func (h *Hub) unregister(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, user)
}
func (h *Hub) routeTo(user string, data []byte) {
	h.mu.Lock()
	cl, ok := h.conns[user]
	h.mu.Unlock()
	if !ok {
		return
	}
	select {
	case cl.send <- data:
	default:
	}
}

func main() {
	hub := &Hub{conns: make(map[string]*client)}

	if err := godotenv.Load(); err != nil {
		slog.Error(".env file not found, relying on real environment", "error", err)
	}
	ctx := context.Background()
	pool, err := connectionPool(ctx)
	if err != nil {
		slog.Warn("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	e := echo.New() // initialise the framework instance

	e.Use(middleware.Recover()) // middleware: recover from panics

	e.GET("/healthz", func(c *echo.Context) error {
		if err := pool.Ping(c.Request().Context()); err != nil {
			return c.String(http.StatusServiceUnavailable, "unhealthy")
		}
		return c.String(http.StatusOK, "healthy")
	})
	e.GET("/events", func(c *echo.Context) error {
		status := c.QueryParam("status")
		var query string = ""
		if status == "" {
			query = `SELECT id, source, source_event_id, event_type, status, attempts, created_at 
			FROM events`
		} else {
			query = `SELECT id, source, source_event_id, event_type, status, attempts, created_at 
			FROM events WHERE status = $1`
		}
		var rows pgx.Rows
		var err error
		if status == "" {
			rows, err = pool.Query(c.Request().Context(), query) // no arg
		} else {
			rows, err = pool.Query(c.Request().Context(), query, status) // one arg
		}

		events := []Event{}
		if err != nil {
			slog.Error("query failed", "error", err)
			return c.String(http.StatusInternalServerError, "could not list events")
		}
		defer rows.Close()
		for rows.Next() {
			var ev Event
			err := rows.Scan(&ev.Id, &ev.Source, &ev.SourceEventId, &ev.EventType, &ev.Status, &ev.Attempts, &ev.CreatedAt)
			if err != nil {
				slog.Error("Failed to scan rows", "error", err)
				return c.String(http.StatusInternalServerError, "could not scan rows")
			}
			events = append(events, ev)
		}
		if err := rows.Err(); err != nil {
			slog.Error("rows iteration failed", "error", err)
			return c.String(http.StatusInternalServerError, "error reading events")
		}
		return c.JSON(http.StatusOK, events)
	})
	e.GET("/events/:id", func(c *echo.Context) error {

		rawid := c.Param("id")
		id, check := strconv.Atoi(rawid)
		if check != nil {
			return c.String(http.StatusBadRequest, "invalid id")
		}
		var query = `SELECT id, source, source_event_id, event_type, status, attempts, created_at 
			FROM events WHERE id = $1`
		row := pool.QueryRow(c.Request().Context(), query, id) // no arg
		var ev Event
		err := row.Scan(&ev.Id, &ev.Source, &ev.SourceEventId, &ev.EventType, &ev.Status, &ev.Attempts, &ev.CreatedAt)

		if errors.Is(err, pgx.ErrNoRows) {
			return c.String(http.StatusNotFound, "event not found")
		}
		if err != nil {
			slog.Error("query failed", "error", err)
			return c.String(http.StatusInternalServerError, "could not get event")
		}
		return c.JSON(http.StatusOK, ev)
	})

	e.POST("/webhooks/:source", func(c *echo.Context) error {
		if c.Request().Header.Get("Content-Type") != "application/json" {
			return c.String(http.StatusBadRequest, "invalid request")
		}
		r := http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20)
		raw, err := io.ReadAll(r)
		if err != nil {
			return c.String(http.StatusBadRequest, "cannot read body")
		}
		signature := c.Request().Header.Get("X-Signature-256")
		secret := os.Getenv("WEBHOOK_SECRET")
		if !validateSignature(secret, raw, signature) {
			return c.String(http.StatusUnauthorized, "invalid signature")
		}
		var meta struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return c.String(http.StatusBadRequest, "invalid JSON")
		}
		if meta.ID == "" {
			return c.String(http.StatusBadRequest, "missing id")
		}
		if meta.Type == "" {
			return c.String(http.StatusBadRequest, "missing type")
		}
		const query = `INSERT INTO events (source, source_event_id, event_type, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (source, source_event_id) DO NOTHING`

		source := c.Param("source")
		tag, err := pool.Exec(c.Request().Context(), query, source, meta.ID, meta.Type, raw)
		if err != nil {
			slog.Error("insert failed", "error", err)
			return c.String(http.StatusInternalServerError, "could not store event")
		}
		if tag.RowsAffected() == 0 {
			return c.String(http.StatusAccepted, "duplicate")
		}

		return c.String(http.StatusAccepted, "stored")

	})

	e.POST("/events/:id/retry", func(c *echo.Context) error {
		rawid := c.Param("id")
		id, check := strconv.Atoi(rawid)
		if check != nil {
			return c.String(http.StatusBadRequest, "invalid id")
		}
		query := `UPDATE events SET status='received', attempts=0, next_attempt_at=now(), last_error=NULL, updated_at=now()
			WHERE id = $1 AND status IN ('failed','dead')`

		tag, err := pool.Exec(c.Request().Context(), query, id)
		if err != nil {
			return c.String(http.StatusInternalServerError, "db error")

		}
		if tag.RowsAffected() == 0 {
			return c.String(http.StatusNotFound, "event not found")
		}
		return c.String(http.StatusOK, "event status successfully requeued")
	})

	e.GET("/ws", func(c *echo.Context) error {
		ctx := c.Request().Context()
		user := c.QueryParam("user")
		con, err := websocket.Accept(c.Response(), c.Request(), nil)
		if err != nil {
			slog.Error("handshake failed", "error", err)
			return nil
		}
		defer con.Close(websocket.StatusNormalClosure, "")
		send := make(chan []byte, 16)
		cl := &client{conn: con, send: send}
		hub.register(user, cl)
		go func() {
			for data := range cl.send {
				cl.conn.Write(ctx, websocket.MessageText, data)
			}
		}()
		defer close(cl.send)
		defer hub.unregister(user)
		slog.Info("ws connected", "user", user)
		for {
			_, data, err := con.Read(ctx)
			if err != nil {
				break
			}
			var env Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				slog.Error("bad envelope", "error", err)
				continue
			}
			hub.routeTo(env.To, env.Data)
		}
		return nil
	})

	e.POST("/sink", func(c *echo.Context) error {
		body, _ := io.ReadAll(c.Request().Body)
		slog.Info("sink recieved", "body", string(body))
		return c.String(http.StatusOK, "ok")
	})

	go startWorker(ctx, pool)

	if err := e.Start(":8080"); err != nil { // start listening (blocks)
		slog.Error("failed to start server", "error", err)
	}

}
