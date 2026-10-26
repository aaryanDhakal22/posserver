package observability

import (
	"context"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestLatencySeconds(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	cases := []struct {
		name     string
		from, to time.Time
		want     float64
		ok       bool
	}{
		{"forward", t0, t0.Add(1500 * time.Millisecond), 1.5, true},
		{"skew clamps to zero", t0.Add(3 * time.Second), t0, 0, true},
		{"unknown start", time.Time{}, t0, 0, false},
		{"unknown end", t0, time.Time{}, 0, false},
	}
	for _, c := range cases {
		got, ok := LatencySeconds(c.from, c.to)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: LatencySeconds = (%v, %v), want (%v, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func histogramPoints(t *testing.T, r *sdkmetric.ManualReader, name string) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if m.Unit != "s" {
				t.Errorf("%s unit = %q, want s", name, m.Unit)
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s is %T, not a float64 histogram", name, m.Data)
			}
			return h.DataPoints
		}
	}
	return nil
}

func TestRecordLatencyUsesHopBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h, err := NewHopHistogram(mp.Meter("test"), "orders.ack.latency", "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t0 := time.Unix(1_000_000, 0)
	store := metric.WithAttributes(attribute.String(AttrTenant, "lombardis"), attribute.String(AttrLocation, "main"))

	if !RecordLatency(ctx, h, t0, t0.Add(200*time.Millisecond), store) {
		t.Fatal("forward sample not recorded")
	}
	if !RecordLatency(ctx, h, t0.Add(time.Minute), t0, store) { // skew → 0
		t.Fatal("skewed sample not recorded")
	}
	if RecordLatency(ctx, h, time.Time{}, t0, store) {
		t.Fatal("unknown start recorded")
	}
	if RecordLatency(ctx, nil, t0, t0) {
		t.Fatal("nil histogram recorded")
	}

	pts := histogramPoints(t, reader, "orders.ack.latency")
	if len(pts) != 1 {
		t.Fatalf("got %d data points, want 1 (one store series)", len(pts))
	}
	p := pts[0]
	if p.Count != 2 {
		t.Errorf("count = %d, want 2", p.Count)
	}
	if !slices.Equal(p.Bounds, HopBuckets) {
		t.Errorf("bounds = %v, want HopBuckets %v", p.Bounds, HopBuckets)
	}
	if v, ok := p.Min.Value(); !ok || v != 0 {
		t.Errorf("min = %v, want 0 (clamped)", v)
	}
	if v, ok := p.Max.Value(); !ok || v < 0.19 || v > 0.21 {
		t.Errorf("max = %v, want 0.2", v)
	}
	if te, _ := p.Attributes.Value(AttrTenant); te.AsString() != "lombardis" {
		t.Errorf("tenant = %q", te.AsString())
	}
}

func TestHopBucketsCoverSubSecondToThirtySeconds(t *testing.T) {
	if !slices.IsSorted(HopBuckets) {
		t.Fatalf("HopBuckets not sorted: %v", HopBuckets)
	}
	for _, b := range []float64{0.05, 1, 30} {
		if !slices.Contains(HopBuckets, b) {
			t.Errorf("HopBuckets missing %v", b)
		}
	}
}
