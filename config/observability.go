package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/detectors/aws/ec2/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	otellog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/trace"
)

var ec2ResourceDetector = ec2.NewResourceDetector()

func configureTracing(serviceCtx context.Context) error {
	// Configure a traceExporter to write to a sidecar collector
	traceExporter, err := otlptracegrpc.New(
		context.Background(),
	)
	if err != nil {
		return fmt.Errorf("failed to create new OTLP trace exporter: %w", err)
	}

	// Configure some resource detection to get data about our operating environment
	detectionCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	ec2Resource, _ := ec2ResourceDetector.Detect(detectionCtx)

	// Configure a trace provider and set it as the global default
	traceProvider := trace.NewTracerProvider(
		trace.WithSampler(trace.AlwaysSample()),
		trace.WithBatcher(traceExporter),
		trace.WithResource(ec2Resource),
	)
	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Try to ensure any pending telemetry gets flushed when the service context terminates
	flushWG.Add(1)
	go func(lifecycleContext context.Context) {
		defer flushWG.Done()
		<-lifecycleContext.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), flushDeadline)
		defer cancel()
		// otlptracegrpc.New already started the exporter; shutting down the provider flushes
		// pending spans and then shuts the exporter down too.
		if err := traceProvider.Shutdown(shutdownCtx); err != nil {
			slog.Default().Error("Failed to flush telemetry data", slog.Any("error", err))
		}
	}(serviceCtx)

	return nil
}

// flushDeadline is each exporter's own flush deadline.
const flushDeadline = 2 * time.Second

// flushWG tracks the telemetry flushes started when the service context ends.
var flushWG sync.WaitGroup

// WaitFlushed blocks until the telemetry flushes triggered by cancelling the
// service context have finished, or ctx expires. With no exporters configured
// it returns immediately.
func WaitFlushed(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		flushWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var programLevel = new(slog.LevelVar)

// configureLogging sets up the logging system for the service, either local text, local json, or otlp based opentelemetry logging,
// through the slog package, based on configuration from viper settings and OTLP environment variables
func configureLogging(serviceCtx context.Context) error {
	var err error
	var logger *slog.Logger
	if viper.GetBool("OTLP_LOGS") {
		exp, err := otlploggrpc.New(context.Background())
		if err != nil {
			return err
		}

		// Configure some resource detection to get data about our operating environment
		detectionCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		ec2Resource, _ := ec2ResourceDetector.Detect(detectionCtx)

		provider := otellog.NewLoggerProvider(
			otellog.WithProcessor(otellog.NewBatchProcessor(exp)),
			otellog.WithResource(ec2Resource),
		)
		otel.SetLoggerProvider(provider)

		logger = otelslog.NewLogger("openai-discord-bot")

		// Rather than trying to return a shutdown function to main, call it based on the serviceContext being terminated
		flushWG.Add(1)
		go func(lifecycleContext context.Context) {
			defer flushWG.Done()
			<-lifecycleContext.Done()
			// The service context is terminated so we want to flush anything we can quickly before the service is terminated
			shutdownCtx, cancel := context.WithTimeout(context.Background(), flushDeadline)
			defer cancel()
			if err := exp.Shutdown(shutdownCtx); err != nil {
				logger.ErrorContext(serviceCtx, "Failed to flush logging data", slog.Any("error", err))
			}
		}(serviceCtx)
	} else if viper.GetBool("JSON_LOGS") {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: programLevel}))
	} else {
		logger = slog.Default()
	}

	if viper.GetBool("DEBUG_LOGS") {
		programLevel.Set(slog.LevelDebug)
	}

	slog.SetDefault(logger)
	return err
}
