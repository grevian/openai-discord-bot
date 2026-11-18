package config

import (
	"context"
	goruntime "runtime"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestParseStatmRSS(t *testing.T) {
	got, err := parseStatmRSS("12345 678 90 1 0 2 0\n", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(678 * 4096); got != want {
		t.Errorf("rss = %d, want %d", got, want)
	}
	for _, bad := range []string{"", "12345", "12345 x 1"} {
		if _, err := parseStatmRSS(bad, 4096); err == nil {
			t.Errorf("parseStatmRSS(%q): expected an error", bad)
		}
	}
}

// The memory metrics the Honeycomb board relies on must be collected.
func TestMemoryMetricsAreCollected(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	if err := startMemoryMetrics(provider); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	values := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					values[m.Name] += p.Value
				}
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					values[m.Name] += p.Value
				}
			default:
				values[m.Name] += 0
			}
		}
	}

	for _, name := range []string{"go.memory.used", "go.memory.allocated", "go.goroutine.count"} {
		if values[name] <= 0 {
			t.Errorf("%s = %d, want > 0 (collected: %v)", name, values[name], values)
		}
	}
	if goruntime.GOOS == "linux" && values["process.memory.usage"] <= 0 {
		t.Errorf("process.memory.usage = %d, want > 0 on linux", values["process.memory.usage"])
	}
}
