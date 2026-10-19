package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
)

const ProjectionConsumer = "reference-data.projection.v1"

type InboxStore struct{ db *sql.DB }

func NewInboxStore(db *sql.DB) *InboxStore { return &InboxStore{db: db} }

// The deduplication key and projection effect commit together, before AMQP ACK.
func (s *InboxStore) ApplyProjection(ctx context.Context, event application.ProjectionEvent) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, event.Payload); err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte(event.EventType+"\x00"), compact.Bytes()...))
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL ROLE reference_writer"); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO parameterization.event_inbox (consumer_name,event_id,payload_sha256)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, ProjectionConsumer, event.EventID, hash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var previous string
		if err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM parameterization.event_inbox WHERE consumer_name=$1 AND event_id=$2`, ProjectionConsumer, event.EventID).Scan(&previous); err != nil {
			return err
		}
		if previous != hash {
			return fmt.Errorf("event_id reused with different payload")
		}
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO parameterization.event_projection
		(aggregate_type,aggregate_id,last_event_type,last_payload,last_occurred_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (aggregate_type,aggregate_id) DO UPDATE SET
		applied_events=event_projection.applied_events+1,
		last_event_type=CASE WHEN EXCLUDED.last_occurred_at >= event_projection.last_occurred_at THEN EXCLUDED.last_event_type ELSE event_projection.last_event_type END,
		last_payload=CASE WHEN EXCLUDED.last_occurred_at >= event_projection.last_occurred_at THEN EXCLUDED.last_payload ELSE event_projection.last_payload END,
		last_occurred_at=GREATEST(event_projection.last_occurred_at,EXCLUDED.last_occurred_at),updated_at=NOW()`,
		event.AggregateType, event.AggregateID, event.EventType, []byte(event.Payload), event.OccurredAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}
