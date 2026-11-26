// Package telemetry provides optional, privacy-preserving OpenTelemetry trace
// export to Pydantic Logfire. When LOGFIRE_TOKEN is unset or empty, no
// exporter is configured, no network connections are made, and tracing is a
// zero-cost no-op.
package telemetry

import (
	"context"
	"fmt"
	"os"
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

// TokenPresent reports whether a Logfire write token is non-empty after trimming.
func TokenPresent(v string) bool { return strings.TrimSpace(v) != "" }

// Enabled reports whether telemetry export is requested by the environment.
func Enabled() bool {
	if !TokenPresent(os.Getenv("LOGFIRE_TOKEN")) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOGFIRE_SEND_TO_LOGFIRE"))) {
	case "no", "false", "0":
		return false
	}
	return true
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

func endpointURL(token, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		if strings.HasPrefix(token, "pylf_v1_eu_") {
			base = "https://logfire-eu.pydantic.dev"
		} else {
			base = "https://logfire-us.pydantic.dev"
		}
	}
	return strings.TrimRight(base, "/") + "/v1/traces"
}

// Init configures OpenTelemetry export to Logfire when Enabled() is true.
// Configuration failures are reported once on stderr and never abort the CLI.
func Init(ctx context.Context) *Session {
	if !Enabled() {
		return &Session{}
	}
	token := strings.TrimSpace(os.Getenv("LOGFIRE_TOKEN"))
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpointURL(token, os.Getenv("LOGFIRE_BASE_URL"))),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": token}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: logfire disabled: %v\n", err)
		return &Session{}
	}
	svc := strings.TrimSpace(os.Getenv("LOGFIRE_SERVICE_NAME"))
	if svc == "" {
		svc = "bskyban"
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(svc)}
	if env := strings.TrimSpace(os.Getenv("LOGFIRE_ENVIRONMENT")); env != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentName(env))
	}
	res := resource.NewWithAttributes(semconv.SchemaURL, attrs...)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	// Suppress SDK export errors so an unreachable collector never draws over the TUI.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	otel.SetTracerProvider(tp)
	return &Session{tp: tp}
}
