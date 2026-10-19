package messaging

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/persistence"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestOutboxInsertAndPending(t *testing.T) {
	dsn := os.Getenv("REFERENCE_DATA_TEST_DSN")
	if dsn == "" {
		t.Skip("REFERENCE_DATA_TEST_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skip("database not available")
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		t.Skipf("database not available: %v", err)
	}

	store := persistence.NewOutboxStore(db)
	event, err := persistence.NewOutboxEvent("catalog", "catalog-1", "reference.catalog.updated", map[string]string{"id": "catalog-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM parameterization.outbox_event WHERE event_id = $1`, event.EventID) })
	pending, err := store.GetPending(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("expected pending outbox event")
	}
	if pending[0].EventType != "reference.catalog.updated" {
		t.Fatalf("unexpected event type: %s", pending[0].EventType)
	}
	if err := store.Insert(context.Background(), event); err == nil {
		t.Fatal("expected duplicate event_id to be rejected")
	}
	var failedIDs []string
	if err := store.LockAndProcessEvent(context.Background(), event.EventID, func(_ context.Context, current persistence.OutboxEvent) error {
		failedIDs = append(failedIDs, current.EventID.String())
		return errors.New("temporary broker outage")
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err := db.QueryRowContext(context.Background(), `SELECT status, attempts FROM parameterization.outbox_event WHERE event_id=$1`, event.EventID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != persistence.OutboxFailed || attempts != 1 {
		t.Fatalf("after failed publish status=%q attempts=%d", status, attempts)
	}
	var recoveredIDs []string
	if err := store.LockAndProcessEvent(context.Background(), event.EventID, func(_ context.Context, current persistence.OutboxEvent) error {
		recoveredIDs = append(recoveredIDs, current.EventID.String())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(failedIDs) != 1 || len(recoveredIDs) != 1 || failedIDs[0] != event.EventID.String() || recoveredIDs[0] != event.EventID.String() {
		t.Fatalf("event identity changed across retry: first=%v retry=%v", failedIDs, recoveredIDs)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT status, attempts FROM parameterization.outbox_event WHERE event_id=$1`, event.EventID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != persistence.OutboxPublished || attempts != 1 {
		t.Fatalf("after recovery status=%q attempts=%d", status, attempts)
	}
}
