package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
)

func TestTrainingCenterCreatedEventOmitsPII(t *testing.T) {
	var output bytes.Buffer
	publisher := NewJSONPublisher(&output)
	err := publisher.PublishTrainingCenterCreated(context.Background(), application.TrainingCenterCreated{
		TrainingCenterID: "e5f6a7b8-0033-4c7d-ae1f-2a3b4c5d6e7f",
		CenterCode:       "C-001", Name: "Centro", MunicipalityID: "municipality-id", CreatedBy: "admin-id", CreatedAt: "2026-09-13T12:00:00Z",
		CorrelationID: "request-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["event_type"] != "reference.training_center.created" {
		t.Fatalf("unexpected event type: %v", event["event_type"])
	}
	for _, key := range []string{"event_id", "event_type", "version", "timestamp", "occurred_at", "source_service", "source_port", "correlation_id", "payload"} {
		if _, ok := event[key]; !ok {
			t.Fatalf("envelope missing %s: %v", key, event)
		}
	}
	if event["source_service"] != "reference-data-service" || event["source_port"] != float64(8002) || event["correlation_id"] != "request-id" {
		t.Fatalf("unexpected envelope: %v", event)
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload is not an object: %T", event["payload"])
	}
	for _, key := range []string{"training_center_id", "center_code", "name", "municipality_id", "created_by", "created_at"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing %s: %v", key, payload)
		}
	}
	if strings.Contains(output.String(), "phone") || strings.Contains(output.String(), "address") {
		t.Fatalf("event contains forbidden fields: %s", output.String())
	}
}

func TestCatalogUpdatedEventUsesRequiredChangeType(t *testing.T) {
	var output bytes.Buffer
	publisher := NewJSONPublisher(&output)
	err := publisher.PublishCatalogUpdated(context.Background(), application.CatalogUpdated{
		CatalogID: "catalog-id", CatalogCode: "MODALITY", UpdatedBy: "admin-id", UpdatedAt: "2026-09-13T12:00:00Z", CorrelationID: "request-id",
		ChangedItems: []application.CatalogChangeItem{{
			ItemID:     "detail-id",
			Code:       "IN_PERSON",
			Label:      "Presencial",
			ChangeType: application.ChangeTypeDeactivated,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Payload struct {
			ChangedItems []struct {
				ChangeType string `json:"change_type"`
			} `json:"changed_items"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Payload.ChangedItems) != 1 || event.Payload.ChangedItems[0].ChangeType != "DEACTIVADO" {
		t.Fatalf("unexpected changed item: %+v", event.Payload.ChangedItems)
	}
}

func TestAMQPPublisherRetriesAndSendsToDLQ(t *testing.T) {
	publisher := NewAMQPPublisher("amqp://guest:guest@localhost:5672/")
	publisher.backoff = []time.Duration{time.Nanosecond, time.Nanosecond, time.Nanosecond}
	var exchanges []string
	publisher.publish = func(_ context.Context, exchange, _ string, _ []byte) error {
		exchanges = append(exchanges, exchange)
		if exchange == referenceDLQ {
			return nil
		}
		return errors.New("broker unavailable")
	}
	err := publisher.PublishCatalogUpdated(context.Background(), application.CatalogUpdated{
		CatalogID: "catalog-id", CatalogCode: "SHIFT", UpdatedBy: "admin-id", UpdatedAt: "2026-09-13T12:00:00Z",
		ChangedItems: []application.CatalogChangeItem{{ItemID: "catalog-id", Code: "SHIFT", Label: "Jornada", ChangeType: application.ChangeTypeUpdated}},
	})
	if err != nil {
		t.Fatalf("unexpected error after DLQ routing: %v", err)
	}
	if len(exchanges) != 5 {
		t.Fatalf("expected 1 attempt, 3 retries and 1 DLQ publish, got %d: %v", len(exchanges), exchanges)
	}
	if exchanges[4] != referenceDLQ {
		t.Fatalf("last publish was not DLQ: %v", exchanges)
	}
}
