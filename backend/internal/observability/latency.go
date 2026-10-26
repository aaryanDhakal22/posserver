package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HopBuckets are the explicit histogram boundaries (seconds) for the
// machine hops of the order path (observability/TENANT-SCOPING.md §2):
// sub-second resolution up to 30 s, plus a 1 / 5 / 30 min tail so an outage
// backlog (agent offline, SQS redrive, a Pi syncing after an internet drop)
// lands in a real bucket instead of +Inf.
var HopBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 1800}

// NewHopHistogram creates a seconds histogram with HopBuckets.
func NewHopHistogram(m metric.Meter, name, desc string) (metric.Float64Histogram, error) {
	return m.Float64Histogram(name,
		metric.WithDescription(desc),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(HopBuckets...),
	)
}

// LatencySeconds is to − from in seconds, clamped at 0 (clock skew between
// the vendor / the Pi and main must not produce a negative sample). ok is
// false when either end is unknown (zero time): nothing should be recorded.
func LatencySeconds(from, to time.Time) (float64, bool) {
	if from.IsZero() || to.IsZero() {
		return 0, false
	}
	d := to.Sub(from).Seconds()
	if d < 0 {
		d = 0
	}
	return d, true
}

// RecordLatency records LatencySeconds(from, to) on h; a no-op when either
// end is unknown. Returns whether a sample was recorded.
func RecordLatency(ctx context.Context, h metric.Float64Histogram, from, to time.Time, opts ...metric.RecordOption) bool {
	if h == nil {
		return false
	}
	s, ok := LatencySeconds(from, to)
	if !ok {
		return false
	}
	h.Record(ctx, s, opts...)
	return true
}

// RegisterInfo publishes main.info{version, commit} = 1 (no store labels):
// which build every main instance runs, for the fleet / upgrade dashboard.
func RegisterInfo(version, commit string) error {
	m := otel.Meter(MeterName)
	g, err := m.Int64ObservableGauge("main.info",
		metric.WithDescription("Build info of the running main (always 1; attributes version, commit)"),
	)
	if err != nil {
		return err
	}
	attrs := metric.WithAttributes(attribute.String("version", version), attribute.String("commit", commit))
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(g, 1, attrs)
		return nil
	}, g)
	return err
}
