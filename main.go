package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

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

func main() {

	ctx := context.Background()
	pool, err := connectionPool(ctx)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
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

	e.POST("/webhooks/:source", func(c *echo.Context) error {
		raw, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return c.String(http.StatusBadRequest, "cannot read body")
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
