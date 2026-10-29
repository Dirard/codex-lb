package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type fakeKeyUsageRepo struct {
	queries []domain.KeyUsageQuery
	data    func(domain.KeyUsageQuery) domain.KeyUsageData
}

func (f *fakeKeyUsageRepo) KeyUsageReport(_ context.Context, q domain.KeyUsageQuery) (domain.KeyUsageData, error) {
	f.queries = append(f.queries, q)
	return f.data(q), nil
}

var keyUsageNow = time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)

func usageDay(requests int64, cost float64) domain.UsageSums {
	return domain.UsageSums{Requests: requests, InputTokens: 10 * requests, OutputTokens: requests, CostUSD: cost}
}

// The period is the local days of the zone ending today by default, with the
// previous period of the same length; at most 731 days, start not after end.
func TestKeyUsagePeriodValidation(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	repo := &fakeKeyUsageRepo{data: func(domain.KeyUsageQuery) domain.KeyUsageData { return domain.KeyUsageData{} }}
	service := NewKeyUsageReportService(repo, func() time.Time { return keyUsageNow })
	report, err := service.Report(context.Background(), KeyUsageReportParams{Location: prague})
	if err != nil {
		t.Fatal(err)
	}
	q := repo.queries[0]
	if report.Period != (domain.ReportPeriod{Start: "2026-10-23", End: "2026-10-29", Timezone: "Europe/Prague",
		PreviousStart: "2026-10-16", PreviousEnd: "2026-10-22", Days: 7}) || len(report.Daily) != 7 {
		t.Fatalf("default period %+v", report.Period)
	}
	// The daylight-saving change of 2026-10-25 makes one local day 25 hours.
	if !q.DayStarts[0].Equal(time.Date(2026, 10, 22, 22, 0, 0, 0, time.UTC)) || !q.End.Equal(time.Date(2026, 10, 29, 23, 0, 0, 0, time.UTC)) ||
		!q.PreviousStart.Equal(time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)) || q.DayStarts[3].Sub(q.DayStarts[2]) != 25*time.Hour {
		t.Fatalf("day bounds %v .. %v, previous %v", q.DayStarts, q.End, q.PreviousStart)
	}
	if _, err := service.Report(context.Background(), KeyUsageReportParams{StartDate: "2024-10-31", EndDate: "2026-10-31"}); err != nil {
		t.Fatalf("731 days: %v", err)
	}
	for _, p := range []KeyUsageReportParams{
		{StartDate: "2024-10-30", EndDate: "2026-10-31"},
		{StartDate: "2026-10-02", EndDate: "2026-10-01"},
		{StartDate: "2026-10-1"},
		{EndDate: "yesterday"},
	} {
		if _, err := service.Report(context.Background(), p); !errors.Is(err, ErrReportPeriod) || !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%+v: %v", p, err)
		}
	}
	tooMany := make([]string, MaxReportKeyIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint("key-", i)
	}
	for _, p := range []KeyUsageReportParams{
		{KeyIDs: []string{" "}},
		{GroupIDs: []string{strings.Repeat("g", 257)}},
		{KeyIDs: tooMany},
	} {
		if _, err := service.Report(context.Background(), p); !errors.Is(err, ErrReportFilters) {
			t.Errorf("%+v: %v", p.GroupIDs, err)
		}
	}
	if _, err := service.Report(context.Background(), KeyUsageReportParams{KeyIDs: []string{"a", "b", "a"}}); err != nil ||
		!slices.Equal(repo.queries[len(repo.queries)-1].Selection.KeyIDs, []string{"a", "b"}) {
		t.Fatalf("duplicate ids: %v %v", err, repo.queries[len(repo.queries)-1].Selection)
	}
}

func reportKeyFixture(id, name string, group *string) domain.OverviewKey {
	return domain.OverviewKey{ID: id, Name: name, Prefix: "sk-" + id, GroupID: group, IsActive: true}
}

// Totals, group rows, key rows and shares follow the resolved keys; a key
// counts in its current group; several groups make the daily series groups;
// a selected group without keys still has a row; the previous period is null
// without requests.
func TestKeyUsageReportAssembly(t *testing.T) {
	groupA, groupB, groupC := "group-a", "group-b", "group-c"
	keys := []domain.OverviewKey{
		reportKeyFixture("k1", "one", &groupA),
		reportKeyFixture("k2", "two", &groupB),
		reportKeyFixture("k3", "three", nil),
		reportKeyFixture("k4", "idle", &groupA),
	}
	keys[0].Limits = []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 200, CurrentValue: 185}}
	data := domain.KeyUsageData{
		Groups: []domain.OverviewGroupRef{{ID: groupA, Name: "Alpha"}, {ID: groupB, Name: "Beta"}, {ID: groupC, Name: "Gamma"}},
		Keys:   keys,
		Days: map[string][]domain.UsageSums{
			"k1": {usageDay(2, 1.5), usageDay(1, 0.25)},
			"k2": {usageDay(0, 0), usageDay(4, 2)},
			"k3": {usageDay(1, 0.25), {}},
		},
	}
	repo := &fakeKeyUsageRepo{data: func(domain.KeyUsageQuery) domain.KeyUsageData { return data }}
	service := NewKeyUsageReportService(repo, func() time.Time { return keyUsageNow })
	report, err := service.Report(context.Background(), KeyUsageReportParams{StartDate: "2026-10-28", EndDate: "2026-10-29",
		GroupIDs: []string{groupA, groupB, groupC}, Ungrouped: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Requests != 8 || report.Summary.CostUSD != 4 || report.ActiveKeys != 3 || report.Previous != nil ||
		report.Selection.ResolvedKeyCount != 4 || report.Selection.AllKeys {
		t.Fatalf("summary %+v active %d previous %v selection %+v", report.Summary, report.ActiveKeys, report.Previous, report.Selection)
	}
	var names []string
	for _, row := range report.Groups {
		names = append(names, fmt.Sprintf("%s:%d/%d:%g:%g", row.Name, row.ActiveKeys, row.KeyCount, row.Measures.CostUSD, row.Share))
	}
	if want := []string{"Beta:1/1:2:0.5", "Alpha:1/2:1.75:0.4375", "Ungrouped keys:1/1:0.25:0.0625", "Gamma:0/0:0:0"}; !slices.Equal(names, want) {
		t.Fatalf("groups %v, want %v", names, want)
	}
	if report.Groups[2].ID != nil || *report.Groups[3].ID != groupC {
		t.Fatalf("group ids %+v", report.Groups)
	}
	if report.Keys[0].ID != "k2" || report.Keys[3].ID != "k4" || *report.Keys[1].MaxLimitPercent != 92.5 ||
		report.Keys[1].GroupName == nil || *report.Keys[1].GroupName != "Alpha" || report.Keys[2].GroupName != nil {
		t.Fatalf("keys %+v", report.Keys)
	}
	if report.SeriesBy != domain.SeriesGroup || len(report.Series) != 3 || report.Series[2].Kind != domain.SeriesUngrouped {
		t.Fatalf("series %s %+v", report.SeriesBy, report.Series)
	}
	if report.Daily[1].Total.Requests != 5 || report.Daily[1].Series[0] != (domain.ReportSeriesPoint{Requests: 4, Tokens: 44, CostUSD: 2}) {
		t.Fatalf("daily %+v", report.Daily)
	}

	// One group: the series are its keys.
	report, err = service.Report(context.Background(), KeyUsageReportParams{GroupIDs: []string{groupA}})
	if err != nil {
		t.Fatal(err)
	}
	if report.SeriesBy != domain.SeriesKey {
		t.Fatalf("one group series by %s", report.SeriesBy)
	}
}

// More than eight series with requests keep the top eight by cost and add
// other; the series points of each day add up to its total.
func TestKeyUsageReportTopEightPlusOther(t *testing.T) {
	data := domain.KeyUsageData{Days: map[string][]domain.UsageSums{}}
	for i := range 11 {
		id := fmt.Sprintf("k%02d", i)
		data.Keys = append(data.Keys, reportKeyFixture(id, id, nil))
		data.Days[id] = []domain.UsageSums{usageDay(int64(i+1), float64(i+1))}
	}
	data.Previous = usageDay(3, 1)
	repo := &fakeKeyUsageRepo{data: func(domain.KeyUsageQuery) domain.KeyUsageData { return data }}
	service := NewKeyUsageReportService(repo, func() time.Time { return keyUsageNow })
	report, err := service.Report(context.Background(), KeyUsageReportParams{StartDate: "2026-10-29", EndDate: "2026-10-29"})
	if err != nil {
		t.Fatal(err)
	}
	if report.SeriesBy != domain.SeriesKey || len(report.Series) != 9 || *report.Series[0].ID != "k10" ||
		report.Series[8].Kind != domain.SeriesOther || report.Previous == nil || report.Previous.Requests != 3 {
		t.Fatalf("series %+v previous %v", report.Series, report.Previous)
	}
	var requests int64
	for _, point := range report.Daily[0].Series {
		requests += point.Requests
	}
	if requests != report.Daily[0].Total.Requests || report.Daily[0].Series[8].Requests != 1+2+3 {
		t.Fatalf("points %+v total %+v", report.Daily[0].Series, report.Daily[0].Total)
	}
}

// The export yields one row per day and key with requests, ordered by date,
// group name with ungrouped keys last, key name and id; it skips the
// previous period.
func TestKeyDailyExportRows(t *testing.T) {
	groupA, groupB := "group-a", "group-b"
	data := domain.KeyUsageData{
		Groups: []domain.OverviewGroupRef{{ID: groupA, Name: "Zulu"}, {ID: groupB, Name: "Alpha"}},
		Keys: []domain.OverviewKey{reportKeyFixture("k1", "b", &groupA), reportKeyFixture("k2", "a", nil),
			reportKeyFixture("k3", "c", &groupB), reportKeyFixture("k4", "a", &groupA)},
		Days: map[string][]domain.UsageSums{
			"k1": {usageDay(1, 1), usageDay(2, 1)},
			"k2": {usageDay(3, 1), {}},
			"k3": {{}, usageDay(1, 1)},
			"k4": {usageDay(1, 1), usageDay(1, 1)},
		},
	}
	repo := &fakeKeyUsageRepo{data: func(domain.KeyUsageQuery) domain.KeyUsageData { return data }}
	service := NewKeyUsageReportService(repo, func() time.Time { return keyUsageNow })
	export, err := service.Export(context.Background(), KeyUsageReportParams{StartDate: "2026-10-28", EndDate: "2026-10-29"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for row := range export.Rows() {
		rows = append(rows, row.Date+" "+row.GroupName+" "+row.KeyName)
	}
	want := []string{"2026-10-28 Zulu a", "2026-10-28 Zulu b", "2026-10-28  a", "2026-10-29 Alpha c", "2026-10-29 Zulu a", "2026-10-29 Zulu b"}
	if !slices.Equal(rows, want) || !repo.queries[0].SkipPrevious || export.Period.Days != 2 {
		t.Fatalf("rows %q, want %q", rows, want)
	}
}
