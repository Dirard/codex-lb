package sqlite

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func TestPlanRollupWindowSplitsPartialAndSplitHours(t *testing.T) {
	const h = rollupHourMS
	base := millis(time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	for _, test := range []struct {
		name       string
		start, end int64
		splits     []int64
		want       rollupPlan
	}{
		{"whole hours", base, base + 5*h, nil, rollupPlan{{base, base + 5*h, false}}},
		{"partial edges", base + 90, base + 3*h + 7, nil,
			rollupPlan{{base + 90, base + h, true}, {base + h, base + 3*h, false}, {base + 3*h, base + 3*h + 7, true}}},
		{"inside one hour", base + 10, base + h - 10, nil, rollupPlan{{base + 10, base + h - 10, true}}},
		{"whole-hour splits read rollups", base, base + 4*h, []int64{base + h, base + 2*h}, rollupPlan{{base, base + 4*h, false}}},
		{"fractional splits read their hours raw", base, base + 6*h, []int64{base + h + h/2, base + 4*h + h/4, base + 4*h + h/2, base + 9*h},
			rollupPlan{{base, base + h, false}, {base + h, base + 2*h, true}, {base + 2*h, base + 4*h, false},
				{base + 4*h, base + 5*h, true}, {base + 5*h, base + 6*h, false}}},
		{"adjacent split hours merge", base, base + 4*h, []int64{base + h + 1, base + 2*h + 1},
			rollupPlan{{base, base + h, false}, {base + h, base + 3*h, true}, {base + 3*h, base + 4*h, false}}},
		{"edge split hour merges with the edge", base + 5, base + 3*h, []int64{base + h + 5},
			rollupPlan{{base + 5, base + 2*h, true}, {base + 2*h, base + 3*h, false}}},
		{"hours before the first rollup hour are raw", -5 * h, 3 * h, nil, rollupPlan{{-5 * h, h, true}, {h, 3 * h, false}}},
		{"empty", base, base, nil, nil},
	} {
		if got := planRollupWindow(test.start, test.end, test.splits); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: plan %+v, want %+v", test.name, got, test.want)
		}
	}
	plan := planRollupWindow(base+90, base+3*h+7, []int64{base + 2*h + 30})
	if !plan.rolledUp(base+h) || plan.rolledUp(base+2*h+5) || plan.rolledUp(base+3*h) || plan.rollupHour((base+2*h)/1000) ||
		!plan.rollupHour((base+h)/1000) || plan.rolledUp(base+5*h) {
		t.Fatalf("plan membership %+v", plan)
	}
	if from, to, ok := plan.hours(); !ok || from != (base+h)/1000 || to != (base+2*h)/1000 {
		t.Fatalf("plan hours %d..%d %v", from, to, ok)
	}
	day := int64(86400000)
	hours, days, ok := planRollupWindow(base-2*h, base+2*day+3*h, nil).sliceRanges()
	if !ok || !reflect.DeepEqual(days, [][2]int64{{base / 1000, (base + 2*day) / 1000}}) ||
		!reflect.DeepEqual(hours, [][2]int64{{(base - 2*h) / 1000, base / 1000}, {(base + 2*day) / 1000, (base + 2*day + 3*h) / 1000}}) {
		t.Fatalf("slice ranges %v %v %v", hours, days, ok)
	}
}

// Rollup rows of the planned hours plus the snapshot's images equal the raw
// events of the window for every fold state: partially folded, with pending
// journal rows of updates and deletions, and drained.
func TestRollupReadSourcesCombineToTheRawWindow(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(41, 42))
	setRollupWatermark(t, s, rollupFixtureBase.Add(20*time.Hour))
	insertRollupFixture(t, s, random, 0, 900, 3*24*time.Hour)
	if _, err := s.FoldUsageRollups(ctx, 500, 500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM usage_events WHERE rowid%9=0 AND rowid<=400`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_events SET cost_microdollars=cost_microdollars+11 WHERE rowid%5=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_events SET account_id=NULL,legacy_deleted=1 WHERE rowid%7=3`); err != nil {
		t.Fatal(err)
	}
	start := millis(rollupFixtureBase.Add(5*time.Hour + 17*time.Minute))
	end := millis(rollupFixtureBase.Add(50*time.Hour + 3*time.Minute))
	splits := []int64{millis(rollupFixtureBase.Add(18*time.Hour + 30*time.Minute)), millis(rollupFixtureBase.Add(42*time.Hour + 45*time.Minute))}
	for step := range 2 {
		tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := openRollupRead(ctx, tx)
		if err != nil || reader == nil {
			t.Fatalf("step %d: rollups unusable: %v", step, err)
		}
		plan := planRollupWindow(start, end, splits)
		var got rollupMeasures
		from, to, _ := plan.hours()
		rows, err := tx.Query(`SELECT bucket_epoch,`+rollupMeasureColumns+` FROM usage_rollup_total_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, from, to)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var hour int64
			var m rollupMeasures
			if err := rows.Scan(append([]any{&hour}, m.fields()...)...); err != nil {
				t.Fatal(err)
			}
			if plan.rollupHour(hour) {
				got = got.plus(m)
			}
		}
		rows.Close()
		where, args := reportEventWhere(reportFilterDims{})
		if err := reader.images(ctx, plan, where, args, func(img *rollupImage, sign int64) { got.add(img, sign) }); err != nil {
			t.Fatal(err)
		}
		var want rollupMeasures
		if err := tx.QueryRow(`SELECT `+oracleMeasures+` FROM usage_events e WHERE e.requested_at>=? AND e.requested_at<? AND `+
			rollupInReportsSQL("e"), start, end).Scan(want.fields()...); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()
		if got != want || want.requests < 100 {
			t.Fatalf("step %d: rollup sources %+v, raw %+v", step, got, want)
		}
		foldRollups(t, s, 64)
	}
}

// A rebuilding state, a moved anchor or a changed import watermark make reads
// use raw events.
func TestRollupReadsFallBackToRawEvents(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	insertRollupFixture(t, s, rand.New(rand.NewPCG(43, 44)), 0, 200, 24*time.Hour)
	usable := func() bool {
		t.Helper()
		tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		reader, err := openRollupRead(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return reader != nil
	}
	if !usable() {
		t.Fatal("a small unfolded tail is not usable")
	}
	foldRollups(t, s, 1000)
	for _, corrupt := range []string{
		`UPDATE usage_rollup_state SET anchor_request_id='other'`,
		`UPDATE usage_rollup_state SET import_watermark=12345`,
		`UPDATE usage_rollup_state SET status='rebuilding'`,
		`UPDATE usage_rollup_state SET layout=2`,
		`UPDATE usage_rollup_state SET folded_rowid=folded_rowid+5`,
	} {
		var state rollupState
		if err := s.db.QueryRow(`SELECT layout,status,folded_rowid,anchor_request_id,import_watermark FROM usage_rollup_state`).
			Scan(&state.layout, &state.status, &state.foldedRowid, &state.anchor, &state.watermark); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(corrupt); err != nil {
			t.Fatal(err)
		}
		if usable() {
			t.Fatalf("%s: rollups still usable", corrupt)
		}
		if _, err := s.db.Exec(`UPDATE usage_rollup_state SET layout=?,status=?,folded_rowid=?,anchor_request_id=?,import_watermark=?`,
			state.layout, state.status, state.foldedRowid, state.anchor, state.watermark); err != nil {
			t.Fatal(err)
		}
		if !usable() {
			t.Fatalf("%s: restored state unusable", corrupt)
		}
	}
}
