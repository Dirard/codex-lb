import { eachISODate } from "./date";
import type { KeyReportDay, KeyReportSeriesPoint } from "./schemas";

export type DailyMetric = "cost" | "requests" | "tokens";

/** Eight series colours plus a neutral one for "other". */
export const SERIES_COLORS = ["#3b82f6", "#10b981", "#f59e0b", "#8b5cf6", "#ec4899", "#06b6d4", "#84cc16", "#f97316"];
export const OTHER_SERIES_COLOR = "#94a3b8";

/** Equal left and right inset of the plotted area. */
export const DAILY_CHART_MARGIN = { top: 5, right: 12, left: 12, bottom: 0 } as const;

export type DailyChartRow = { date: string; label: string } & Record<string, number | string>;

function pointValue(point: KeyReportSeriesPoint | undefined, metric: DailyMetric): number {
  if (!point) {
    return 0;
  }
  return metric === "cost" ? point.costUsd : metric === "requests" ? point.requests : point.tokens;
}

export function seriesDataKey(index: number): string {
  return `s${index}`;
}

/**
 * One row per local day from start to end, oldest first, with the value of
 * every series for the metric; a day the response omits is a zero row. With
 * no series the day's total is plotted as one series.
 */
export function buildDailyChartRows(
  start: string,
  end: string,
  daily: KeyReportDay[],
  seriesCount: number,
  metric: DailyMetric,
): DailyChartRow[] {
  const byDate = new Map(daily.map((day) => [day.date, day]));
  const dates = eachISODate(start, end);
  return (dates.length > 0 ? dates : daily.map((day) => day.date)).map((date) => {
    const day = byDate.get(date);
    const row: DailyChartRow = { date, label: date.slice(5) };
    if (seriesCount === 0) {
      row[seriesDataKey(0)] = day
        ? metric === "cost"
          ? day.total.costUsd
          : metric === "requests"
            ? day.total.requests
            : day.total.inputTokens + day.total.outputTokens
        : 0;
      return row;
    }
    for (let index = 0; index < seriesCount; index += 1) {
      row[seriesDataKey(index)] = pointValue(day?.series[index], metric);
    }
    return row;
  });
}
