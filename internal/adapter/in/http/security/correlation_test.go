package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/code-sena/design-software-reference-data-service/internal/platform/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestCorrelationPreservesRequestID(t *testing.T) {
	handler := Correlation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := CorrelationID(r.Context())
		if !ok || id != "trace-123" {
			t.Fatalf("unexpected correlation id: %q, %v", id, ok)
		}
		WriteError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid request")
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("X-Request-Id", "trace-123")
	handler.ServeHTTP(recorder, request)

	if recorder.Header().Get("X-Request-Id") != "trace-123" {
		t.Fatalf("request id was not returned: %q", recorder.Header().Get("X-Request-Id"))
	}
	if recorder.Body.String() == "" || !strings.Contains(recorder.Body.String(), "trace-123") {
		t.Fatalf("request id was not included in error envelope: %s", recorder.Body.String())
	}
}

func TestCorrelationGeneratesRequestID(t *testing.T) {
	handler := Correlation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := CorrelationID(r.Context()); !ok || id == "" {
			t.Fatal("expected generated correlation id")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected generated request id response header")
	}
}

// TestComposedStackCorrelationConsistency verifies that when no X-Request-Id is
// sent, the response header, error envelope trace_id, and context correlation_id
// all match the same value (the OTel trace_id).
func TestComposedStackCorrelationConsistency(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(noop.NewTracerProvider())
	})

	var contextCorrelation, headerCorrelation, envelopeCorrelation string
	handler := observability.HTTP(Correlation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextCorrelation = RequestCorrelationID(r)
		WriteError(w, r, http.StatusBadRequest, "BAD_REQUEST", "test error")
	})))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(recorder, request)

	headerCorrelation = recorder.Header().Get("X-Request-Id")
	var envelope struct {
		Error struct {
			TraceID string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	envelopeCorrelation = envelope.Error.TraceID

	if headerCorrelation == "" {
		t.Fatal("expected response header correlation id")
	}
	if contextCorrelation != headerCorrelation {
		t.Fatalf("context and header correlation differ: context=%q header=%q", contextCorrelation, headerCorrelation)
	}
	if envelopeCorrelation != headerCorrelation {
		t.Fatalf("envelope and header correlation differ: envelope=%q header=%q", envelopeCorrelation, headerCorrelation)
	}
}
