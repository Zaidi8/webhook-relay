package delivery

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Zaidi8/webhook-relay/internal/event"
)

type Worker struct {
	repo       *event.Repository
	httpClient *http.Client
	destURL    string
}

func NewWorker(repo *event.Repository, destURL string) *Worker {
	HttpClient := &http.Client{Timeout: 10 * time.Second}
	return &Worker{repo: repo, httpClient: HttpClient, destURL: destURL}
}
func (w *Worker) deliver(ctx context.Context, ev *event.EventWithPayload) {
	resp, err := w.httpClient.Post(w.destURL, "application/json", bytes.NewReader(ev.Payload))
	if err != nil {
		w.repo.MarkFailed(ctx, ev, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		w.repo.MarkDelivered(ctx, ev.Id)
		return
	}
	w.repo.MarkFailed(ctx, ev, fmt.Sprintf("got status %d", resp.StatusCode))

}

func (w *Worker) StartWorker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ev, err := w.repo.ClaimDueEvent(ctx)
			if err != nil {
				slog.Error("event not claimed", "error", err)
				continue
			}
			if ev == nil {
				continue
			}
			w.deliver(ctx, ev)

		}

	}
}
