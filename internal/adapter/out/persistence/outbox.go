package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	OutboxPending    = "pending"
	OutboxPublishing = "publishing"
	OutboxPublished  = "published"
	OutboxFailed     = "failed"
)

type OutboxEvent struct {
	ID            uuid.UUID       `json:"id"`
	EventID       uuid.UUID       `json:"event_id"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	SentAt        sql.NullTime    `json:"sent_at,omitempty"`
	LastError     sql.NullString  `json:"last_error,omitempty"`
}

func NewOutboxEvent(aggregateType, aggregateID, eventType string, payload any) (OutboxEvent, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:            uuid.New(),
		EventID:       uuid.New(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       body,
		Status:        OutboxPending,
		Attempts:      0,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}, nil
}

type OutboxStore struct {
	db *sql.DB
}

func NewOutboxStore(db *sql.DB) *OutboxStore { return &OutboxStore{db: db} }

func (s *OutboxStore) Insert(ctx context.Context, event OutboxEvent) error {
	if s.db == nil {
		return errors.New("outbox store db is nil")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO parameterization.outbox_event (
			id, event_id, aggregate_type, aggregate_id, event_type, payload, status, attempts, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, event.ID, event.EventID, event.AggregateType, event.AggregateID, event.EventType, event.Payload, event.Status, event.Attempts, event.CreatedAt, event.UpdatedAt)
	return err
}

// InsertTx writes an event using the caller's business transaction.
func (s *OutboxStore) InsertTx(ctx context.Context, tx *sql.Tx, event OutboxEvent) error {
	if tx == nil {
		return errors.New("outbox transaction is nil")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO parameterization.outbox_event (
			id, event_id, aggregate_type, aggregate_id, event_type, payload, status, attempts, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, event.ID, event.EventID, event.AggregateType, event.AggregateID, event.EventType, event.Payload, event.Status, event.Attempts, event.CreatedAt, event.UpdatedAt)
	return err
}

func (s *OutboxStore) GetPending(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, aggregate_type, aggregate_id, event_type, payload, status, attempts, created_at, updated_at, sent_at, last_error
		FROM parameterization.outbox_event
		WHERE status IN ($1, $2)
		ORDER BY created_at ASC
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	`, OutboxPending, OutboxFailed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []OutboxEvent
	for rows.Next() {
		var event OutboxEvent
		var payload []byte
		var sentAt sql.NullTime
		var lastError sql.NullString
		if err := rows.Scan(&event.ID, &event.EventID, &event.AggregateType, &event.AggregateID, &event.EventType, &payload, &event.Status, &event.Attempts, &event.CreatedAt, &event.UpdatedAt, &sentAt, &lastError); err != nil {
			return nil, err
		}
		event.Payload = payload
		event.SentAt = sentAt
		event.LastError = lastError
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *OutboxStore) MarkPublishing(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE parameterization.outbox_event
		SET status = $1, updated_at = NOW()
		WHERE id = $2
	`, OutboxPublishing, id)
	return err
}

func (s *OutboxStore) MarkPublished(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE parameterization.outbox_event
		SET status = $1, sent_at = NOW(), updated_at = NOW(), last_error = NULL
		WHERE id = $2
	`, OutboxPublished, id)
	return err
}

func (s *OutboxStore) MarkFailed(ctx context.Context, id uuid.UUID, errCause error) error {
	msg := ""
	if errCause != nil {
		msg = errCause.Error()
	}
	_, execErr := s.db.ExecContext(ctx, `
		UPDATE parameterization.outbox_event
		SET status = $1, attempts = attempts + 1, last_error = $2, updated_at = NOW()
		WHERE id = $3
	`, OutboxFailed, msg, id)
	if execErr != nil {
		return execErr
	}
	return errCause
}

func (s *OutboxStore) MarkPending(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE parameterization.outbox_event
		SET status = $1, updated_at = NOW()
		WHERE id = $2
	`, OutboxPending, id)
	return err
}

func (s *OutboxStore) LockAndProcess(ctx context.Context, limit int, publish func(context.Context, OutboxEvent) error) error {
	return s.lockAndProcess(ctx, nil, limit, publish)
}

// LockAndProcessEvent processes a single event, useful for targeted replay and
// operational checks without claiming unrelated pending messages.
func (s *OutboxStore) LockAndProcessEvent(ctx context.Context, eventID uuid.UUID, publish func(context.Context, OutboxEvent) error) error {
	return s.lockAndProcess(ctx, &eventID, 1, publish)
}

func (s *OutboxStore) lockAndProcess(ctx context.Context, eventID *uuid.UUID, limit int, publish func(context.Context, OutboxEvent) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE reference_writer"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, event_id, aggregate_type, aggregate_id, event_type, payload, status, attempts, created_at, updated_at, sent_at, last_error
		FROM parameterization.outbox_event
		WHERE status IN ($1, $2)
		  AND ($3::uuid IS NULL OR event_id = $3)
		ORDER BY created_at ASC
		LIMIT $4
		FOR UPDATE SKIP LOCKED
	`, OutboxPending, OutboxFailed, eventID, limit)
	if err != nil {
		return err
	}
	var events []OutboxEvent
	for rows.Next() {
		var event OutboxEvent
		var payload []byte
		if err := rows.Scan(&event.ID, &event.EventID, &event.AggregateType, &event.AggregateID, &event.EventType, &payload, &event.Status, &event.Attempts, &event.CreatedAt, &event.UpdatedAt, &event.SentAt, &event.LastError); err != nil {
			rows.Close()
			return err
		}
		event.Payload = payload
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, `UPDATE parameterization.outbox_event SET status=$1, updated_at=NOW() WHERE id=$2`, OutboxPublishing, event.ID); err != nil {
			return fmt.Errorf("mark publishing: %w", err)
		}
		if err := publish(ctx, event); err != nil {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE parameterization.outbox_event SET status=$1, attempts=attempts+1, last_error=$2, updated_at=NOW() WHERE id=$3`, OutboxFailed, err.Error(), event.ID); updateErr != nil {
				return errors.Join(err, updateErr)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE parameterization.outbox_event SET status=$1, sent_at=NOW(), updated_at=NOW(), last_error=NULL WHERE id=$2`, OutboxPublished, event.ID); err != nil {
			return fmt.Errorf("mark published: %w", err)
		}
	}
	return tx.Commit()
}
