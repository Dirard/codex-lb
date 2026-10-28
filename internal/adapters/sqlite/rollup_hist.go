package sqlite

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"sort"
)

// rollupHistPrecision is p of the log-linear histograms behind daily medians:
// 2^p sub-buckets per octave, so a representative value is within 1/64 of
// every value of its bucket and values below 2^p are exact. Rollup tables
// record it through usage_rollup_state.layout; another p needs a rebuild.
const rollupHistPrecision = 5

// rollupTPSScale stores tokens per second as floor(tps*100).
const rollupTPSScale = 100

var errRollupHistogram = errors.New("invalid usage rollup histogram")

// rollupHistBucket is one non-empty bucket. Deltas may carry negative counts;
// stored histograms never do.
type rollupHistBucket struct {
	index int
	count int64
}

// rollupHist is a histogram sorted by bucket index without zero counts. It is
// mergeable and subtractable, which the journal of deleted events needs.
type rollupHist []rollupHistBucket

// rollupHistIndex maps v>=0 to its bucket: v itself below 2^p, otherwise
// (e-p+1)<<p plus the p bits after the leading one, e being the octave.
func rollupHistIndex(v int64) int {
	if v < 1<<rollupHistPrecision {
		return int(max(v, 0))
	}
	e := bits.Len64(uint64(v)) - 1
	m := int(v >> (e - rollupHistPrecision))
	return (e-rollupHistPrecision+1)<<rollupHistPrecision + m - 1<<rollupHistPrecision
}

// rollupHistBounds returns the lowest value and the width of a bucket.
func rollupHistBounds(index int) (lower, width int64) {
	if index < 1<<rollupHistPrecision {
		return int64(index), 1
	}
	e := index>>rollupHistPrecision + rollupHistPrecision - 1
	m := int64(index&(1<<rollupHistPrecision-1) + 1<<rollupHistPrecision)
	shift := e - rollupHistPrecision
	return m << shift, 1 << shift
}

// rollupHistRepresentative is the middle of a bucket: lower + (width-1)/2.
func rollupHistRepresentative(index int) float64 {
	lower, width := rollupHistBounds(index)
	return float64(lower) + float64(width-1)/2
}

// add changes the count of v's bucket by n.
func (h *rollupHist) add(v, n int64) {
	h.addIndex(rollupHistIndex(v), n)
}

func (h *rollupHist) addIndex(index int, n int64) {
	if n == 0 {
		return
	}
	buckets := *h
	at := sort.Search(len(buckets), func(i int) bool { return buckets[i].index >= index })
	switch {
	case at < len(buckets) && buckets[at].index == index:
		buckets[at].count += n
		if buckets[at].count == 0 {
			buckets = append(buckets[:at], buckets[at+1:]...)
		}
	default:
		buckets = append(buckets, rollupHistBucket{})
		copy(buckets[at+1:], buckets[at:])
		buckets[at] = rollupHistBucket{index: index, count: n}
	}
	*h = buckets
}

// merge returns h + sign*other without changing either.
func (h rollupHist) merge(other rollupHist, sign int64) rollupHist {
	result := make(rollupHist, 0, len(h)+len(other))
	i, j := 0, 0
	for i < len(h) || j < len(other) {
		switch {
		case j == len(other) || i < len(h) && h[i].index < other[j].index:
			result = append(result, h[i])
			i++
		case i == len(h) || other[j].index < h[i].index:
			result = append(result, rollupHistBucket{other[j].index, sign * other[j].count})
			j++
		default:
			if count := h[i].count + sign*other[j].count; count != 0 {
				result = append(result, rollupHistBucket{h[i].index, count})
			}
			i, j = i+1, j+1
		}
	}
	return result
}

// subtract returns h - other.
func (h rollupHist) subtract(other rollupHist) rollupHist { return h.merge(other, -1) }

// negative reports a bucket with a negative count, which a stored histogram
// never has.
func (h rollupHist) negative() bool {
	for _, bucket := range h {
		if bucket.count < 0 {
			return true
		}
	}
	return false
}

func (h rollupHist) total() int64 {
	var n int64
	for _, bucket := range h {
		n += bucket.count
	}
	return n
}

// encode stores the buckets as (uvarint index delta, uvarint count) pairs;
// an empty histogram is NULL. Only histograms without negative counts encode.
func (h rollupHist) encode() []byte {
	if len(h) == 0 {
		return nil
	}
	out := make([]byte, 0, len(h)*3)
	previous := 0
	for _, bucket := range h {
		out = binary.AppendUvarint(out, uint64(bucket.index-previous))
		out = binary.AppendUvarint(out, uint64(bucket.count))
		previous = bucket.index
	}
	return out
}

// maxRollupHistIndex is the bucket of the largest int64.
var maxRollupHistIndex = rollupHistIndex(math.MaxInt64)

func decodeRollupHist(data []byte) (rollupHist, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var h rollupHist
	index := 0
	for len(data) > 0 {
		delta, n := binary.Uvarint(data)
		if n <= 0 || delta > uint64(maxRollupHistIndex) || (len(h) > 0 && delta == 0) {
			return nil, errRollupHistogram
		}
		data = data[n:]
		count, n := binary.Uvarint(data)
		if n <= 0 || count == 0 || count > math.MaxInt64 {
			return nil, errRollupHistogram
		}
		data = data[n:]
		index += int(delta)
		if index > maxRollupHistIndex {
			return nil, errRollupHistogram
		}
		h = append(h, rollupHistBucket{index, int64(count)})
	}
	return h, nil
}

// histQuantile is the q-quantile (0<=q<=1) of h in stored units, unrounded,
// 0 when empty. It interpolates linearly between the representative values of
// the order statistics around rank q*(n-1) (0-based); for q=0.5 that is the
// median rank rule of referenceReportMedian: the middle value, or the average
// of the two middle values.
//
// Error bound: a representative value is within 1/64 relative of every value
// of its bucket and exact below 32, and representatives are ordered like the
// values, so the result is within q_exact/64 of the same interpolation over
// the exact values (plus float rounding); daily medians add 0.005 for their
// rounding, and tokens per second 0.01 more for their stored floor.
func histQuantile(h rollupHist, q float64) float64 {
	n := h.total()
	if n <= 0 {
		return 0
	}
	q = min(max(q, 0), 1)
	rank := q * float64(n-1)
	if nearest := math.Round(rank); math.Abs(rank-nearest) < 1e-9 {
		rank = nearest
	}
	low := int64(rank)
	high := low
	if fraction := rank - float64(low); fraction > 0 {
		high = low + 1
	}
	var lowValue, highValue float64
	var seen int64
	for _, bucket := range h {
		before := seen
		seen += bucket.count
		if before <= low && low < seen {
			lowValue = rollupHistRepresentative(bucket.index)
		}
		if before <= high && high < seen {
			highValue = rollupHistRepresentative(bucket.index)
			break
		}
	}
	return lowValue + (highValue-lowValue)*(rank-float64(low))
}

// median is histQuantile(h, 0.5) divided by scale and rounded to two places,
// like the former SQL median; 0 when empty.
func (h rollupHist) median(scale float64) float64 {
	return roundReport(histQuantile(h, 0.5)/scale, 2)
}

// rollupLatency holds the latency histograms of one scope: the three behind
// daily medians and total request latency, which rollups keep for latency
// percentiles that no current response shows.
type rollupLatency struct{ ttft, queue, tps, total rollupHist }

// observe adds one event's samples with the guards of the former SQL medians:
// TTFT when positive, queue when not negative, and tokens per second when
// TTFT, output and the generation time after the first token are positive.
// Total latency counts when positive.
func (l *rollupLatency) observe(ttft sql.NullInt64, queue, output, total int64, sign int64) {
	if ttft.Valid && ttft.Int64 > 0 {
		l.ttft.add(ttft.Int64, sign)
		if output > 0 && total > ttft.Int64 {
			l.tps.add(rollupTPSSample(output, total-ttft.Int64), sign)
		}
	}
	if queue >= 0 {
		l.queue.add(queue, sign)
	}
	if total > 0 {
		l.total.add(total, sign)
	}
}

// rollupTPSSample is floor(output*1000/generation*100) in integers.
func rollupTPSSample(output, generation int64) int64 {
	const factor = 1000 * rollupTPSScale
	if output > math.MaxInt64/factor {
		return math.MaxInt64 / generation
	}
	return output * factor / generation
}

// medians returns the daily median TTFT, queue latency and tokens per second.
func (l rollupLatency) medians() (ttft, queue, tps float64) {
	return l.ttft.median(1), l.queue.median(1), l.tps.median(rollupTPSScale)
}

func (l rollupLatency) merge(other rollupLatency, sign int64) rollupLatency {
	return rollupLatency{l.ttft.merge(other.ttft, sign), l.queue.merge(other.queue, sign),
		l.tps.merge(other.tps, sign), l.total.merge(other.total, sign)}
}

func (l rollupLatency) negative() bool {
	return l.ttft.negative() || l.queue.negative() || l.tps.negative() || l.total.negative()
}

// encode returns the ttft_hist, queue_hist, tps_hist and total_hist values.
func (l rollupLatency) encode() (ttft, queue, tps, total []byte) {
	return l.ttft.encode(), l.queue.encode(), l.tps.encode(), l.total.encode()
}

// decodeRollupLatency reads the four histogram columns of a rollup row.
func decodeRollupLatency(ttft, queue, tps, total []byte) (rollupLatency, error) {
	var l rollupLatency
	var err error
	for _, column := range []struct {
		data []byte
		hist *rollupHist
	}{{ttft, &l.ttft}, {queue, &l.queue}, {tps, &l.tps}, {total, &l.total}} {
		if *column.hist, err = decodeRollupHist(column.data); err != nil {
			return l, err
		}
	}
	return l, nil
}
