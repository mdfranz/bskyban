// Package telemetry provides optional, privacy-preserving OpenTelemetry trace
// export (e.g. to Pydantic Logfire). Export is enabled only when both
// OTEL_EXPORTER_OTLP_ENDPOINT and OTEL_EXPORTER_OTLP_HEADERS are set and
// non-empty; otherwise no exporter is configured, no network connections are
// made, and tracing is a zero-cost no-op.
package telemetry

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName  = "github.com/mdfranz/bskyban"
	flushBudget = 3 * time.Second
)

// Enabled reports whether OTLP trace export is configured in the environment.
// Both OTEL_EXPORTER_OTLP_ENDPOINT and OTEL_EXPORTER_OTLP_HEADERS must be set.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" &&
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")) != ""
}

// ClassifyOutcome maps an error to a fixed, coarse outcome label so raw
// server-supplied or user-identifying error text is never exported.
func ClassifyOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "operation_failed"
}

// Tracer returns the package tracer (no-op when telemetry is not active).
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// Session holds an active OpenTelemetry TracerProvider for the process lifetime.
type Session struct {
	tp *sdktrace.TracerProvider
}

// Active reports whether spans are being exported.
func (s *Session) Active() bool { return s != nil && s.tp != nil }

// Shutdown flushes pending spans within flushBudget so an unreachable collector
// never stalls process exit.
func (s *Session) Shutdown() {
	if s == nil || s.tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushBudget)
	defer cancel()
	_ = s.tp.Shutdown(ctx)
}

func buildResource(ctx context.Context) (*resource.Resource, error) {
	svc := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if svc == "" {
		svc = "bskyban"
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(svc)}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
			attrs = append(attrs, semconv.ServiceVersion(v))
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && strings.TrimSpace(s.Value) != "" {
				attrs = append(attrs, attribute.String("vcs.revision", s.Value))
				break
			}
		}
	}
	return resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(attrs...),
	)
}

// Init configures OTLP/HTTP trace export from standard OpenTelemetry
// environment variables when Enabled() is true. Callers should treat a non-nil
// error as non-fatal and continue without telemetry.
func Init(ctx context.Context) (*Session, error) {
	if !Enabled() {
		return &Session{}, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return &Session{}, err
	}
	res, err := buildResource(ctx)
	if err != nil {
		return &Session{}, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	// Suppress background export errors so an unreachable collector never draws over the TUI.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	otel.SetTracerProvider(tp)
	return &Session{tp: tp}, nil
}
