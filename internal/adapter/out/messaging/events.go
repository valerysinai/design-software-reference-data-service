package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	referenceExchange = "reference-data-events"
	referenceDLQ      = "reference-data-events.dlq"
	dlqRetention      = 7 * 24 * time.Hour
)

var retryBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

type JSONPublisher struct {
	writer io.Writer
}

func NewJSONPublisher(writer io.Writer) *JSONPublisher { return &JSONPublisher{writer: writer} }

func (p *JSONPublisher) PublishTrainingCenterCreated(_ context.Context, payload application.TrainingCenterCreated) error {
	return writeEnvelope(p.writer, "reference.training_center.created", payload, payload.CorrelationID)
}

func (p *JSONPublisher) PublishCatalogUpdated(_ context.Context, payload application.CatalogUpdated) error {
	return writeEnvelope(p.writer, "reference.catalog.updated", payload, payload.CorrelationID)
}

func writeEnvelope(writer io.Writer, eventType string, payload any, correlationID string) error {
	envelope, err := application.NewEventEnvelope(eventType, payload, correlationID, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(envelope)
}

type AMQPPublisher struct {
	url      string
	exchange string
	dlq      string
	backoff  []time.Duration
	publish  func(context.Context, string, string, []byte) error
}

func NewAMQPPublisher(url string) *AMQPPublisher {
	publisher := &AMQPPublisher{
		url:      url,
		exchange: referenceExchange,
		dlq:      referenceDLQ,
		backoff:  retryBackoff,
	}
	publisher.publish = publisher.publishAMQP
	return publisher
}

func (p *AMQPPublisher) PublishTrainingCenterCreated(ctx context.Context, payload application.TrainingCenterCreated) error {
	return p.publishEnvelope(ctx, "reference.training_center.created", payload, payload.CorrelationID)
}

func (p *AMQPPublisher) PublishCatalogUpdated(ctx context.Context, payload application.CatalogUpdated) error {
	return p.publishEnvelope(ctx, "reference.catalog.updated", payload, payload.CorrelationID)
}

func (p *AMQPPublisher) publishEnvelope(ctx context.Context, eventType string, payload any, correlationID string) error {
	return p.publishEnvelopeWithID(ctx, eventType, payload, correlationID, "")
}

func (p *AMQPPublisher) publishEnvelopeWithID(ctx context.Context, eventType string, payload any, correlationID, eventID string) error {
	envelope, err := application.NewEventEnvelope(eventType, payload, correlationID, time.Now())
	if err != nil {
		return err
	}
	if eventID != "" {
		envelope.EventID = eventID
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	err = p.publishWithRetry(ctx, p.exchange, eventType, body)
	if err == nil {
		return nil
	}
	if dlqErr := p.publish(ctx, p.dlq, eventType, body); dlqErr != nil {
		return errors.Join(err, dlqErr)
	}
	return nil
}

// PublishOutboxEvent preserves the event_id committed with the business row.
func (p *AMQPPublisher) PublishOutboxEvent(ctx context.Context, eventID, eventType string, payload any) error {
	correlationID := ""
	switch value := payload.(type) {
	case application.CatalogUpdated:
		correlationID = value.CorrelationID
	case application.TrainingCenterCreated:
		correlationID = value.CorrelationID
	}
	return p.publishEnvelopeWithID(ctx, eventType, payload, correlationID, eventID)
}

func (p *AMQPPublisher) publishWithRetry(ctx context.Context, exchange, routingKey string, body []byte) error {
	err := p.publish(ctx, exchange, routingKey, body)
	if err == nil {
		return nil
	}
	for _, wait := range p.backoff {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		err = p.publish(ctx, exchange, routingKey, body)
		if err == nil {
			return nil
		}
	}
	return err
}

func (p *AMQPPublisher) publishAMQP(ctx context.Context, exchange, routingKey string, body []byte) error {
	conn, err := amqp.DialConfig(p.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()
	if err := channel.Confirm(false); err != nil {
		return err
	}
	if err := channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if exchange == referenceDLQ {
		if err := declareDLQ(channel); err != nil {
			return err
		}
	}
	confirms := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := channel.NotifyReturn(make(chan amqp.Return, 1))
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	headers := amqp.Table{}
	for key, value := range carrier {
		headers[key] = value
	}
	if err := channel.PublishWithContext(ctx, exchange, routingKey, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now().UTC(),
		Type:         routingKey,
		Headers:      headers,
		Body:         body,
	}); err != nil {
		return err
	}
	select {
	case confirmation := <-confirms:
		if !confirmation.Ack {
			return errors.New("rabbitmq publish was not acknowledged")
		}
		select {
		case returned := <-returns:
			return fmt.Errorf("rabbitmq publish was returned: %s", returned.ReplyText)
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func declareDLQ(channel *amqp.Channel) error {
	if err := channel.ExchangeDeclare(referenceDLQ, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := channel.QueueDeclare(referenceDLQ, true, false, false, false, amqp.Table{
		"x-message-ttl": int32(dlqRetention / time.Millisecond),
	}); err != nil {
		return err
	}
	return channel.QueueBind(referenceDLQ, "#", referenceDLQ, false, nil)
}
