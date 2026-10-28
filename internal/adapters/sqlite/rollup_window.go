package sqlite

import (
	"math"
	"sort"
)

// rollupHourMS is one rollup bucket in milliseconds.
const rollupHourMS = 3600000

// rollupFirstHourMS is the first hour a read takes from rollups. Integer
// division truncates toward zero, so bucket 0 also holds events of the hour
// before the epoch; earlier instants are always read raw.
const rollupFirstHourMS = rollupHourMS

// rollupSpan is a part of a read window in UTC milliseconds: whole hours read
// from rollup rows (plus the unfolded tail and journal), or a raw range read
// from every event regardless of rowid.
type rollupSpan struct {
	from, to int64
	raw      bool
}

// rollupPlan is a window split into ordered, adjacent spans.
type rollupPlan []rollupSpan

// rollupEverything covers every instant with rollup rows: global facet values
// and conversation details are not hour-bucketed, so the tail and journal
// apply without time bounds and no raw span exists.
var rollupEverything = rollupPlan{{from: math.MinInt64, to: math.MaxInt64}}

func floorRollupHour(ms int64) int64 {
	hour := ms / rollupHourMS
	if ms%rollupHourMS < 0 {
		hour--
	}
	return hour * rollupHourMS
}

// planRollupWindow is the workflow "whole hours of [start,end) --> rollups;
// partial-hour edges and hours holding a split instant that is not on an hour
// boundary --> raw ranges" (design §1.6). Splits are the instants where a
// caller attributes events to different buckets, such as local midnights of a
// fractional-offset zone; whole-hour splits need no raw reads.
func planRollupWindow(start, end int64, splits []int64) rollupPlan {
	if start >= end {
		return nil
	}
	var plan rollupPlan
	add := func(from, to int64, raw bool) {
		if from >= to {
			return
		}
		if n := len(plan); n > 0 && plan[n-1].raw == raw && plan[n-1].to == from {
			plan[n-1].to = to
			return
		}
		plan = append(plan, rollupSpan{from, to, raw})
	}
	first := max(rollupFirstHourMS, floorRollupHour(start))
	if first < start {
		first += rollupHourMS
	}
	last := floorRollupHour(end)
	if first >= last {
		add(start, end, true)
		return plan
	}
	add(start, first, true)
	var hours []int64
	for _, split := range splits {
		if split > first && split < last && split%rollupHourMS != 0 {
			hours = append(hours, floorRollupHour(split))
		}
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i] < hours[j] })
	cursor := first
	for _, hour := range hours {
		if hour < cursor {
			continue
		}
		add(cursor, hour, false)
		add(hour, hour+rollupHourMS, true)
		cursor = hour + rollupHourMS
	}
	add(cursor, last, false)
	add(last, end, true)
	return plan
}

// span returns the span holding instant at.
func (p rollupPlan) span(at int64) (rollupSpan, bool) {
	i := sort.Search(len(p), func(i int) bool { return p[i].to > at })
	if i == len(p) || at < p[i].from {
		return rollupSpan{}, false
	}
	return p[i], true
}

// rolledUp reports whether instant at is served by rollup rows.
func (p rollupPlan) rolledUp(at int64) bool {
	span, ok := p.span(at)
	return ok && !span.raw
}

// rollupHour reports whether the rollup bucket starting at hour seconds is
// read from rollup rows.
func (p rollupPlan) rollupHour(hour int64) bool {
	if hour > math.MaxInt64/1000 || hour < math.MinInt64/1000 {
		return false
	}
	return p.rolledUp(hour * 1000)
}

// hours returns the bounds in seconds of the rollup buckets the plan reads:
// one range for a single query, whose raw hours rollupHour excludes.
func (p rollupPlan) hours() (from, to int64, ok bool) {
	for _, span := range p {
		if span.raw {
			continue
		}
		if !ok {
			from, ok = span.from/1000, true
		}
		to = span.to / 1000
	}
	return from, to, ok
}

// raws returns the raw spans.
func (p rollupPlan) raws() []rollupSpan {
	var spans []rollupSpan
	for _, span := range p {
		if span.raw {
			spans = append(spans, span)
		}
	}
	return spans
}

// rollupSliceMaxRanges bounds the bucket ranges of one slice query; a plan
// with more rollup spans reads hour rows of its whole range instead.
const rollupSliceMaxRanges = 32

// sliceRanges returns second ranges of hour-grain and day-grain slice rows
// covering the plan's rollup hours: whole UTC days inside one rollup span use
// day rows. ok is false when the plan has too many spans for one query.
func (p rollupPlan) sliceRanges() (hours, days [][2]int64, ok bool) {
	for _, span := range p {
		if span.raw {
			continue
		}
		from, to := span.from/1000, span.to/1000
		dayFrom, dayTo := (from+86399)/86400*86400, to/86400*86400
		if dayFrom >= dayTo {
			hours = append(hours, [2]int64{from, to})
			continue
		}
		if from < dayFrom {
			hours = append(hours, [2]int64{from, dayFrom})
		}
		days = append(days, [2]int64{dayFrom, dayTo})
		if dayTo < to {
			hours = append(hours, [2]int64{dayTo, to})
		}
	}
	return hours, days, len(hours)+len(days) <= rollupSliceMaxRanges
}
