package observability

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// sums collects every Int64 sum / gauge point by instrument name.
func sums(t *testing.T, r *sdkmetric.ManualReader) map[string][]metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string][]metricdata.DataPoint[int64]{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				out[m.Name] = append(out[m.Name], d.DataPoints...)
			case metricdata.Gauge[int64]:
				out[m.Name] = append(out[m.Name], d.DataPoints...)
			case metricdata.Histogram[int64]:
				for _, p := range d.DataPoints {
					out[m.Name] = append(out[m.Name], metricdata.DataPoint[int64]{Attributes: p.Attributes, Value: int64(p.Count)})
				}
			}
		}
	}
	return out
}

func attr(p metricdata.DataPoint[int64], k string) string {
	v, _ := p.Attributes.Value(attribute.Key(k))
	return v.Emit()
}

// L-3 + CM-11 / CM-13 / CM-14: every money instrument records with bounded
// attributes (unknown tenders / actions fold into "unknown" / "other").
func TestMoneyInstruments(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	moneyOnce.Do(func() {})
	moneyInst = buildMoney(mp.Meter("test"))
	var m Meters
	ctx, loc := context.Background(), uuid.New()

	m.RecordCardHold(ctx, loc, "captured", "capture", 2300)
	m.RecordCardHold(ctx, loc, "failed", "void", 0)
	m.RecordCardHold(ctx, loc, "refunded", "refund", 900)
	m.RecordCounterPayment(ctx, loc, "cash", 1000, 200)
	m.RecordCounterPayment(ctx, loc, "card", -500, 0)
	m.RecordCounterPayment(ctx, loc, "bitcoin", 0, 300)
	m.RecordJournal(ctx, loc, "refund", "cashier")
	m.RecordJournal(ctx, loc, "something_new", "")
	m.RecordDrawerClosed(ctx, loc, "counted", false, -50)
	diff := int64(-1000)
	m.RecordZ(ctx, loc, 0, &diff)
	m.RecordMoneyFlag(ctx, loc, "order.payment", "overpaid")

	got := sums(t, reader)
	want := map[string]int64{"payments.captured.cents": 2300, "payments.refunded.cents": 900, "card_holds.failed": 1,
		"counter.tips.cents": 500, "money.flags": 1, "dayclose.z.stored": 1, "dayclose.drawer.over_short.cents": 1}
	for name, v := range want {
		var total int64
		for _, p := range got[name] {
			total += p.Value
		}
		if total != v {
			t.Errorf("%s = %d, want %d (%v)", name, total, v, got[name])
		}
	}
	kinds := map[string]string{}
	for _, p := range got["counter.payments"] {
		kinds[attr(p, "kind")] = attr(p, "tender")
	}
	if kinds["sale"] != "cash" || kinds["refund"] != "card" || kinds["tip"] != "unknown" {
		t.Errorf("counter.payments kinds: %v", kinds)
	}
	actions := map[string]bool{}
	for _, p := range got["counter.journal.entries"] {
		actions[attr(p, "action")+"/"+attr(p, "role")] = true
	}
	if !actions["refund/cashier"] || !actions["other/none"] {
		t.Errorf("journal: %v", actions)
	}
	for _, p := range got["dayclose.z.stored"] {
		if attr(p, "balanced") != "false" {
			t.Errorf("a Z main disagrees with is not balanced: %v", p.Attributes)
		}
	}
	srcs := map[string]int64{}
	for _, p := range got["dayclose.z.difference.cents"] {
		srcs[attr(p, "source")] = p.Value
	}
	if srcs["pi"] != 0 || srcs["main"] != -1000 {
		t.Errorf("z difference: %v", srcs)
	}
}
