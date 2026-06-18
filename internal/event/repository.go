package event

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("event not found")

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

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	repo := &Repository{pool}
	return repo
}
func (r *Repository) Ping(ctx context.Context) error {
	err := r.pool.Ping(ctx)
	return err
}

func (r *Repository) ClaimDueEvent(ctx context.Context) (*EventWithPayload, error) {

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

	row := r.pool.QueryRow(ctx, query)
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

func (r *Repository) MarkDelivered(ctx context.Context, id int64) error {
	query := `UPDATE events SET status='delivered', updated_at=now() WHERE id=$1`
	_, err := r.pool.Exec(ctx, query, id)
	return err
}
func backoffSeconds(attempts int) int {
	return 1 << attempts
}

func (r *Repository) MarkFailed(ctx context.Context, ev *EventWithPayload, deliveryErr string) error {
	newAttempts := ev.Attempts + 1
	status := "failed"
	query := `UPDATE events SET status=$1, attempts=$2, last_error=$3, next_attempt_at=now() + ($4 * interval '1 second'), updated_at=now() WHERE id=$5`
	backoffTime := backoffSeconds(newAttempts)
	if newAttempts >= ev.MaxAttempts {
		status = "dead"
	}
	_, err := r.pool.Exec(ctx, query, status, newAttempts, deliveryErr, backoffTime, ev.Id)
	return err
}

func (r *Repository) GetEventByID(ctx context.Context, id int) (*Event, error) {

	var query = `SELECT id, source, source_event_id, event_type, status, attempts, created_at 
			FROM events WHERE id = $1`
	row := r.pool.QueryRow(ctx, query, id) // no arg
	var ev Event
	err := row.Scan(&ev.Id, &ev.Source, &ev.SourceEventId, &ev.EventType, &ev.Status, &ev.Attempts, &ev.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

func (r *Repository) GetEvents(ctx context.Context, status string) ([]Event, error) {
	var query string = ""
	var rows pgx.Rows
	var err error
	if status == "" {
		query = `SELECT id, source, source_event_id, event_type, status, attempts, created_at 
			FROM events`
		rows, err = r.pool.Query(ctx, query) // no arg
	} else {
		query = `SELECT id, source, source_event_id, event_type, status, attempts, created_at 
			FROM events WHERE status = $1`
		rows, err = r.pool.Query(ctx, query, status) // one arg
	}

	events := []Event{}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev Event
		err := rows.Scan(&ev.Id, &ev.Source, &ev.SourceEventId, &ev.EventType, &ev.Status, &ev.Attempts, &ev.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *Repository) InsertEvent(ctx context.Context, source string, metaID string, metaType string, raw []byte) (bool, error) {

	const query = `INSERT INTO events (source, source_event_id, event_type, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (source, source_event_id) DO NOTHING`

	tag, err := r.pool.Exec(ctx, query, source, metaID, metaType, raw)
	if err != nil {
		return false, fmt.Errorf("inserting event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	return true, nil
}

func (r *Repository) RetryEvent(ctx context.Context, id int) (bool, error) {
	query := `UPDATE events SET status='received', attempts=0, next_attempt_at=now(), last_error=NULL, updated_at=now()
			WHERE id = $1 AND status IN ('failed','dead')`

	tag, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return false, fmt.Errorf("retrying event: %w", err)

	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	return true, nil
}
