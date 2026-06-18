package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Zaidi8/webhook-relay/internal/event"
	"github.com/Zaidi8/webhook-relay/internal/ws"
	"github.com/coder/websocket"
	"github.com/labstack/echo/v5"
)

type Handler struct {
	repo   *event.Repository
	hub    *ws.Hub
	secret string
}

func NewHandler(repo *event.Repository, hub *ws.Hub, secret string) *Handler {
	handler := &Handler{repo, hub, secret}
	return handler

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

func (h *Handler) RegisterRoutes(e *echo.Echo) {

	e.GET("/healthz", func(c *echo.Context) error {
		if err := h.repo.Ping(c.Request().Context()); err != nil {
			return c.String(http.StatusServiceUnavailable, "unhealthy")
		}
		return c.String(http.StatusOK, "healthy")
	})
	e.GET("/events", func(c *echo.Context) error {
		status := c.QueryParam("status")
		events, err := h.repo.GetEvents(c.Request().Context(), status)

		if err != nil {
			return c.String(http.StatusInternalServerError, "could not list events")
		}
		return c.JSON(http.StatusOK, events)
	})
	e.GET("/events/:id", func(c *echo.Context) error {

		rawid := c.Param("id")
		id, check := strconv.Atoi(rawid)
		if check != nil {
			return c.String(http.StatusBadRequest, "invalid id")
		}
		ev, err := h.repo.GetEventByID(c.Request().Context(), id)

		if errors.Is(err, event.ErrNotFound) {
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
		if !validateSignature(h.secret, raw, signature) {
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

		source := c.Param("source")
		inserted, err := h.repo.InsertEvent(c.Request().Context(), source, meta.ID, meta.Type, raw)
		if err != nil {
			slog.Error("insert failed", "error", err)
			return c.String(http.StatusInternalServerError, "could not store event")
		}
		if !inserted {
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

		retried, err := h.repo.RetryEvent(c.Request().Context(), id)

		if err != nil {
			return c.String(http.StatusInternalServerError, "db error")

		}
		if !retried {
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
		h.hub.Serve(ctx, user, con)
		return nil
	})

	e.POST("/sink", func(c *echo.Context) error {
		body, _ := io.ReadAll(c.Request().Body)
		slog.Info("sink recieved", "body", string(body))
		return c.String(http.StatusOK, "ok")
	})
}
