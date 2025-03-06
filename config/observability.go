package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/detectors/aws/ec2"
	"go.opentelemetry.io/contrib/detectors/aws/ecs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	otellog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/trace"
)

var ec2ResourceDetector = ec2.NewResourceDetector()
var ecsResourceDetector = ecs.NewResourceDetector()

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
	ecsResource, _ := ecsResourceDetector.Detect(detectionCtx)

	// Configure a trace provider and set it as the global default
	traceProvider := trace.NewTracerProvider(
		trace.WithSampler(trace.AlwaysSample()),
		trace.WithBatcher(traceExporter),
		trace.WithResource(ec2Resource),
		trace.WithResource(ecsResource),
	)
	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Use a context.Background here to allow us an extra few seconds to flush data once the
	// service context itself does terminate
	err = traceExporter.Start(detectionCtx)
	if err != nil {
		return fmt.Errorf("failed to create new OTLP trace exporter: %w", err)
	}

	// Try to ensure any pending telemetry gets flushed when the service context terminates
	go func(lifecycleContext context.Context) {
		<-lifecycleContext.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second*2)
		defer cancel()
		err := traceExporter.Shutdown(shutdownCtx)
		slog.Default().ErrorContext(serviceCtx, "Failed to flush telemetry data", slog.Any("error", err))
	}(serviceCtx)

	return nil
}

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
		ecsResource, _ := ecsResourceDetector.Detect(detectionCtx)

		provider := otellog.NewLoggerProvider(
			otellog.WithProcessor(otellog.NewBatchProcessor(exp)),
			otellog.WithResource(ec2Resource),
			otellog.WithResource(ecsResource),
		)
		global.SetLoggerProvider(provider)

		logger = otelslog.NewLogger("openai-discord-bot")

		// Rather than trying to return a shutdown function to main, call it based on the serviceContext being terminated
		go func(lifecycleContext context.Context) {
			<-lifecycleContext.Done()
			// The service context is terminated so we want to flush anything we can quickly before the service is terminated
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			defer cancel()
			err := exp.Shutdown(shutdownCtx)
			logger.ErrorContext(serviceCtx, "Failed to flush logging data", slog.Any("error", err))
		}(serviceCtx)
	} else if viper.GetBool("JSON_LOGS") {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	} else {
		logger = slog.Default()
	}

	slog.SetDefault(logger)
	return err
}
