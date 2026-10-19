package observability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/platform/correlation"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

const serviceName = "reference-data-service"

// requestIDHeader is the platform correlation header shared with IAM.
const requestIDHeader = "X-Request-Id"

// CorrelationAttribute exposes the request correlation identifier on the HTTP
// span, so the collector can join a trace with the correlation_id carried by
// the events published during that request.
const CorrelationAttribute = "correlation_id"

func Tracer() trace.Tracer { return otel.Tracer(serviceName) }

// Setup configures an OTLP/HTTP exporter only when OTEL_EXPORTER_OTLP_ENDPOINT is set.
// Without it, the SDK uses a noop provider, which keeps local unit tests offline.
func Setup(ctx context.Context) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	options := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := Tracer().Start(ctx, r.Method+" "+r.URL.Path)
		defer span.End()
		correlationID := resolveCorrelationID(r, span)
		ctx = correlation.WithID(ctx, correlationID)
		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", r.URL.Path),
			attribute.String(CorrelationAttribute, correlationID),
		)
		writer := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r.WithContext(ctx))
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.RecordError(fmt.Errorf("http status %d", status))
		}
	})
}

// resolveCorrelationID owns the single decision for the request identifier, so
// the response header, the error envelope and the published events cannot
// diverge. The caller's X-Request-Id wins; otherwise the trace id of the root
// span is reused so correlation_id and trace_id are the same value. The UUID
// fallback only applies when no exporter is configured, because the noop
// provider does not produce a valid trace id.
func resolveCorrelationID(r *http.Request, span trace.Span) string {
	if id := strings.TrimSpace(r.Header.Get(requestIDHeader)); id != "" {
		return id
	}
	if spanContext := span.SpanContext(); spanContext.HasTraceID() {
		return spanContext.TraceID().String()
	}
	return correlation.NewID()
}

func Database(ctx context.Context, operation string, fn func() error) error {
	_, span := Tracer().Start(ctx, "postgres "+operation)
	defer span.End()
	span.SetAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.operation.name", operation))
	if err := fn(); err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

func ShutdownWithTimeout(shutdown func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return shutdown(ctx)
}
