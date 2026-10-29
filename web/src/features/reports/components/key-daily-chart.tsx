import { BarChart3 } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { EmptyState } from "@/components/empty-state";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "@/components/lazy-recharts";
import { Button } from "@/components/ui/button";
import { formatCompactNumber, formatCurrency } from "@/utils/formatters";

import {
  buildDailyChartRows,
  DAILY_CHART_MARGIN,
  OTHER_SERIES_COLOR,
  SERIES_COLORS,
  seriesDataKey,
  type DailyMetric,
} from "../daily-chart-rows";
import type { KeyReportDay, KeyReportSeries } from "../schemas";
import { ChartTooltip } from "./chart-tooltip";

const METRICS: { id: DailyMetric; labelKey: string }[] = [
  { id: "cost", labelKey: "reports.daily.metrics.cost" },
  { id: "requests", labelKey: "reports.daily.metrics.requests" },
  { id: "tokens", labelKey: "reports.daily.metrics.tokens" },
];

function formatMetric(metric: DailyMetric, value: number): string {
  return metric === "cost" ? formatCurrency(value) : formatCompactNumber(value);
}

export type KeyDailyChartProps = {
  start: string;
  end: string;
  series: KeyReportSeries[];
  daily: KeyReportDay[];
  empty: boolean;
};

/**
 * Stacked bars per local day of the top 8 keys, or groups, plus other, with
 * a cost, requests or tokens toggle.
 */
export function KeyDailyChart({ start, end, series, daily, empty }: KeyDailyChartProps) {
  const { t } = useTranslation();
  const [metric, setMetric] = useState<DailyMetric>("cost");
  const rows = useMemo(() => buildDailyChartRows(start, end, daily, series.length, metric), [daily, end, metric, series.length, start]);
  const legend = useMemo(() => {
    if (series.length === 0) {
      return [{ key: seriesDataKey(0), name: t("reports.daily.total"), color: SERIES_COLORS[0] }];
    }
    let colorIndex = 0;
    return series.map((entry, index) => {
      const name =
        entry.kind === "ungrouped" ? t("reports.ungroupedKeys") : entry.kind === "other" ? t("reports.daily.other") : entry.name;
      const color = entry.kind === "other" ? OTHER_SERIES_COLOR : SERIES_COLORS[colorIndex++ % SERIES_COLORS.length];
      return { key: seriesDataKey(index), name, color };
    });
  }, [series, t]);
  const names = useMemo(() => Object.fromEntries(legend.map((item) => [item.key, item.name])), [legend]);

  return (
    <section aria-labelledby="reports-daily-title" className="rounded-xl border bg-card p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="reports-daily-title" className="text-sm font-semibold text-foreground">
          {t("reports.daily.title")}
        </h2>
        <div role="group" aria-label={t("reports.daily.metric")} className="flex items-center gap-1">
          {METRICS.map((item) => (
            <Button
              key={item.id}
              type="button"
              size="xs"
              variant={metric === item.id ? "default" : "outline"}
              aria-pressed={metric === item.id}
              onClick={() => setMetric(item.id)}
            >
              {t(item.labelKey)}
            </Button>
          ))}
        </div>
      </div>
      {empty ? (
        <div className="mt-4">
          <EmptyState icon={BarChart3} title={t("reports.empty.noUsage")} />
        </div>
      ) : (
        <>
          <div className="mt-4 h-[240px]" data-testid="key-daily-chart">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={rows} margin={DAILY_CHART_MARGIN}>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" vertical={false} />
                <XAxis dataKey="label" tick={{ fontSize: 10, fill: "var(--muted-foreground)" }} axisLine={false} tickLine={false} />
                <YAxis
                  tick={{ fontSize: 10, fill: "var(--muted-foreground)" }}
                  axisLine={false}
                  tickLine={false}
                  width={56}
                  tickFormatter={(value: number) => formatMetric(metric, value)}
                />
                <Tooltip
                  cursor={{ fill: "var(--muted)", opacity: 0.4 }}
                  content={<ChartTooltip names={names} formatValue={(value: number) => formatMetric(metric, value)} />}
                />
                {legend.map((item) => (
                  <Bar key={item.key} dataKey={item.key} name={item.key} stackId="daily" fill={item.color} isAnimationActive={false} />
                ))}
              </BarChart>
            </ResponsiveContainer>
          </div>
          <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground" aria-label={t("reports.daily.legend")}>
            {legend.map((item) => (
              <li key={item.key} className="flex items-center gap-1.5">
                <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: item.color }} aria-hidden="true" />
                <span className="max-w-48 truncate">{item.name}</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}
