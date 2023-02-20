package config

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"time"

	"go.opentelemetry.io/contrib/detectors/aws/ec2"
	"go.opentelemetry.io/contrib/detectors/aws/ecs"
	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
)

func configureTracing(serviceCtx context.Context, logger *zap.Logger) error {
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
		trace.WithIDGenerator(xray.NewIDGenerator()), // Generate xray compatible trace IDs
		trace.WithResource(ec2Resource),
		trace.WithResource(ecsResource),
	)
	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(xray.Propagator{})

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
		logger.Error("Failed to flush telemetry data", zap.Error(err))
	}(serviceCtx)

	return nil
}
