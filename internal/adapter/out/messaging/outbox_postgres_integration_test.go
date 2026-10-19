package messaging

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	persistenceadapter "github.com/code-sena/design-software-reference-data-service/internal/adapter/out/postgres"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestCreateAndOutboxCommitAtomically(t *testing.T) {
	dsn := os.Getenv("REFERENCE_DATA_TEST_DSN")
	if dsn == "" {
		t.Skip("REFERENCE_DATA_TEST_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		t.Skipf("database not available: %v", err)
	}

	repo := persistenceadapter.NewRepository(db)
	ctx := context.Background()
	code := "outbox_" + uuid.NewString()
	name := "Outbox integration test"
	command := application.Command{Name: &name, Code: &code}
	item, err := repo.CreateWithOutbox(ctx, "catalogs", command, "catalog", "reference.catalog.updated", func(item json.RawMessage) (any, error) {
		return map[string]any{"item": json.RawMessage(item)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(item, &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM parameterization.outbox_event WHERE aggregate_id=$1`, created.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM parameterization.catalog WHERE id=$1`, created.ID)
	})
	var businessRows, eventRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM parameterization.catalog WHERE id=$1`, created.ID).Scan(&businessRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM parameterization.outbox_event WHERE aggregate_id=$1 AND event_type=$2`, created.ID, "reference.catalog.updated").Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if businessRows != 1 || eventRows != 1 {
		t.Fatalf("committed business rows=%d, outbox rows=%d; expected one of each", businessRows, eventRows)
	}

	failingCode := "outbox_" + uuid.NewString()
	failingName := "Outbox rollback test"
	failingCommand := application.Command{Name: &failingName, Code: &failingCode}
	_, err = repo.CreateWithOutbox(ctx, "catalogs", failingCommand, "catalog", "reference.catalog.updated", func(json.RawMessage) (any, error) {
		return make(chan int), nil // JSON serialization fails after the catalog INSERT.
	})
	if err == nil {
		t.Fatal("expected the outbox serialization error")
	}
	var rolledBackRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM parameterization.catalog WHERE code=$1`, failingCode).Scan(&rolledBackRows); err != nil {
		t.Fatal(err)
	}
	if rolledBackRows != 0 {
		t.Fatalf("catalog row survived outbox failure: count=%d", rolledBackRows)
	}
	var unexpectedEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM parameterization.outbox_event WHERE aggregate_type='catalog' AND payload::text LIKE $1`, "%"+failingCode+"%").Scan(&unexpectedEvents); err != nil {
		t.Fatal(err)
	}
	if unexpectedEvents != 0 {
		t.Fatalf("outbox row survived failed transaction: count=%d", unexpectedEvents)
	}
}
