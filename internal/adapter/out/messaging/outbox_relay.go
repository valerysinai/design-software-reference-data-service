package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/persistence"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
)

// OutboxRelay publishes pending outbox messages to RabbitMQ.
type OutboxRelay struct {
	store     *persistence.OutboxStore
	publisher *AMQPPublisher
	interval  time.Duration
}

func NewOutboxRelay(store *persistence.OutboxStore, publisher *AMQPPublisher, interval time.Duration) *OutboxRelay {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &OutboxRelay{store: store, publisher: publisher, interval: interval}
}

func (r *OutboxRelay) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if err := r.store.LockAndProcess(ctx, 20, r.publishOne); err != nil {
			// A temporary PostgreSQL outage must not permanently stop the relay.
			time.Sleep(r.interval)
			continue
		}
		timer := time.NewTimer(r.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (r *OutboxRelay) publishOne(ctx context.Context, event persistence.OutboxEvent) error {
	if r.publisher == nil {
		return errors.New("outbox relay publisher is nil")
	}
	if event.EventType == "reference.catalog.updated" {
		var payload application.CatalogUpdated
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return r.publisher.PublishOutboxEvent(ctx, event.EventID.String(), event.EventType, payload)
	}
	if event.EventType == "reference.training_center.created" {
		var payload application.TrainingCenterCreated
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return r.publisher.PublishOutboxEvent(ctx, event.EventID.String(), event.EventType, payload)
	}
	return errors.New("unsupported outbox event type: " + event.EventType)
}
