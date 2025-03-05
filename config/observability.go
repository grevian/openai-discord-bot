package config

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/detectors/aws/ec2"
	"go.opentelemetry.io/contrib/detectors/aws/ecs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	otellog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
)

func configureTracing(serviceCtx context.Context) error {
	// Configure a traceExporter to write to a sidecar collector
	traceExporter := otlptracegrpc.NewUnstarted(
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint("0.0.0.0:4317"),
		otlptracegrpc.WithDialOption(grpc.WithBlock()),
	)

	// Configure some resource detection to get data about our operating environment
	detectionCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	ec2ResourceDetector := ec2.NewResourceDetector()
	ec2Resource, _ := ec2ResourceDetector.Detect(detectionCtx)
	ecsResourceDetector := ecs.NewResourceDetector()
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
	err := traceExporter.Start(detectionCtx)
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

func configureLogging() error {
	var err error
	var logger *slog.Logger
	if viper.GetBool("JSON_LOGS") {
		// TODO Lots of warnings and caveats about using this in production, but I think since we're
		//  logging via ECS -> Cloudwatch -> Firehouse -> Honeycomb it's the right thing to do, If we want to though,
		//  we are running a collector sidecar and could log over it via otlp which is much better supported
		exp, err := stdoutlog.New()
		if err != nil {
			return err
		}
		provider := otellog.NewLoggerProvider(otellog.WithProcessor(otellog.NewSimpleProcessor(exp)))
		global.SetLoggerProvider(provider)

		logger = otelslog.NewLogger("openai-discord-bot")
	} else {
		logger = slog.Default()
	}
	slog.SetDefault(logger)

	return err
}
