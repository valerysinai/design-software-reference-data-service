package messaging

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/persistence"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	amqp "github.com/rabbitmq/amqp091-go"
)

func r1Database(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("R1_REQUIRED") != "true" {
		t.Skip("run the isolated R1 runner for real integration/chaos tests")
	}
	dsn := os.Getenv("REFERENCE_DATA_TEST_DSN")
	if dsn == "" || os.Getenv("REFERENCE_DATA_AMQP_URL") == "" {
		t.Fatal("R1 database and AMQP configuration required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow("SELECT current_database()").Scan(&name); err != nil || name != "hu_ref_020" {
		t.Fatal("R1 runner requires its isolated validation database")
	}
	return db
}

func TestR1TransactionalOutbox(t *testing.T) {
	_ = r1Database(t)
	TestCreateAndOutboxCommitAtomically(t)
	t.Log("OUTBOX PASS: business row and event commit together; serialization failure rolls both back")
}

func r1Wait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("timeout: " + description)
}

func projectionCount(db *sql.DB, id string) int {
	var n int
	_ = db.QueryRow("SELECT applied_events FROM parameterization.event_projection WHERE aggregate_type='catalog' AND aggregate_id=$1", id).Scan(&n)
	return n
}

func r1Body(t *testing.T, id string) (application.EventEnvelope, []byte) {
	t.Helper()
	event, err := application.NewEventEnvelope("reference.catalog.updated", application.CatalogUpdated{CatalogID: id, CatalogCode: "R1"}, "r1-integration", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return event, body
}

func TestR1AtomicInboxAndDLQ(t *testing.T) {
	db := r1Database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	service := application.NewProjectionService(persistence.NewInboxStore(db))
	id := uuid.NewString()
	event, body := r1Body(t, id)
	// Concurrent redeliveries race on the same inbox primary key. The observable
	// projection counter must change once even when commits race across workers.
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- service.Handle(ctx, body) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := projectionCount(db, id); got != 1 {
		t.Fatalf("concurrent duplicate effects=%d want 1", got)
	}
	conflicting := event
	conflicting.Payload = json.RawMessage(fmt.Sprintf(`{"catalog_id":%q,"catalog_code":"DIFFERENT"}`, id))
	conflictingBody, _ := json.Marshal(conflicting)
	if err := service.Handle(ctx, conflictingBody); err == nil {
		t.Fatal("same event ID with altered payload was accepted")
	}
	if projectionCount(db, id) != 1 {
		t.Fatal("conflicting duplicate changed the effect")
	}
	t.Log("INBOX PASS: 12 concurrent deliveries, 1 committed effect")

	// Fail the actual database write after inbox insertion. No injected handler
	// or fake broker: rollback, retries and DLQ are exercised by the live service.
	failingID := uuid.NewString()
	failingEvent, failingBody := r1Body(t, failingID)
	_, err := db.Exec(`CREATE OR REPLACE FUNCTION parameterization.r1_fail_projection() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.aggregate_id::text = '` + failingID + `' THEN RAISE EXCEPTION 'R1 controlled projection outage'; END IF; RETURN NEW; END $$;
		DROP TRIGGER IF EXISTS r1_fail_projection ON parameterization.event_projection;
		CREATE TRIGGER r1_fail_projection BEFORE INSERT OR UPDATE ON parameterization.event_projection
		FOR EACH ROW EXECUTE FUNCTION parameterization.r1_fail_projection()`)
	if err != nil {
		t.Fatal(err)
	}
	dropFailure := func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS r1_fail_projection ON parameterization.event_projection; DROP FUNCTION IF EXISTS parameterization.r1_fail_projection()`)
	}
	t.Cleanup(dropFailure)
	url := os.Getenv("REFERENCE_DATA_AMQP_URL")
	if err := EnsureProjectionTopology(url); err != nil {
		t.Fatal(err)
	}
	if err := publishConfirmed(ctx, url, referenceExchange, failingEvent.EventType, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: failingEvent.EventID, Type: failingEvent.EventType, Body: failingBody}); err != nil {
		t.Fatal(err)
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	start := time.Now()
	var failed amqp.Delivery
	r1Wait(t, "three exponential retries then DLQ", func() bool {
		d, ok, err := ch.Get(ProjectionDLQ, false)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			failed = d
			return true
		}
		return false
	})
	if failed.MessageId != failingEvent.EventID || !bytes.Equal(failed.Body, failingBody) {
		t.Fatal("DLQ did not preserve message identity/body")
	}
	if retryAttempt(failed.Headers) != 4 {
		t.Fatalf("DLQ attempt=%d want 4", retryAttempt(failed.Headers))
	}
	if time.Since(start) < 7*time.Second-250*time.Millisecond {
		t.Fatal("retry backoff did not wait 1+2+4 seconds")
	}
	var inboxRows int
	if err := db.QueryRow(`SELECT count(*) FROM parameterization.event_inbox WHERE event_id=$1`, failingEvent.EventID).Scan(&inboxRows); err != nil {
		t.Fatal(err)
	}
	if inboxRows != 0 || projectionCount(db, failingID) != 0 {
		t.Fatal("failed projection transaction left an inbox key/effect")
	}
	if err := failed.Nack(false, true); err != nil {
		t.Fatal(err)
	}
	dropFailure()
	count, err := ReplayDLQ(ctx, url, 1)
	if err != nil || count != 1 {
		t.Fatalf("manual replay count=%d err=%v", count, err)
	}
	r1Wait(t, "manual replay applied", func() bool { return projectionCount(db, failingID) == 1 })
	t.Log("RETRY/DLQ PASS: 1 initial attempt + retries at 1s/2s/4s; inbox rollback; manual replay preserved event_id and applied once")
	// The original concurrent event remains exactly once after an actual AMQP
	// redelivery, covering the commit-before-ACK crash window.
	if err := publishConfirmed(ctx, url, referenceExchange, event.EventType, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: event.EventID, Type: event.EventType, Body: body}); err != nil {
		t.Fatal(err)
	}
}

func r1Prefix(t *testing.T) string {
	t.Helper()
	id := os.Getenv("R1_RUN_ID")
	if len(id) != 12 || strings.ContainsAny(id, "%_'\" ") {
		t.Fatal("R1_RUN_ID must be the 12-character runner identifier")
	}
	return "R1C_" + id + "_"
}

func r1CreateCatalog(t *testing.T, code string) string {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	login, _ := json.Marshal(map[string]string{"email": os.Getenv("HU_REF_020_ADMIN_EMAIL"), "password": os.Getenv("HU_REF_020_ADMIN_PASSWORD")})
	response, err := client.Post(os.Getenv("HU_REF_020_IAM_BASE_URL")+"/api/v1/auth/login", "application/json", bytes.NewReader(login))
	if err != nil {
		t.Fatal(err)
	}
	var auth struct {
		Token string `json:"access_token"`
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		t.Fatalf("IAM login HTTP %d", response.StatusCode)
	}
	err = json.NewDecoder(response.Body).Decode(&auth)
	response.Body.Close()
	if err != nil || auth.Token == "" {
		t.Fatal("IAM login returned no access token")
	}
	body, _ := json.Marshal(map[string]string{"code": code, "name": "R1 automated chaos fixture"})
	request, _ := http.NewRequest(http.MethodPost, os.Getenv("HU_REF_020_REFERENCE_DATA_BASE_URL")+"/catalogs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+auth.Token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("create catalog HTTP %d", response.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.ID == "" {
		t.Fatal("catalog ID missing")
	}
	return result.ID
}

func TestR1ChaosBaseline(t *testing.T) {
	db := r1Database(t)
	prefix := r1Prefix(t)
	for i := 0; i < 3; i++ {
		id := r1CreateCatalog(t, fmt.Sprintf("%sbaseline_%d", prefix, i))
		r1Wait(t, "baseline event", func() bool { return projectionCount(db, id) == 1 })
	}
	t.Log("BASELINE PASS: 3 real HTTP catalog writes -> 3 outbox events -> 3 effects")
}

func TestR1ChaosDuringOutage(t *testing.T) {
	db := r1Database(t)
	prefix := r1Prefix(t)
	url := os.Getenv("REFERENCE_DATA_AMQP_URL")
	if conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(time.Second)}); err == nil {
		conn.Close()
		t.Fatal("chaos test requires broker actually stopped")
	}
	for i := 0; i < 3; i++ {
		id := r1CreateCatalog(t, fmt.Sprintf("%soutage_%d", prefix, i))
		if projectionCount(db, id) != 0 {
			t.Fatal("effect appeared while broker was stopped")
		}
	}
	var business, outbox int
	if err := db.QueryRow(`SELECT count(*),count(o.id) FROM parameterization.catalog c LEFT JOIN parameterization.outbox_event o ON o.aggregate_id=c.id::text WHERE left(c.code,length($1))=$1`, prefix).Scan(&business, &outbox); err != nil {
		t.Fatal(err)
	}
	if business != 6 || outbox != 6 {
		t.Fatalf("during outage business=%d outbox=%d want 6/6", business, outbox)
	}
	t.Log("OUTAGE PASS: broker unreachable; 3 additional HTTP writes succeeded; all 6 business rows have committed outbox events")
}

func TestR1ChaosRecovery(t *testing.T) {
	db := r1Database(t)
	prefix := r1Prefix(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	type stored struct {
		id, eventID, eventType string
		payload                []byte
		created                time.Time
	}
	rows, err := db.Query(`SELECT c.id,o.event_id,o.event_type,o.payload,o.created_at FROM parameterization.catalog c JOIN parameterization.outbox_event o ON o.aggregate_id=c.id::text WHERE left(c.code,length($1))=$1 ORDER BY c.code`, prefix)
	if err != nil {
		t.Fatal(err)
	}
	var events []stored
	for rows.Next() {
		var e stored
		if err := rows.Scan(&e.id, &e.eventID, &e.eventType, &e.payload, &e.created); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(events) != 6 {
		t.Fatalf("persisted events=%d want 6", len(events))
	}
	for _, e := range events {
		e := e
		r1Wait(t, "event recovery "+e.eventID, func() bool { return projectionCount(db, e.id) == 1 })
	}
	publisher := NewAMQPPublisher(os.Getenv("REFERENCE_DATA_AMQP_URL"))
	for _, e := range events {
		for i := 0; i < 2; i++ {
			if err := publisher.PublishPersisted(ctx, persistence.OutboxEvent{EventID: uuid.MustParse(e.eventID), EventType: e.eventType, Payload: e.payload, CreatedAt: e.created}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// One consumer/QoS 1: observing this later event is a barrier proving the
	// twelve prior duplicate deliveries were processed, not merely published.
	fence := r1CreateCatalog(t, "R1_FENCE_"+os.Getenv("R1_RUN_ID"))
	r1Wait(t, "post-duplicate FIFO barrier", func() bool { return projectionCount(db, fence) == 1 })
	var published, inbox, effects, applied int
	for _, e := range events {
		var status string
		if err := db.QueryRow(`SELECT status FROM parameterization.outbox_event WHERE event_id=$1`, e.eventID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == persistence.OutboxPublished {
			published++
		}
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM parameterization.event_inbox WHERE event_id=$1`, e.eventID).Scan(&n)
		inbox += n
		n = projectionCount(db, e.id)
		if n > 0 {
			effects++
		}
		applied += n
	}
	if published != 6 || inbox != 6 || effects != 6 || applied != 6 {
		t.Fatalf("recovery published=%d inbox=%d effects=%d applied=%d want 6 each", published, inbox, effects, applied)
	}
	t.Log("CHAOS PASS: 6 committed events, 6 published, 6 inbox records, 6 effects; broker stopped mid-flow and service restarted; 12 deliberate duplicates; lost=0 duplicated_effects=0")
}
