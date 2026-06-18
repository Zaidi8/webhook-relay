package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Zaidi8/webhook-relay/internal/api"
	"github.com/Zaidi8/webhook-relay/internal/delivery"
	"github.com/Zaidi8/webhook-relay/internal/event"
	"github.com/Zaidi8/webhook-relay/internal/ws"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
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

	if err := godotenv.Load(); err != nil {
		slog.Error(".env file not found, relying on real environment", "error", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := connectionPool(ctx)
	if err != nil {
		slog.Warn("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	repo := event.NewRepository(pool)
	destURL := os.Getenv("DESTINATION_URL")
	worker := delivery.NewWorker(repo, destURL)
	e := echo.New() // initialise the framework instance
	secret := os.Getenv("WEBHOOK_SECRET")
	hub := ws.NewHub()
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:  true,
		LogURI:     true,
		LogStatus:  true,
		LogLatency: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			slog.Info(
				"request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency", v.Latency,
			)
			return nil
		},
	}))
	handler := api.NewHandler(repo, hub, secret)
	handler.RegisterRoutes(e)
	var wg sync.WaitGroup
	wg.Add(1)
	go worker.StartWorker(ctx, &wg)

	sc := echo.StartConfig{Address: ":8080"}
	if err := sc.Start(ctx, e); err != nil && err != http.ErrServerClosed {
		slog.Error("server err", "error", err)
	}
	wg.Wait()
	slog.Info("worker stopped, existing")
}
