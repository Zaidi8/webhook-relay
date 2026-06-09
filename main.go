package main

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

func main() {
	e := echo.New() // initialise the framework instance

	e.Use(middleware.Recover()) // middleware: recover from panics

	e.GET("/", func(c *echo.Context) error { // a route
		return c.String(http.StatusOK, "Hello, World!")
	})

	if err := e.Start(":8080"); err != nil { // start listening (blocks)
		slog.Error("failed to start server", "error", err)
	}
}
