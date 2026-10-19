package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ProjectionEvent is the input to the transactional inbox/read-model port.
type ProjectionEvent struct {
	EventID, EventType, AggregateType, AggregateID string
	Payload                                        json.RawMessage
	OccurredAt                                     time.Time
}

type ProjectionWriter interface {
	ApplyProjection(context.Context, ProjectionEvent) error
}

type ProjectionService struct{ writer ProjectionWriter }

func NewProjectionService(writer ProjectionWriter) *ProjectionService {
	return &ProjectionService{writer: writer}
}

func (s *ProjectionService) Handle(ctx context.Context, body []byte) error {
	var envelope EventEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("invalid event JSON: %w", err)
	}
	if _, err := uuid.Parse(envelope.EventID); err != nil {
		return fmt.Errorf("invalid event_id: %w", err)
	}
	if envelope.SourceService != "reference-data-service" || envelope.Version != "1.0" {
		return fmt.Errorf("unsupported event source/version")
	}
	occurred, err := time.Parse(time.RFC3339Nano, envelope.OccurredAt)
	if err != nil {
		return fmt.Errorf("invalid occurred_at: %w", err)
	}
	var payload struct {
		CatalogID        string `json:"catalog_id"`
		TrainingCenterID string `json:"training_center_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("invalid event payload: %w", err)
	}
	event := ProjectionEvent{EventID: envelope.EventID, EventType: envelope.EventType, Payload: envelope.Payload, OccurredAt: occurred}
	switch envelope.EventType {
	case "reference.catalog.updated":
		event.AggregateType, event.AggregateID = "catalog", payload.CatalogID
	case "reference.training_center.created":
		event.AggregateType, event.AggregateID = "training_center", payload.TrainingCenterID
	default:
		return fmt.Errorf("unsupported event_type")
	}
	if _, err := uuid.Parse(event.AggregateID); err != nil {
		return fmt.Errorf("invalid aggregate ID: %w", err)
	}
	return s.writer.ApplyProjection(ctx, event)
}
