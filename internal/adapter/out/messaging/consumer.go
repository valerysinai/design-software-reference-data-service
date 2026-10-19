package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	ProjectionQueue        = "reference-data.projection"
	ProjectionDLQ          = "reference-data.projection.dlq"
	projectionDeadExchange = "reference-data.projection.dlx"
	attemptHeader          = "x-r1-attempt"
)

var consumerBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

func EnsureProjectionTopology(url string) error {
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	return DeclareProjectionTopology(ch)
}

func DeclareProjectionTopology(ch *amqp.Channel) error {
	for _, exchange := range []string{referenceExchange, projectionDeadExchange} {
		if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(ProjectionQueue, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	for _, key := range []string{"reference.catalog.updated", "reference.training_center.created"} {
		if err := ch.QueueBind(ProjectionQueue, key, referenceExchange, false, nil); err != nil {
			return err
		}
	}
	// No TTL on the failed-message queue: operator recovery must not race expiry.
	if _, err := ch.QueueDeclare(ProjectionDLQ, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	if err := ch.QueueBind(ProjectionDLQ, "#", projectionDeadExchange, false, nil); err != nil {
		return err
	}
	for _, delay := range consumerBackoff {
		_, err := ch.QueueDeclare(retryQueue(delay), true, false, false, false, amqp.Table{
			"x-queue-type":              "quorum",
			"x-overflow":                "reject-publish",
			"x-dead-letter-strategy":    "at-least-once",
			"x-message-ttl":             int32(delay / time.Millisecond),
			"x-dead-letter-exchange":    "",
			"x-dead-letter-routing-key": ProjectionQueue,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func retryQueue(delay time.Duration) string {
	return fmt.Sprintf("%s.retry.%d", ProjectionQueue, delay/time.Millisecond)
}

type Consumer struct {
	url    string
	handle func(context.Context, []byte) error
}

func NewConsumer(url string, handle func(context.Context, []byte) error) *Consumer {
	return &Consumer{url: url, handle: handle}
}

func (c *Consumer) Run(ctx context.Context) error {
	delay := time.Second
	for ctx.Err() == nil {
		if err := c.runSession(ctx); err != nil && ctx.Err() == nil {
			log.Printf("projection consumer disconnected; reconnecting: %v", err)
		}
		if !waitContext(ctx, delay) {
			break
		}
		if delay < 8*time.Second {
			delay *= 2
		}
	}
	return nil
}

func (c *Consumer) runSession(ctx context.Context) error {
	conn, err := amqp.DialConfig(c.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := DeclareProjectionTopology(ch); err != nil {
		return err
	}
	if err := ch.Qos(1, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.ConsumeWithContext(ctx, ProjectionQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("AMQP delivery channel closed")
			}
			workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.handle(workCtx, delivery.Body)
			cancel()
			if err == nil {
				// The handler committed inbox + effect before this ACK. If this ACK
				// is lost, redelivery is harmless because event_id is already stored.
				if err := delivery.Ack(false); err != nil {
					return err
				}
				continue
			}
			attempt := retryAttempt(delivery.Headers)
			headers := cloneHeaders(delivery.Headers)
			headers[attemptHeader] = int32(attempt + 1)
			headers["x-r1-error"] = "projection processing failed"
			exchange, key := projectionDeadExchange, delivery.Type
			if attempt < len(consumerBackoff) {
				exchange, key = "", retryQueue(consumerBackoff[attempt])
			}
			message := forwardedMessage(delivery, headers)
			publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			forwardErr := publishConfirmed(publishCtx, c.url, exchange, key, message)
			cancel()
			if forwardErr != nil {
				_ = delivery.Nack(false, true)
				return forwardErr
			}
			// Confirm the durable retry/DLQ publication before releasing the original.
			if err := delivery.Ack(false); err != nil {
				return err
			}
		}
	}
}

func retryAttempt(headers amqp.Table) int {
	switch n := headers[attemptHeader].(type) {
	case int32:
		if n >= 0 {
			return int(n)
		}
	case int64:
		if n >= 0 && n < 1000000 {
			return int(n)
		}
	case int:
		if n >= 0 {
			return n
		}
	}
	return 0
}

func cloneHeaders(headers amqp.Table) amqp.Table {
	copy := amqp.Table{}
	for k, v := range headers {
		copy[k] = v
	}
	return copy
}

func forwardedMessage(d amqp.Delivery, headers amqp.Table) amqp.Publishing {
	return amqp.Publishing{ContentType: d.ContentType, DeliveryMode: amqp.Persistent, MessageId: d.MessageId,
		Type: d.Type, Timestamp: d.Timestamp, Headers: headers, Body: d.Body}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ReplayDLQ moves a bounded number of failed messages back to the consumer route.
// The exact body/event_id is preserved and publication is confirmed before ACK.
func ReplayDLQ(ctx context.Context, url string, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("replay limit must be between 1 and 1000")
	}
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return 0, err
	}
	defer ch.Close()
	if err := DeclareProjectionTopology(ch); err != nil {
		return 0, err
	}
	for count := 0; count < limit; count++ {
		delivery, ok, err := ch.Get(ProjectionDLQ, false)
		if err != nil {
			return count, err
		}
		if !ok {
			return count, nil
		}
		headers := cloneHeaders(delivery.Headers)
		headers[attemptHeader] = int32(0)
		headers["x-r1-replayed-at"] = time.Now().UTC().Format(time.RFC3339Nano)
		if err := publishConfirmed(ctx, url, referenceExchange, delivery.Type, forwardedMessage(delivery, headers)); err != nil {
			_ = delivery.Nack(false, true)
			return count, err
		}
		if err := delivery.Ack(false); err != nil {
			return count, err
		}
	}
	return limit, nil
}

func publishConfirmed(ctx context.Context, url, exchange, key string, message amqp.Publishing) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return err
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := ch.NotifyReturn(make(chan amqp.Return, 1))
	if err := ch.PublishWithContext(ctx, exchange, key, true, false, message); err != nil {
		return err
	}
	select {
	case confirmation, ok := <-confirms:
		if !ok || !confirmation.Ack {
			return errors.New("AMQP publication not confirmed")
		}
		select {
		case <-returns:
			return errors.New("AMQP message was not routed")
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}
