package application

import (
	"cmp"
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// KeyUsageReportRepository is the storage the reports for API-key users read:
// the selection resolved to current keys and their per-day measures, in one
// analytics snapshot.
type KeyUsageReportRepository interface {
	KeyUsageReport(context.Context, domain.KeyUsageQuery) (domain.KeyUsageData, error)
}

// Validation failures of a report request; both wrap domain.ErrInvalid.
var (
	ErrReportPeriod  = fmt.Errorf("report period: %w", domain.ErrInvalid)
	ErrReportFilters = fmt.Errorf("report filters: %w", domain.ErrInvalid)
)

// Bounds of a report selection and of its daily chart.
const (
	MaxReportGroupIDs   = 200
	MaxReportKeyIDs     = 1000
	maxReportIDLength   = 256
	keyUsageSeriesLimit = 8
	ungroupedKeysName   = "Ungrouped keys"
	otherSeriesName     = "Other"
)

// KeyUsageReportParams are the validated transport parameters of a report;
// a nil Location is UTC.
type KeyUsageReportParams struct {
	StartDate, EndDate string
	Location           *time.Location
	GroupIDs, KeyIDs   []string
	Ungrouped          bool
}

// KeyUsageReportService is use case 5 of the dashboard redesign: reports for
// the users of API keys by group and key, and their per-day export.
type KeyUsageReportService struct {
	repo KeyUsageReportRepository
	now  func() time.Time
}

func NewKeyUsageReportService(repo KeyUsageReportRepository, now func() time.Time) *KeyUsageReportService {
	if now == nil {
		now = time.Now
	}
	return &KeyUsageReportService{repo: repo, now: now}
}

// keyUsagePlan is a validated report request: the storage query, the period
// shown and the label of each local day.
type keyUsagePlan struct {
	query  domain.KeyUsageQuery
	period domain.ReportPeriod
	dates  []string
	now    time.Time
}

// plan is the step "choose groups or keys and a period": local days of the
// zone from start to end (default the 7 days ending today), at most 731, the
// previous period of the same number of days, and a bounded selection.
func (s *KeyUsageReportService) plan(p KeyUsageReportParams, skipPrevious bool) (keyUsagePlan, error) {
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	now := s.now().UTC()
	local := now.In(loc)
	endDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if p.EndDate != "" {
		parsed, err := time.ParseInLocation(time.DateOnly, p.EndDate, loc)
		if err != nil {
			return keyUsagePlan{}, fmt.Errorf("end_date: %w", ErrReportPeriod)
		}
		endDay = parsed
	}
	startDay := endDay.AddDate(0, 0, -6)
	if p.StartDate != "" {
		parsed, err := time.ParseInLocation(time.DateOnly, p.StartDate, loc)
		if err != nil {
			return keyUsagePlan{}, fmt.Errorf("start_date: %w", ErrReportPeriod)
		}
		startDay = parsed
	}
	if startDay.After(endDay) {
		return keyUsagePlan{}, fmt.Errorf("start_date after end_date: %w", ErrReportPeriod)
	}
	plan := keyUsagePlan{now: now}
	for day := 0; ; day++ {
		start := time.Date(startDay.Year(), startDay.Month(), startDay.Day()+day, 0, 0, 0, 0, loc)
		if start.After(endDay) {
			plan.query.End = start.UTC()
			break
		}
		if day == domain.KeyUsageMaxDays {
			return keyUsagePlan{}, fmt.Errorf("more than %d days: %w", domain.KeyUsageMaxDays, ErrReportPeriod)
		}
		plan.query.DayStarts = append(plan.query.DayStarts, start.UTC())
		plan.dates = append(plan.dates, start.Format(time.DateOnly))
	}
	days := len(plan.dates)
	previousStart := time.Date(startDay.Year(), startDay.Month(), startDay.Day()-days, 0, 0, 0, 0, loc)
	plan.query.PreviousStart, plan.query.Now, plan.query.SkipPrevious = previousStart.UTC(), now, skipPrevious
	plan.period = domain.ReportPeriod{Start: plan.dates[0], End: plan.dates[days-1], Timezone: loc.String(),
		PreviousStart: previousStart.Format(time.DateOnly), PreviousEnd: startDay.AddDate(0, 0, -1).Format(time.DateOnly), Days: days}
	groups, err := reportIDs(p.GroupIDs, MaxReportGroupIDs)
	if err != nil {
		return keyUsagePlan{}, err
	}
	keys, err := reportIDs(p.KeyIDs, MaxReportKeyIDs)
	if err != nil {
		return keyUsagePlan{}, err
	}
	plan.query.Selection = domain.KeySelection{GroupIDs: groups, KeyIDs: keys, Ungrouped: p.Ungrouped}
	return plan, nil
}

// Query is the storage query of a report; store tests and benchmarks build
// their reads with it.
func (s *KeyUsageReportService) Query(p KeyUsageReportParams) (domain.KeyUsageQuery, error) {
	plan, err := s.plan(p, false)
	return plan.query, err
}

// reportIDs deduplicates ids in order and refuses blank, oversized or too
// many ids.
func reportIDs(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("more than %d ids: %w", limit, ErrReportFilters)
	}
	seen := make(map[string]bool, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maxReportIDLength {
			return nil, fmt.Errorf("report id: %w", ErrReportFilters)
		}
		if !seen[value] {
			seen[value] = true
			ids = append(ids, value)
		}
	}
	return ids, nil
}

// Report returns the report of a selection and period.
func (s *KeyUsageReportService) Report(ctx context.Context, p KeyUsageReportParams) (domain.KeyUsageReport, error) {
	plan, err := s.plan(p, false)
	if err != nil {
		return domain.KeyUsageReport{}, err
	}
	data, err := s.repo.KeyUsageReport(ctx, plan.query)
	if err != nil {
		return domain.KeyUsageReport{}, err
	}
	return buildKeyUsageReport(plan, data), nil
}

// reportKey is one resolved key with its period sums.
type reportKey struct {
	key       domain.OverviewKey
	groupName string
	days      []domain.UsageSums
	total     domain.UsageSums
}

// reportScopeKey identifies a group row; "" with ungrouped true is the row of
// the keys without a group.
type reportScopeKey struct {
	id        string
	ungrouped bool
}

func scopeOf(key domain.OverviewKey) reportScopeKey {
	if key.GroupID == nil {
		return reportScopeKey{ungrouped: true}
	}
	return reportScopeKey{id: *key.GroupID}
}

// reportKeys attaches group names and period sums to the resolved keys.
func reportKeys(data domain.KeyUsageData) ([]reportKey, map[string]string) {
	names := make(map[string]string, len(data.Groups))
	for _, group := range data.Groups {
		names[group.ID] = group.Name
	}
	keys := make([]reportKey, len(data.Keys))
	for i, key := range data.Keys {
		keys[i] = reportKey{key: key, days: data.Days[key.ID], groupName: ungroupedKeysName}
		if key.GroupID != nil {
			keys[i].groupName = cmp.Or(names[*key.GroupID], *key.GroupID)
		}
		for _, day := range keys[i].days {
			keys[i].total.Add(day)
		}
	}
	return keys, names
}

// buildKeyUsageReport is the assembly step of use case 5: totals, group rows,
// key rows, the top-8 daily series and the previous period.
func buildKeyUsageReport(plan keyUsagePlan, data domain.KeyUsageData) domain.KeyUsageReport {
	selection := plan.query.Selection
	keys, names := reportKeys(data)
	report := domain.KeyUsageReport{GeneratedAt: plan.now, Period: plan.period,
		Selection: domain.ReportSelection{GroupIDs: selection.GroupIDs, KeyIDs: selection.KeyIDs,
			Ungrouped: selection.Ungrouped, AllKeys: selection.Empty(), ResolvedKeyCount: len(keys)},
		Series: []domain.ReportSeries{}, Groups: []domain.GroupReportRow{}, Keys: []domain.KeyReportRow{},
		Daily: make([]domain.ReportDay, len(plan.dates))}
	var summary domain.UsageSums
	for _, key := range keys {
		summary.Add(key.total)
		if key.total.Requests > 0 {
			report.ActiveKeys++
		}
	}
	report.Summary = summary.Measures()
	if !plan.query.SkipPrevious && data.Previous.Requests > 0 {
		previous := data.Previous.Measures()
		report.Previous = &previous
	}
	share := func(cost float64) float64 {
		if summary.CostUSD <= 0 {
			return 0
		}
		return roundTo(cost/summary.CostUSD, 6)
	}

	// Group rows: every group of a resolved key and every selected group.
	type groupSum struct {
		scope            reportScopeKey
		name             string
		keyCount, active int
		total            domain.UsageSums
	}
	groupIndex := map[reportScopeKey]*groupSum{}
	var groups []*groupSum
	group := func(scope reportScopeKey, name string) *groupSum {
		sum := groupIndex[scope]
		if sum == nil {
			sum = &groupSum{scope: scope, name: name}
			groupIndex[scope] = sum
			groups = append(groups, sum)
		}
		return sum
	}
	for _, key := range keys {
		sum := group(scopeOf(key.key), key.groupName)
		sum.keyCount++
		sum.total.Add(key.total)
		if key.total.Requests > 0 {
			sum.active++
		}
	}
	resolvedGroups := len(groups)
	for _, id := range selection.GroupIDs {
		if name, ok := names[id]; ok {
			group(reportScopeKey{id: id}, name)
		}
	}
	slices.SortStableFunc(groups, func(a, b *groupSum) int {
		return rankSums(a.total, b.total, a.name, b.name, a.scope.id, b.scope.id)
	})
	for _, sum := range groups {
		row := domain.GroupReportRow{Name: sum.name, KeyCount: sum.keyCount, ActiveKeys: sum.active,
			Measures: sum.total.Measures(), Share: share(sum.total.CostUSD)}
		if !sum.scope.ungrouped {
			row.ID = stringPointer(sum.scope.id)
		}
		report.Groups = append(report.Groups, row)
	}

	ranked := slices.Clone(keys)
	slices.SortStableFunc(ranked, func(a, b reportKey) int {
		return rankSums(a.total, b.total, a.key.Name, b.key.Name, a.key.ID, b.key.ID)
	})
	for _, key := range ranked {
		row := domain.KeyReportRow{ID: key.key.ID, Name: key.key.Name, Prefix: key.key.Prefix, GroupID: key.key.GroupID,
			Status: key.key.Status(plan.now), Measures: key.total.Measures(), Share: share(key.total.CostUSD),
			LastUsedAt: key.key.LastUsedAt}
		if key.key.GroupID != nil {
			row.GroupName = stringPointer(key.groupName)
		}
		if limit := domain.FullestLimit(key.key.Limits); limit != nil {
			percent := roundTo(limit.Percent, 2)
			row.MaxLimitPercent = &percent
		}
		report.Keys = append(report.Keys, row)
	}

	// Daily series: the top keys, or the top groups when several groups are
	// selected, plus other.
	byGroup := resolvedGroups > 1 && (selection.Empty() || len(selection.GroupIDs)+boolCount(selection.Ungrouped) > 1)
	report.SeriesBy = domain.SeriesKey
	seriesOf := map[string]int{} // key id -> series index
	if byGroup {
		report.SeriesBy = domain.SeriesGroup
		var top []*groupSum
		for _, sum := range groups {
			if sum.total.Requests > 0 {
				top = append(top, sum)
			}
		}
		for i, sum := range top {
			index := min(i, keyUsageSeriesLimit)
			if i < keyUsageSeriesLimit {
				series := domain.ReportSeries{Name: sum.name, Kind: domain.SeriesUngrouped}
				if !sum.scope.ungrouped {
					series.ID, series.Kind = stringPointer(sum.scope.id), domain.SeriesGroup
				}
				report.Series = append(report.Series, series)
			}
			for _, key := range keys {
				if scopeOf(key.key) == sum.scope {
					seriesOf[key.key.ID] = index
				}
			}
		}
	} else {
		n := 0
		for _, key := range ranked {
			if key.total.Requests <= 0 {
				continue
			}
			if n < keyUsageSeriesLimit {
				report.Series = append(report.Series, domain.ReportSeries{ID: stringPointer(key.key.ID), Name: key.key.Name, Kind: domain.SeriesKey})
			}
			seriesOf[key.key.ID] = min(n, keyUsageSeriesLimit)
			n++
		}
	}
	for _, index := range seriesOf {
		if index == keyUsageSeriesLimit {
			report.Series = append(report.Series, domain.ReportSeries{Name: otherSeriesName, Kind: domain.SeriesOther})
			break
		}
	}
	totals := make([]domain.UsageSums, len(plan.dates))
	points := make([][]domain.UsageSums, len(plan.dates))
	for d := range points {
		points[d] = make([]domain.UsageSums, len(report.Series))
	}
	for _, key := range keys {
		index, inSeries := seriesOf[key.key.ID]
		for d, day := range key.days {
			totals[d].Add(day)
			if inSeries {
				points[d][index].Add(day)
			}
		}
	}
	for d, date := range plan.dates {
		day := domain.ReportDay{Date: date, Total: totals[d].Measures(), Series: make([]domain.ReportSeriesPoint, len(report.Series))}
		for i, point := range points[d] {
			day.Series[i] = domain.ReportSeriesPoint{Requests: point.Requests, Tokens: point.Tokens(), CostUSD: domain.RoundCostUSD(point.CostUSD)}
		}
		report.Daily[d] = day
	}
	return report
}

// rankSums orders by cost, then requests, descending, then name and id.
func rankSums(a, b domain.UsageSums, nameA, nameB, idA, idB string) int {
	return cmp.Or(cmp.Compare(b.CostUSD, a.CostUSD), cmp.Compare(b.Requests, a.Requests),
		cmp.Compare(nameA, nameB), cmp.Compare(idA, idB))
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func stringPointer(value string) *string { return &value }

// KeyDailyExport is the per-day export of one report, read in a snapshot that
// is closed before the first row is produced.
type KeyDailyExport struct {
	Period domain.ReportPeriod
	dates  []string
	keys   []reportKey
}

// Export reads the per-day rows of a selection and period (R31).
func (s *KeyUsageReportService) Export(ctx context.Context, p KeyUsageReportParams) (KeyDailyExport, error) {
	plan, err := s.plan(p, true)
	if err != nil {
		return KeyDailyExport{}, err
	}
	data, err := s.repo.KeyUsageReport(ctx, plan.query)
	if err != nil {
		return KeyDailyExport{}, err
	}
	keys, _ := reportKeys(data)
	keys = slices.DeleteFunc(keys, func(key reportKey) bool { return key.days == nil })
	slices.SortStableFunc(keys, func(a, b reportKey) int {
		return cmp.Or(cmp.Compare(boolCount(a.key.GroupID == nil), boolCount(b.key.GroupID == nil)),
			cmp.Compare(a.groupName, b.groupName), cmp.Compare(a.key.Name, b.key.Name), cmp.Compare(a.key.ID, b.key.ID))
	})
	return KeyDailyExport{Period: plan.period, dates: plan.dates, keys: keys}, nil
}

// Rows yields one row per local day and key with requests, ordered by date,
// group name with ungrouped keys last, key name and key id.
func (e KeyDailyExport) Rows() iter.Seq[domain.KeyDailyRow] {
	return func(yield func(domain.KeyDailyRow) bool) {
		for d, date := range e.dates {
			for _, key := range e.keys {
				day := key.days[d]
				if day.Requests == 0 {
					continue
				}
				row := domain.KeyDailyRow{Date: date, GroupID: key.key.GroupID, KeyID: key.key.ID, KeyName: key.key.Name, Usage: day}
				if key.key.GroupID != nil {
					row.GroupName = key.groupName
				}
				if !yield(row) {
					return
				}
			}
		}
	}
}
