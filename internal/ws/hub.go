package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

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

func (h *Hub) Register(user string, cl *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[user] = cl
}
func (h *Hub) Unregister(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, user)
}
func (h *Hub) RouteTo(user string, data []byte) {
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
func (h *Hub) Serve(ctx context.Context, user string, conn *websocket.Conn) {
	send := make(chan []byte, 16)

	cl := &client{conn: conn, send: send}
	h.Register(user, cl)
	go func() {
		for data := range cl.send {
			cl.conn.Write(ctx, websocket.MessageText, data)
		}
	}()
	defer close(cl.send)
	defer h.Unregister(user)
	slog.Info("ws connected", "user", user)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Error("bad envelope", "error", err)
			continue
		}
		h.RouteTo(env.To, env.Data)
	}
}

func NewHub() *Hub {
	ConHub := &Hub{conns: make(map[string]*client)}
	return ConHub
}
