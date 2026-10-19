package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/persistence"
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
			if !waitContext(ctx, r.interval) {
				return nil
			}
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
	if event.EventType == "reference.catalog.updated" || event.EventType == "reference.training_center.created" {
		return r.publisher.PublishPersisted(ctx, event)
	}
	return errors.New("unsupported outbox event type: " + event.EventType)
}
