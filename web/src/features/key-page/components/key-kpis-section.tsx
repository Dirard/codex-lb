import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  countLabel,
  deltaPercent,
  fractionPercent,
  msLabel,
  usd,
} from "@/features/key-page/key-page-format";
import type { KeyPagePeriod } from "@/features/key-page/period";
import type { KeyPeriodSummary } from "@/features/key-page/schemas";
import { cn } from "@/lib/utils";
import { formatNumber } from "@/utils/formatters";

function Delta({ current, previous, format }: { current: number; previous: number | null; format: (value: number) => string }) {
  const { t } = useTranslation();
  if (previous === null) return <span>{t("keyPage.kpis.noPrevious")}</span>;
  const delta = deltaPercent(current, previous);
  const title = t("keyPage.kpis.previousValue", { value: format(previous) });
  if (delta === null) return <span title={title}>{t("keyPage.kpis.previousZero")}</span>;
  const sign = delta > 0 ? "+" : delta < 0 ? "−" : "±";
  return (
    <span title={title} className={cn(delta > 0 && "text-foreground")}>
      {t("keyPage.kpis.delta", { value: `${sign}${Math.abs(delta).toFixed(Math.abs(delta) < 10 ? 1 : 0)}%` })}
    </span>
  );
}

function Kpi({ label, value, children }: { label: string; value: string; children?: ReactNode }) {
  return (
    <div className="min-w-0 space-y-0.5" data-testid="key-kpi">
      <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</p>
      <p className="text-lg font-semibold tabular-nums">{value}</p>
      {children ? <p className="truncate text-[11px] tabular-nums text-muted-foreground">{children}</p> : null}
    </div>
  );
}

/** The period KPIs with the previous period of equal length (R24 section 4). */
export function KeyKpisSection({ summary, previousPeriod }: { summary: KeyPeriodSummary; previousPeriod: KeyPagePeriod }) {
  const { t } = useTranslation();
  const previous = summary.previous;
  const tokens = summary.inputTokens + summary.outputTokens;
  return (
    <section aria-labelledby="key-page-kpis-title" className="rounded-xl border bg-card p-4">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="key-page-kpis-title" className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
          {t("keyPage.kpis.title")}
        </h2>
        <p className="text-[11px] tabular-nums text-muted-foreground">
          {t("keyPage.kpis.comparedWith", { from: previousPeriod.from, to: previousPeriod.to })}
        </p>
      </div>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        <Kpi label={t("keyPage.kpis.requests")} value={formatNumber(summary.requests)}>
          <Delta current={summary.requests} previous={previous?.requests ?? null} format={formatNumber} />
        </Kpi>
        <Kpi label={t("keyPage.kpis.tokens")} value={countLabel(tokens)}>
          <Delta current={tokens} previous={previous?.tokens ?? null} format={countLabel} />
        </Kpi>
        <Kpi label={t("keyPage.kpis.cost")} value={usd(summary.costUsd)}>
          <Delta current={summary.costUsd} previous={previous?.costUsd ?? null} format={usd} />
        </Kpi>
        <Kpi label={t("keyPage.kpis.errors")} value={formatNumber(summary.errors)}>
          {t("keyPage.kpis.errorRate", { value: fractionPercent(summary.errorRate) })}
          {" · "}
          {t("keyPage.kpis.cancelled", { count: summary.cancelled })}
        </Kpi>
        <Kpi label={t("keyPage.kpis.ttft")} value={msLabel(summary.ttftP50Ms)}>
          {t("keyPage.kpis.p95", { value: msLabel(summary.ttftP95Ms) })}
        </Kpi>
        <Kpi label={t("keyPage.kpis.totalLatency")} value={msLabel(summary.totalP50Ms)}>
          {t("keyPage.kpis.p95", { value: msLabel(summary.totalP95Ms) })}
        </Kpi>
      </div>
      <dl className="mt-3 flex flex-wrap gap-x-5 gap-y-1 border-t pt-3 text-xs tabular-nums">
        <div className="flex gap-1.5">
          <dt className="text-muted-foreground">{t("common.units.input")}</dt>
          <dd className="font-medium">{countLabel(summary.inputTokens)}</dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="text-muted-foreground">{t("common.units.output")}</dt>
          <dd className="font-medium">{countLabel(summary.outputTokens)}</dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="text-muted-foreground">{t("common.units.cached")}</dt>
          <dd className="font-medium">{countLabel(summary.cachedInputTokens)}</dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="text-muted-foreground">{t("keyPage.kpis.cacheShare")}</dt>
          <dd className="font-medium">{fractionPercent(summary.cacheShare)}</dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="text-muted-foreground">{t("keyPage.kpis.reasoning")}</dt>
          <dd className="font-medium">{countLabel(summary.reasoningTokens)}</dd>
        </div>
      </dl>
    </section>
  );
}
