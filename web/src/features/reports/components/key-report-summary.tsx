import { useTranslation } from "react-i18next";

import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

import {
  costLabel,
  countLabel,
  errorRate,
  percentChange,
  percentChangeLabel,
  rateLabel,
  tokenLabel,
  totalTokens,
} from "../report-format";
import type { KeyUsageReport, UsageMeasures } from "../schemas";

type Metric = {
  id: string;
  label: string;
  value: string;
  sub?: string;
  change: number | null;
  previous?: string;
  /** Errors rising is bad; spend rising is neutral. */
  risingIsBad?: boolean;
};

function previousValue(previous: UsageMeasures | null, pick: (measures: UsageMeasures) => number): number | null {
  return previous ? pick(previous) : null;
}

/**
 * Totals of the report with the change against the previous period of the
 * same length. Every indicator is hidden without a previous period, and a
 * metric's indicator is hidden when its previous value is zero.
 */
export function KeyReportSummary({ report }: { report: KeyUsageReport }) {
  const { t } = useTranslation();
  const { summary, previous } = report;
  const rate = errorRate(summary);
  const metrics: Metric[] = [
    {
      id: "requests",
      label: t("reports.summary.requests"),
      value: countLabel(summary.requests),
      change: percentChange(summary.requests, previousValue(previous, (m) => m.requests)),
      previous: previous ? countLabel(previous.requests) : undefined,
    },
    {
      id: "tokens",
      label: t("reports.summary.tokens"),
      value: tokenLabel(totalTokens(summary)),
      sub: t("reports.summary.tokensSub", {
        input: tokenLabel(summary.inputTokens),
        cache: tokenLabel(summary.cachedInputTokens),
        output: tokenLabel(summary.outputTokens),
      }),
      change: percentChange(totalTokens(summary), previousValue(previous, totalTokens)),
      previous: previous ? tokenLabel(totalTokens(previous)) : undefined,
    },
    {
      id: "cost",
      label: t("reports.summary.cost"),
      value: costLabel(summary.costUsd),
      sub: t("reports.summary.costPerDay", { cost: costLabel(report.period.days > 0 ? summary.costUsd / report.period.days : 0) }),
      change: percentChange(summary.costUsd, previousValue(previous, (m) => m.costUsd)),
      previous: previous ? costLabel(previous.costUsd) : undefined,
    },
    {
      id: "errors",
      label: t("reports.summary.errors"),
      value: countLabel(summary.errors),
      sub: t("reports.summary.errorRate", { rate: rateLabel(rate) }),
      change: percentChange(summary.errors, previousValue(previous, (m) => m.errors)),
      previous: previous ? countLabel(previous.errors) : undefined,
      risingIsBad: true,
    },
    {
      id: "active-keys",
      label: t("reports.summary.activeKeys"),
      value: t("reports.summary.activeKeysValue", { active: report.activeKeys, total: report.selection.resolvedKeyCount }),
      change: null,
    },
  ];

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5" data-testid="key-report-summary">
      {metrics.map((metric) => (
        <div key={metric.id} data-testid={`key-report-metric-${metric.id}`} className="rounded-xl border bg-card p-4">
          <div className="text-[11px] font-medium tracking-wider text-muted-foreground uppercase">{metric.label}</div>
          <div className="mt-1 flex flex-wrap items-baseline gap-x-2">
            <div className="text-[1.5rem] font-semibold tracking-[-0.02em] text-foreground tabular-nums">{metric.value}</div>
            {metric.change !== null ? (
              <div
                data-testid="previous-period-change"
                title={t("reports.summary.previousPeriod", { value: metric.previous })}
                className={cn(
                  "text-xs font-medium tabular-nums",
                  metric.risingIsBad && metric.change > 0
                    ? "text-red-600 dark:text-red-400"
                    : metric.risingIsBad && metric.change < 0
                      ? "text-emerald-600 dark:text-emerald-400"
                      : "text-muted-foreground",
                )}
              >
                {percentChangeLabel(metric.change)}
                <span className="sr-only"> {t("reports.summary.previousPeriod", { value: metric.previous })}</span>
              </div>
            ) : null}
          </div>
          {metric.sub ? <div className="mt-0.5 text-xs text-muted-foreground">{metric.sub}</div> : null}
        </div>
      ))}
    </div>
  );
}

export function KeyReportSummarySkeleton() {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5" data-testid="key-report-summary-skeleton">
      {Array.from({ length: 5 }, (_, index) => (
        <Skeleton key={index} className="h-[92px] rounded-xl" />
      ))}
    </div>
  );
}
