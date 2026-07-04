// Package tracing wires up (or deliberately doesn't wire up) OpenTelemetry
// distributed tracing for Conduit, per spec/09-observability.md §3. Every
// span Conduit creates elsewhere (internal/proxy, internal/auth,
// internal/ratelimit) goes through otel.Tracer("conduit"), so this
// package's only job is installing the right TracerProvider — a real
// OTLP-exporting one when observability.otel_endpoint is configured, or
// otel's built-in no-op provider (the default before Setup runs) when it
// isn't.
package tracing

import (
	"context"
	"fmt"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// Shutdown flushes and stops the tracer provider Setup installed. Calling
// it is always safe, even if tracing was never enabled (Setup returns a
// no-op Shutdown in that case).
type Shutdown func(context.Context) error

// Setup installs a TracerProvider based on cfg. If cfg.OTELEndpoint is
// empty, tracing stays disabled (otel's default no-op provider) and Setup
// returns immediately with a no-op Shutdown — every otel.Tracer(...).Start
// call elsewhere in Conduit remains cheap and safe either way.
func Setup(ctx context.Context, cfg *config.ObservabilityConfig, version string) (Shutdown, error) {
	if cfg.OTELEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTELEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("conduit"),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	samplingRate := cfg.TraceSamplingRate
	if samplingRate <= 0 {
		samplingRate = 1.0
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(samplingRate)),
	)
	otel.SetTracerProvider(tp)

	return func(shutdownCtx context.Context) error {
		ctx, cancel := context.WithTimeout(shutdownCtx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx)
	}, nil
}
