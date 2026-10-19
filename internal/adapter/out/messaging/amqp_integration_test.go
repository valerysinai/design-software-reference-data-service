package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestAMQPPublisherIntegration(t *testing.T) {
	url := os.Getenv("REFERENCE_DATA_AMQP_URL")
	if url == "" {
		t.Skip("REFERENCE_DATA_AMQP_URL is not configured")
	}
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := channel.ExchangeDeclare(referenceExchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	queue, err := channel.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.QueueBind(queue.Name, "reference.catalog.updated", referenceExchange, false, nil); err != nil {
		t.Fatal(err)
	}
	messages, err := channel.Consume(queue.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	publisher := NewAMQPPublisher(url)
	if err := publisher.PublishCatalogUpdated(context.Background(), application.CatalogUpdated{
		CatalogID: "catalog-id", CatalogCode: "MODALITY", UpdatedBy: "admin-id", UpdatedAt: "2026-09-15T12:00:00Z", CorrelationID: "request-id",
		ChangedItems: []application.CatalogChangeItem{{ItemID: "detail-id", Code: "IN_PERSON", Label: "Presencial", ChangeType: application.ChangeTypeUpdated}},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case delivery := <-messages:
		var envelope map[string]any
		if err := json.Unmarshal(delivery.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["event_type"] != "reference.catalog.updated" || envelope["source_service"] != "reference-data-service" || envelope["correlation_id"] != "request-id" {
			t.Fatalf("unexpected envelope: %v", envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reference.catalog.updated")
	}
}

func TestAMQPPublisherRoutesUnroutableMessageToRealDLQ(t *testing.T) {
	url := os.Getenv("REFERENCE_DATA_AMQP_URL")
	if url == "" {
		t.Skip("REFERENCE_DATA_AMQP_URL is not configured")
	}
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := declareDLQ(channel); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.QueuePurge(referenceDLQ, false); err != nil {
		t.Fatal(err)
	}

	unroutableExchange := fmt.Sprintf("%s.it.%d", referenceExchange, time.Now().UnixNano())
	defer channel.ExchangeDelete(unroutableExchange, false, false)

	publisher := NewAMQPPublisher(url)
	publisher.exchange = unroutableExchange
	publisher.backoff = []time.Duration{time.Nanosecond, time.Nanosecond, time.Nanosecond}
	if err := publisher.PublishCatalogUpdated(context.Background(), application.CatalogUpdated{
		CatalogID:     "catalog-id",
		CatalogCode:   "MODALITY",
		UpdatedBy:     "admin-id",
		UpdatedAt:     "2026-09-15T12:00:00Z",
		CorrelationID: "request-id-dlq",
		ChangedItems: []application.CatalogChangeItem{{
			ItemID:     "detail-id",
			Code:       "IN_PERSON",
			Label:      "Presencial",
			ChangeType: application.ChangeTypeUpdated,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	var delivery amqp.Delivery
	var ok bool
	deadline := time.After(5 * time.Second)
	for {
		delivery, ok, err = channel.Get(referenceDLQ, true)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for message in reference-data-events.dlq")
		case <-time.After(100 * time.Millisecond):
		}
	}

	var envelope map[string]any
	if err := json.Unmarshal(delivery.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["event_type"] != "reference.catalog.updated" || envelope["source_service"] != "reference-data-service" || envelope["correlation_id"] != "request-id-dlq" {
		t.Fatalf("unexpected DLQ envelope: %v", envelope)
	}
}
