package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

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

func main() {
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

	if err := e.Start(":8080"); err != nil { // start listening (blocks)
		slog.Error("failed to start server", "error", err)
	}

}
