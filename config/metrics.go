package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// metricsInterval is how often metrics are exported. It matches the host
// metrics interval of the collector on the instance.
const metricsInterval = time.Minute

// configureMetrics exports the bot's own memory metrics over OTLP to the
// collector on the instance (OTEL_EXPORTER_OTLP_* configure the endpoint,
// as for traces and logs):
//
//   - the Go runtime metrics: go.memory.used, go.memory.limit,
//     go.memory.allocated, go.memory.gc.goal, go.goroutine.count, ...
//   - process.memory.usage: the process's resident set size (RSS), which is
//     what the host sees, including memory the Go heap does not count.
func configureMetrics(serviceCtx context.Context) error {
	exporter, err := otlpmetricgrpc.New(context.Background())
	if err != nil {
		return fmt.Errorf("failed to create new OTLP metric exporter: %w", err)
	}

	detectionCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	ec2Resource, _ := ec2ResourceDetector.Detect(detectionCtx)

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(metricsInterval))),
		sdkmetric.WithResource(ec2Resource),
	)
	otel.SetMeterProvider(provider)
	if err := startMemoryMetrics(provider); err != nil {
		return err
	}

	flushWG.Add(1)
	go func(lifecycleContext context.Context) {
		defer flushWG.Done()
		<-lifecycleContext.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), flushDeadline)
		defer cancel()
		// Shutting down the provider exports what is pending and then shuts
		// the exporter down too.
		if err := provider.Shutdown(shutdownCtx); err != nil {
			slog.Default().Error("Failed to flush metrics", slog.Any("error", err))
		}
	}(serviceCtx)

	return nil
}

// startMemoryMetrics registers the Go runtime metrics and the process RSS
// gauge on provider.
func startMemoryMetrics(provider metric.MeterProvider) error {
	if err := runtime.Start(runtime.WithMeterProvider(provider)); err != nil {
		return fmt.Errorf("failed to start runtime metrics: %w", err)
	}
	meter := provider.Meter("openai-discord-bot")
	_, err := meter.Int64ObservableGauge("process.memory.usage",
		metric.WithDescription("The amount of physical memory in use (resident set size)."),
		metric.WithUnit("By"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			rss, err := readRSS()
			if err != nil {
				// Not Linux, or /proc is unreadable: report nothing.
				return nil
			}
			o.Observe(rss)
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to register the RSS gauge: %w", err)
	}
	return nil
}

// readRSS returns this process's resident set size in bytes, from
// /proc/self/statm (Linux only).
func readRSS() (int64, error) {
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	return parseStatmRSS(string(statm), os.Getpagesize())
}

// parseStatmRSS reads the resident page count, the second field of
// /proc/<pid>/statm, and converts it to bytes.
func parseStatmRSS(statm string, pageSize int) (int64, error) {
	fields := strings.Fields(statm)
	if len(fields) < 2 {
		return 0, errors.New("statm: too few fields")
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("statm: %w", err)
	}
	return pages * int64(pageSize), nil
}
