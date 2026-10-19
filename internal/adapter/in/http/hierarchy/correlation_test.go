package hierarchy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	"github.com/code-sena/design-software-reference-data-service/internal/platform/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestCommandActorUsesContextCorrelationID verifies that commandActor reads
// the correlation identifier from the context (set by observability.HTTP)
// instead of re-reading the X-Request-Id header, so published events carry the
// same correlation_id as the response header and the OTel trace.
func TestCommandActorUsesContextCorrelationID(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(noop.NewTracerProvider())
	})

	writer := &correlationFakeWriter{item: json.RawMessage(`{"id":"new-id"}`)}
	commands := application.NewCommandService(writer)
	handler := NewHandler(nil, commands)

	mux := http.NewServeMux()
	security.RegisterProtectedRoutes(mux, correlationTestValidator{}, handler.Routes())

	stack := observability.HTTP(security.Correlation(mux))

	body := `{"name":"Nororiental","code":"NO"}`
	req := httptest.NewRequest(http.MethodPost, "/macroregions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer manager")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}

	headerCorrelation := rec.Header().Get("X-Request-Id")
	if headerCorrelation == "" {
		t.Fatal("expected X-Request-Id response header")
	}
	if writer.lastCorrelationID == nil {
		t.Fatal("command reached the writer without a correlation id")
	}
	if *writer.lastCorrelationID != headerCorrelation {
		t.Fatalf("event correlation_id diverges from response header: event=%q header=%q", *writer.lastCorrelationID, headerCorrelation)
	}
}

type correlationFakeWriter struct {
	item              json.RawMessage
	lastCorrelationID *string
}

func (w *correlationFakeWriter) Create(_ context.Context, _ string, command application.Command) (json.RawMessage, error) {
	w.lastCorrelationID = command.CorrelationID
	return w.item, nil
}
func (w *correlationFakeWriter) Update(_ context.Context, _ string, _ string, command application.Command) (json.RawMessage, error) {
	w.lastCorrelationID = command.CorrelationID
	return w.item, nil
}
func (w *correlationFakeWriter) Deactivate(_ context.Context, _ string, _ string, command application.Command, _ ...string) (json.RawMessage, error) {
	w.lastCorrelationID = command.CorrelationID
	return w.item, nil
}

type correlationTestValidator struct{}

func (correlationTestValidator) Validate(_ context.Context, token string) (security.Principal, error) {
	return security.Principal{
		ActorID:  "test-actor",
		Features: []string{"REF_HIERARCHY_MANAGE:GLOBAL", "REF_HIERARCHY_VIEW:GLOBAL", "REF_CATALOG_MANAGE:GLOBAL", "REF_CATALOG_VIEW:GLOBAL"},
	}, nil
}
