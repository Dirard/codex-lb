package sqlite

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// Every value maps into a bucket whose bounds contain it, buckets are ordered
// like their values, and values below 2^p have their own exact bucket.
func TestRollupHistBucketsCoverValuesInOrder(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	values := []int64{0, 1, 31, 32, 63, 64, 65, 100, 1023, 1024, 1 << 40, math.MaxInt64 - 1, math.MaxInt64}
	for range 20000 {
		values = append(values, random.Int64N(1<<uint(1+random.IntN(62))))
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	previous := -1
	for _, v := range values {
		index := rollupHistIndex(v)
		lower, width := rollupHistBounds(index)
		if v < lower || v-lower >= width || index < previous || index > maxRollupHistIndex {
			t.Fatalf("value %d: bucket %d [%d,+%d) after %d", v, index, lower, width, previous)
		}
		if v < 32 && (index != int(v) || width != 1) {
			t.Fatalf("small value %d is not exact: bucket %d width %d", v, index, width)
		}
		if v >= 32 && math.Abs(rollupHistRepresentative(index)-float64(v)) >= float64(v)/64 {
			t.Fatalf("value %d: representative %v outside 1/64", v, rollupHistRepresentative(index))
		}
		previous = index
	}
}

func randomRollupHist(random *rand.Rand, n int) rollupHist {
	var h rollupHist
	for range n {
		h.add(random.Int64N(1<<uint(1+random.IntN(40))), 1+random.Int64N(5))
	}
	return h
}

func TestRollupHistCodecRoundTripsAndRejectsDamage(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 4))
	for _, n := range []int{0, 1, 2, 17, 300} {
		h := randomRollupHist(random, n)
		decoded, err := decodeRollupHist(h.encode())
		if err != nil || !reflect.DeepEqual(decoded, h) && !(len(h) == 0 && len(decoded) == 0) {
			t.Fatalf("round trip of %d samples: %v %v", n, decoded, err)
		}
	}
	var extreme rollupHist
	extreme.add(math.MaxInt64, math.MaxInt64)
	extreme.add(0, 1)
	if decoded, err := decodeRollupHist(extreme.encode()); err != nil || !reflect.DeepEqual(decoded, extreme) {
		t.Fatalf("extreme round trip: %v %v", decoded, err)
	}
	if encoded := (rollupHist{}).encode(); encoded != nil {
		t.Fatalf("empty histogram encodes as %x, want NULL", encoded)
	}
	for _, damaged := range [][]byte{{0x80}, {1}, {1, 0}, {1, 1, 0, 1}, {0xff, 0xff, 0xff, 0x7f, 1}} {
		if _, err := decodeRollupHist(damaged); err == nil {
			t.Fatalf("damaged histogram %x decoded", damaged)
		}
	}
}

// Merging is commutative and associative, subtraction undoes it, and a
// subtraction of more than was merged shows as a negative count.
func TestRollupHistMergeAndSubtractAreExact(t *testing.T) {
	random := rand.New(rand.NewPCG(5, 6))
	for range 200 {
		a, b, c := randomRollupHist(random, random.IntN(50)), randomRollupHist(random, random.IntN(50)), randomRollupHist(random, random.IntN(50))
		left := a.merge(b, 1).merge(c, 1)
		right := a.merge(b.merge(c, 1), 1)
		if !reflect.DeepEqual(left, right) || !reflect.DeepEqual(a.merge(b, 1), b.merge(a, 1)) {
			t.Fatalf("merge is not associative or commutative")
		}
		if back := left.subtract(c).subtract(b); len(back) != len(a) || len(a) > 0 && !reflect.DeepEqual(back, a) {
			t.Fatalf("subtract does not undo merge: %v vs %v", back, a)
		}
		if len(b) > 0 && !a.subtract(a.merge(b, 1)).negative() {
			t.Fatalf("over-subtraction is not negative")
		}
		if left.negative() || left.total() != a.total()+b.total()+c.total() {
			t.Fatalf("merged totals %d", left.total())
		}
	}
}

// exactReportMedian is referenceReportMedian's rank rule without rounding.
func exactReportMedian(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	return (sorted[(n+1)/2-1] + sorted[(n+2)/2-1]) / 2
}

// exactQuantile is histQuantile's interpolation over exact values.
func exactQuantile(values []float64, q float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := q * float64(len(sorted)-1)
	low := int(rank)
	if low+1 >= len(sorted) {
		return sorted[low]
	}
	return sorted[low] + (sorted[low+1]-sorted[low])*(rank-float64(low))
}

// Quantiles from p0 to p100 stay within 1/64 of the exact interpolated
// quantile, are exact below 32, and the median is the 0.5-quantile.
func TestRollupHistQuantileWithinBound(t *testing.T) {
	random := rand.New(rand.NewPCG(11, 12))
	samples := map[string]func() int64{
		"lognormal": func() int64 { return int64(math.Exp(random.NormFloat64()*2 + 7)) },
		"uniform":   func() int64 { return random.Int64N(1 << 30) },
		"two-point": func() int64 { return []int64{40, 70000}[random.IntN(2)] },
		"below-32":  func() int64 { return random.Int64N(32) },
	}
	for name, sample := range samples {
		for _, n := range []int{1, 2, 7, 100, 999, 4000} {
			var h rollupHist
			values := make([]float64, n)
			for i := range values {
				v := sample()
				h.add(v, 1)
				values[i] = float64(v)
			}
			for _, q := range []float64{0, 0.01, 0.05, 0.25, 0.5, 0.75, 0.9, 0.95, 0.99, 1} {
				got, exact := histQuantile(h, q), exactQuantile(values, q)
				if math.Abs(got-exact) > exact/64+1e-9 || name == "below-32" && got != exact {
					t.Fatalf("%s n=%d q=%v: %v exact %v", name, n, q, got, exact)
				}
			}
			if h.median(1) != roundReport(histQuantile(h, 0.5), 2) || exactQuantile(values, 0.5) != exactReportMedian(values) {
				t.Fatalf("%s n=%d: median is not the 0.5-quantile", name, n)
			}
		}
	}
	if histQuantile(nil, 0.95) != 0 {
		t.Fatal("empty quantile is not 0")
	}
}

// Histogram medians stay within 1/64 of the exact median plus rounding for
// the distributions report latencies take (design §1.3); tokens per second
// add the floor of their stored hundredths.
func TestRollupHistMedianWithinBound(t *testing.T) {
	random := rand.New(rand.NewPCG(7, 8))
	distributions := map[string]func() int64{
		"lognormal": func() int64 { return int64(math.Exp(random.NormFloat64()*1.5 + 6)) },
		"uniform":   func() int64 { return random.Int64N(60000) },
		"constant":  func() int64 { return 777 },
		"two-point": func() int64 { return []int64{120, 9000}[random.IntN(2)] },
		"below-32":  func() int64 { return random.Int64N(32) },
		"zero":      func() int64 { return 0 },
	}
	for name, sample := range distributions {
		for _, n := range []int{1, 2, 3, 10, 101, 1000, 5000} {
			var h rollupHist
			values := make([]float64, n)
			for i := range values {
				v := sample()
				h.add(v, 1)
				values[i] = float64(v)
			}
			exact := exactReportMedian(values)
			if got := h.median(1); math.Abs(got-exact) > exact/64+0.005 {
				t.Fatalf("%s n=%d: median %v exact %v", name, n, got, exact)
			}
			if got := h.median(1); (name == "below-32" || name == "zero") && got != roundReport(exact, 2) {
				t.Fatalf("%s n=%d: small values not exact: %v vs %v", name, n, got, exact)
			}
			var tps rollupHist
			rates := make([]float64, n)
			for i := range rates {
				output, generation := 1+random.Int64N(4000), 1+sample()
				tps.add(rollupTPSSample(output, generation), 1)
				rates[i] = float64(output) * 1000.0 / float64(generation)
			}
			exact = exactReportMedian(rates)
			if got := tps.median(rollupTPSScale); math.Abs(got-exact) > exact/64+0.015 {
				t.Fatalf("%s n=%d: tps median %v exact %v", name, n, got, exact)
			}
		}
	}
	if (rollupHist{}).median(1) != 0 {
		t.Fatal("empty histogram median is not 0")
	}
}

// The raw report path's daily medians agree with the exact SQL median of
// referenceReportMedian within the bound, including rows the guards drop.
func TestReportMediansMatchReferenceWithinBound(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(9, 10))
	day := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for i := range 3000 {
		var ttft any
		if i%9 != 0 {
			ttft = int64(math.Exp(random.NormFloat64()+6)) - int64(i%50)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_events(request_id,model,status,requested_at,
 output_tokens,queue_latency_ms,total_latency_ms,latency_first_token_ms) VALUES(?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("median-%d", i), "m", "success", millis(day)+random.Int64N(3*86400000),
			random.Int64N(3)*random.Int64N(900), random.Int64N(70)-3, random.Int64N(30000), ttft); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.ReportFilter{Start: day, End: day.AddDate(0, 0, 3), PreviousStart: day.AddDate(0, 0, -3), Location: time.UTC}
	report, err := s.QueryReports(ctx, filter)
	if err != nil || len(report.Daily) != 3 {
		t.Fatalf("report: %d days %v", len(report.Daily), err)
	}
	for i, row := range report.Daily {
		start, end := day.AddDate(0, 0, i), day.AddDate(0, 0, i+1)
		for _, check := range []struct {
			name, expr, guard string
			got, extra        float64
		}{
			{"ttft", "e.latency_first_token_ms", "e.latency_first_token_ms>0", row.MedianTTFTMS, 0},
			{"queue", "e.queue_latency_ms", "e.queue_latency_ms>=0", row.MedianQueueMS, 0},
			{"tps", "e.output_tokens*1000.0/(e.total_latency_ms-e.latency_first_token_ms)",
				"e.output_tokens>0 AND e.latency_first_token_ms>0 AND e.total_latency_ms>e.latency_first_token_ms", row.MedianTPS, 0.01},
		} {
			want, err := s.referenceReportMedian(ctx, filter, start, end, check.expr, check.guard)
			if err != nil {
				t.Fatal(err)
			}
			if want == 0 || !withinHistogramMedian(check.got, want, check.extra) {
				t.Fatalf("%s %s: median %v, reference %v", row.Date, check.name, check.got, want)
			}
		}
	}
}

// Integer microdollars avoid the float sum's drift: many small raw costs add
// up to the exact decimal amount.
func TestReportCostSumsIntegerMicrodollars(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	day := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for i := range 1000 {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_events(request_id,model,status,requested_at,cost_microdollars)
 VALUES(?,'m','success',?,100001)`, fmt.Sprintf("cost-%d", i), millis(day)+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.QueryReports(ctx, domain.ReportFilter{Start: day, End: day.AddDate(0, 0, 1), PreviousStart: day.AddDate(0, 0, -1)})
	if err != nil || report.Summary.TotalCostUSD != 100.001 || report.Daily[0].CostUSD != 100.001 || report.ByModel[0].CostUSD != 100.001 {
		t.Fatalf("cost %+v %v", report.Summary, err)
	}
	if got := reportCostUSD(0, 0); got != 0 {
		t.Fatalf("empty cost %v", got)
	}
}
