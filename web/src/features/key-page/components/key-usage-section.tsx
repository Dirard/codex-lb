import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipContentProps,
} from "@/components/lazy-recharts";
import { Button } from "@/components/ui/button";
import { countLabel, dayTick, msLabel, sharePercent, usd } from "@/features/key-page/key-page-format";
import { SectionCard, TableHeadRow, Th } from "@/features/key-page/components/section-card";
import type { KeyDay, KeyModelRow } from "@/features/key-page/schemas";
import { useChartColors } from "@/hooks/use-chart-colors";
import { formatNumber } from "@/utils/formatters";

const USAGE_METRICS = ["cost", "requests", "tokens"] as const;
type UsageMetric = (typeof USAGE_METRICS)[number];

const OTHER = "__other__";
const OTHER_COLOR = "#a1a1aa";

type Series = { key: string; label: string };

/**
 * One row per local day with each top model's value of the metric and the
 * rest summed into "other", in the order of the period's model table.
 */
function usageChartRows(daily: KeyDay[], metric: UsageMetric): { rows: Record<string, number | string>[]; series: string[] } {
  const series: string[] = [];
  let other = false;
  for (const day of daily) {
    for (const entry of day.byModel) {
      if (entry.other) other = true;
      else if (!series.includes(entry.model)) series.push(entry.model);
    }
  }
  if (other) series.push(OTHER);
  const rows = daily.map((day) => {
    const row: Record<string, number | string> = { date: dayTick(day.date) };
    for (const key of series) row[key] = 0;
    for (const entry of day.byModel) {
      const key = entry.other ? OTHER : entry.model;
      const value = metric === "cost" ? entry.costUsd : metric === "requests" ? entry.requests : entry.tokens;
      row[key] = (row[key] as number) + value;
    }
    return row;
  });
  return { rows, series };
}

function UsageTooltip({
  active,
  payload,
  label,
  names,
  format,
}: Partial<TooltipContentProps<number, string>> & { names: Record<string, string>; format: (value: number) => string }) {
  if (!active || !payload?.length) return null;
  return (
    <div className="rounded-lg border bg-popover p-2 text-xs shadow-md">
      <p className="mb-1 font-medium text-muted-foreground">{label}</p>
      {payload
        .filter((entry) => typeof entry.value === "number" && entry.value > 0)
        .map((entry) => (
          <p key={String(entry.dataKey)} className="flex items-center gap-2">
            <span className="h-2 w-2 rounded-full" style={{ backgroundColor: entry.color }} />
            <span>{names[String(entry.dataKey)] ?? String(entry.dataKey)}</span>
            <span className="ml-auto pl-3 font-semibold tabular-nums">{format(entry.value as number)}</span>
          </p>
        ))}
    </div>
  );
}

function UsageChart({ daily, metric }: { daily: KeyDay[]; metric: UsageMetric }) {
  const { t } = useTranslation();
  const colors = useChartColors();
  const { rows, series } = useMemo(() => usageChartRows(daily, metric), [daily, metric]);
  const names: Record<string, string> = Object.fromEntries(
    series.map((key) => [key, key === OTHER ? t("keyPage.usage.other") : key]),
  );
  const format = metric === "cost" ? usd : countLabel;
  const legend: Series[] = series.map((key) => ({ key, label: names[key] }));
  return (
    <div className="space-y-2">
      <div className="h-56" data-testid="key-usage-chart">
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={rows} margin={{ top: 5, right: 8, left: 4, bottom: 0 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
            <XAxis dataKey="date" tick={{ fontSize: 10, fill: "var(--muted-foreground)" }} axisLine={false} tickLine={false} minTickGap={8} />
            <YAxis tick={{ fontSize: 10, fill: "var(--muted-foreground)" }} axisLine={false} tickLine={false} tickFormatter={format} width={56} />
            <Tooltip cursor={{ fill: "var(--muted)", opacity: 0.4 }} content={<UsageTooltip names={names} format={format} />} />
            {series.map((key, index) => (
              <Bar
                key={key}
                dataKey={key}
                stackId="models"
                fill={key === OTHER ? OTHER_COLOR : colors[index % colors.length]}
                isAnimationActive={false}
              />
            ))}
          </BarChart>
        </ResponsiveContainer>
      </div>
      <ul className="flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground" aria-label={t("keyPage.usage.legend")}>
        {legend.map((entry, index) => (
          <li key={entry.key} className="flex items-center gap-1.5">
            <span
              className="h-2 w-2 rounded-full"
              style={{ backgroundColor: entry.key === OTHER ? OTHER_COLOR : colors[index % colors.length] }}
              aria-hidden="true"
            />
            {entry.label}
          </li>
        ))}
      </ul>
    </div>
  );
}

function ModelTable({ rows }: { rows: KeyModelRow[] }) {
  const { t } = useTranslation();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <TableHeadRow>
            <Th>{t("keyPage.columns.model")}</Th>
            <Th align="right">{t("keyPage.columns.requests")}</Th>
            <Th align="right">{t("keyPage.columns.tokens")}</Th>
            <Th align="right">{t("keyPage.columns.cost")}</Th>
            <Th align="right">{t("keyPage.columns.share")}</Th>
            <Th align="right">{t("keyPage.columns.ttftP50")}</Th>
          </TableHeadRow>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.model} className="border-b last:border-0" data-testid="key-model-row">
              <td className="max-w-64 truncate px-3 py-1.5 font-mono text-xs" title={row.model}>{row.model}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{formatNumber(row.requests)}</td>
              <td
                className="px-3 py-1.5 text-right tabular-nums"
                title={t("keyPage.usage.tokensTitle", {
                  input: countLabel(row.inputTokens),
                  output: countLabel(row.outputTokens),
                  cached: countLabel(row.cachedInputTokens),
                })}
              >
                {countLabel(row.inputTokens + row.outputTokens)}
              </td>
              <td className="px-3 py-1.5 text-right tabular-nums">{usd(row.costUsd)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{sharePercent(row.share)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(row.ttftP50Ms)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Cost (or requests or tokens) per local day by the top five models, and the period's model table. */
export function KeyUsageSection({ daily, byModel }: { daily: KeyDay[]; byModel: KeyModelRow[] }) {
  const { t } = useTranslation();
  const [metric, setMetric] = useState<UsageMetric>("cost");
  return (
    <SectionCard
      id="key-page-usage"
      title={t("keyPage.usage.title")}
      actions={
        <div role="group" aria-label={t("keyPage.usage.metric")} className="flex items-center gap-1">
          {USAGE_METRICS.map((value) => (
            <Button
              key={value}
              type="button"
              size="sm"
              variant={metric === value ? "secondary" : "ghost"}
              aria-pressed={metric === value}
              className="h-6 px-2 text-[11px]"
              onClick={() => setMetric(value)}
            >
              {t(`keyPage.usage.metrics.${value}`)}
            </Button>
          ))}
        </div>
      }
    >
      <div className="grid gap-4 p-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <UsageChart daily={daily} metric={metric} />
        {byModel.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("keyPage.empty.noModels")}</p>
        ) : (
          <ModelTable rows={byModel} />
        )}
      </div>
    </SectionCard>
  );
}
