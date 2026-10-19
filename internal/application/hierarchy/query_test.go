package hierarchy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCommandServicePublishesCatalogEventsAfterSuccessfulWrites(t *testing.T) {
	writer := &eventWriter{item: json.RawMessage(`{"id":"catalog-id","code":"SHIFT","name":"Jornada"}`)}
	publisher := &eventPublisher{}
	service := NewCommandService(writer, WithEventPublisher(publisher))
	actor, correlation := "admin-id", "request-id"
	command := Command{CreatedBy: &actor, CorrelationID: &correlation}

	if _, err := service.Create(context.Background(), "catalogs", command); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), "catalogs", "catalog-id", command); err != nil {
		t.Fatal(err)
	}
	if err := service.Deactivate(context.Background(), "catalogs", "catalog-id", command); err != nil {
		t.Fatal(err)
	}

	if len(publisher.catalogs) != 3 {
		t.Fatalf("expected 3 catalog events, got %d", len(publisher.catalogs))
	}
	for i, changeType := range []ChangeType{ChangeTypeAdded, ChangeTypeUpdated, ChangeTypeDeactivated} {
		event := publisher.catalogs[i]
		if event.CatalogID != "catalog-id" || event.CatalogCode != "SHIFT" || event.UpdatedBy != actor || event.CorrelationID != correlation {
			t.Fatalf("unexpected catalog event %d: %+v", i, event)
		}
		if len(event.ChangedItems) != 1 || event.ChangedItems[0].ChangeType != changeType {
			t.Fatalf("unexpected changed item %d: %+v", i, event.ChangedItems)
		}
	}
}

func TestCommandServicePublishesCatalogDetailEventsAfterSuccessfulWrites(t *testing.T) {
	writer := &eventWriter{item: json.RawMessage(`{"id":"detail-id","catalog_id":"catalog-id","code":"IN_PERSON","label":"Presencial"}`)}
	publisher := &eventPublisher{}
	service := NewCommandService(writer, WithEventPublisher(publisher))
	actor, correlation, catalogCode := "admin-id", "request-id", "MODALITY"
	command := Command{CatalogCode: &catalogCode, CreatedBy: &actor, CorrelationID: &correlation}

	if _, err := service.Create(context.Background(), "catalog-details", command); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), "catalog-details", "detail-id", command); err != nil {
		t.Fatal(err)
	}
	if err := service.Deactivate(context.Background(), "catalog-details", "detail-id", command, "catalog-id"); err != nil {
		t.Fatal(err)
	}

	if len(publisher.catalogs) != 3 {
		t.Fatalf("expected 3 detail events, got %d", len(publisher.catalogs))
	}
	for i, changeType := range []ChangeType{ChangeTypeAdded, ChangeTypeUpdated, ChangeTypeDeactivated} {
		event := publisher.catalogs[i]
		if event.CatalogCode != catalogCode || event.UpdatedBy != actor || event.CorrelationID != correlation {
			t.Fatalf("unexpected detail event %d: %+v", i, event)
		}
		if len(event.ChangedItems) != 1 || event.ChangedItems[0].ItemID != "detail-id" || event.ChangedItems[0].ChangeType != changeType {
			t.Fatalf("unexpected changed item %d: %+v", i, event.ChangedItems)
		}
	}
}

func TestCommandServicePublishesTrainingCenterCreatedAfterSuccessfulCreate(t *testing.T) {
	writer := &eventWriter{item: json.RawMessage(`{"id":"center-id","center_code":"C-001","name":"Centro","municipality_id":"municipality-id","address":"Direccion","phone":"300","created_at":"2026-09-15T12:00:00Z"}`)}
	publisher := &eventPublisher{}
	service := NewCommandService(writer, WithEventPublisher(publisher))
	actor, correlation := "admin-id", "request-id"
	command := Command{CreatedBy: &actor, CorrelationID: &correlation}

	if _, err := service.Create(context.Background(), "training-centers", command); err != nil {
		t.Fatal(err)
	}
	if len(publisher.centers) != 1 {
		t.Fatalf("expected one training center event, got %d", len(publisher.centers))
	}
	event := publisher.centers[0]
	if event.TrainingCenterID != "center-id" || event.CenterCode != "C-001" || event.Name != "Centro" || event.MunicipalityID != "municipality-id" || event.CreatedBy != actor || event.CorrelationID != correlation {
		t.Fatalf("unexpected training center event: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || json.Valid(encoded) == false {
		t.Fatalf("invalid event json: %s", encoded)
	}
	if strings.Contains(string(encoded), "phone") || strings.Contains(string(encoded), "address") {
		t.Fatalf("training center event contains PII: %s", encoded)
	}
}

func TestCommandServiceDoesNotPublishWhenWriteFails(t *testing.T) {
	writer := &eventWriter{err: errors.New("write failed")}
	publisher := &eventPublisher{}
	service := NewCommandService(writer, WithEventPublisher(publisher))
	if _, err := service.Create(context.Background(), "catalogs", Command{}); err == nil {
		t.Fatal("expected write error")
	}
	if len(publisher.catalogs) != 0 || len(publisher.centers) != 0 {
		t.Fatalf("published event after failed write: %+v %+v", publisher.catalogs, publisher.centers)
	}
}

type eventWriter struct {
	item json.RawMessage
	err  error
}

func (w *eventWriter) Create(context.Context, string, Command) (json.RawMessage, error) {
	return w.item, w.err
}

func (w *eventWriter) Update(context.Context, string, string, Command) (json.RawMessage, error) {
	return w.item, w.err
}

func (w *eventWriter) Deactivate(context.Context, string, string, Command, ...string) (json.RawMessage, error) {
	return w.item, w.err
}

type eventPublisher struct {
	catalogs []CatalogUpdated
	centers  []TrainingCenterCreated
}

func (p *eventPublisher) PublishCatalogUpdated(_ context.Context, event CatalogUpdated) error {
	p.catalogs = append(p.catalogs, event)
	return nil
}

func (p *eventPublisher) PublishTrainingCenterCreated(_ context.Context, event TrainingCenterCreated) error {
	p.centers = append(p.centers, event)
	return nil
}
