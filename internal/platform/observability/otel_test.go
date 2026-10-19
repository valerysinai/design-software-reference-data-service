package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/code-sena/design-software-reference-data-service/internal/platform/correlation"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// exportingProvider installs a real SDK provider, which is what a deployment
// with OTEL_EXPORTER_OTLP_ENDPOINT configured uses. No exporter is required:
// the provider already produces valid trace ids.
func exportingProvider(t *testing.T) {
	t.Helper()
	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(noop.NewTracerProvider())
	})
}

// capture records what the wrapped handler observed for the request.
func capture(t *testing.T, request *http.Request) (correlationID, traceID string) {
	t.Helper()
	handler := HTTP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		correlationID, _ = correlation.ID(r.Context())
		traceID = trace.SpanContextFromContext(r.Context()).TraceID().String()
	}))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	return correlationID, traceID
}

func TestHTTPReusesTraceIDAsCorrelationIDWithoutRequestHeader(t *testing.T) {
	exportingProvider(t)
	correlationID, traceID := capture(t, httptest.NewRequest(http.MethodGet, "/catalogs", nil))
	if correlationID == "" {
		t.Fatal("expected a correlation id in the request context")
	}
	if correlationID != traceID {
		t.Fatalf("correlation id must equal the trace id: correlation=%q trace=%q", correlationID, traceID)
	}
}

func TestHTTPPreservesCallerRequestID(t *testing.T) {
	exportingProvider(t)
	request := httptest.NewRequest(http.MethodGet, "/catalogs", nil)
	request.Header.Set("X-Request-Id", "hu009-qa-001")
	correlationID, traceID := capture(t, request)
	if correlationID != "hu009-qa-001" {
		t.Fatalf("caller request id was not preserved: %q", correlationID)
	}
	if traceID == "" {
		t.Fatal("expected an active span for the request")
	}
}

// Without an OTLP endpoint the provider is noop and yields no valid trace id,
// so the middleware must still produce a usable correlation id for local runs.
func TestHTTPFallsBackToGeneratedIDWithoutExporter(t *testing.T) {
	otel.SetTracerProvider(noop.NewTracerProvider())
	correlationID, _ := capture(t, httptest.NewRequest(http.MethodGet, "/catalogs", nil))
	if len(correlationID) != 36 {
		t.Fatalf("expected a generated UUID correlation id, got %q", correlationID)
	}
}
