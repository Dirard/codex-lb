import { useTranslation } from "react-i18next";

import { SparklineChart } from "@/components/sparkline-chart";
import { HealthBar } from "@/features/overview/components/health-bar";
import { clockLabel, percentLabel, rateLabel, trendPoints, untilLabel, usdLabel } from "@/features/overview/overview-format";
import type { PoolSummary, WindowHealth } from "@/features/overview/schemas";
import { formatNumber } from "@/utils/formatters";

function Cell({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 space-y-0.5">
      <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</p>
      {children}
    </div>
  );
}

/** One window's remaining, next reset and projected exhaustion. */
export function WindowCell({ label, health }: { label: string; health: WindowHealth }) {
  const { t } = useTranslation();
  const reset = untilLabel(health.nextResetAt);
  const exhaustion = health.runsOutBeforeReset ? clockLabel(health.projectedExhaustionAt) : null;
  return (
    <Cell label={label}>
      <p className="text-lg font-semibold tabular-nums">{percentLabel(health.remainingPercent)}</p>
      <p className="truncate text-[11px] text-muted-foreground">
        {reset ? t("overview.window.resetsIn", { until: reset }) : t("overview.window.noReset")}
        {health.totalAccounts > 0 && health.coveredAccounts < health.totalAccounts
          ? ` · ${t("overview.window.coverage", { covered: health.coveredAccounts, total: health.totalAccounts })}`
          : ""}
      </p>
      {exhaustion ? (
        <p className="text-[11px] font-medium text-red-500">{t("overview.window.runsOutAt", { time: exhaustion })}</p>
      ) : null}
    </Cell>
  );
}

/** The pool in one dense row: health, both windows, spend, requests, errors and the 7-day trend. */
export function PoolSummaryStrip({ pool, trendsKnown }: { pool: PoolSummary; trendsKnown: boolean }) {
  const { t } = useTranslation();
  const yesterday = pool.spendYesterdaySameTimeUsd;
  return (
    <section aria-labelledby="overview-pool-title" className="rounded-xl border bg-card p-4">
      <h2 id="overview-pool-title" className="mb-3 text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
        {t("overview.pool.title")}
      </h2>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4 xl:grid-cols-7">
        <Cell label={t("overview.pool.accounts")}>
          <HealthBar counts={pool.accounts} />
          {pool.externalSources > 0 ? (
            <p className="text-[11px] text-muted-foreground">{t("overview.pool.externalSources", { count: pool.externalSources })}</p>
          ) : null}
        </Cell>
        <WindowCell label={t("overview.pool.primary")} health={pool.primary} />
        <WindowCell label={t("overview.pool.secondary")} health={pool.secondary} />
        <Cell label={t("overview.pool.spendToday")}>
          <p className="text-lg font-semibold tabular-nums">{usdLabel(pool.spendTodayUsd)}</p>
          <p className="truncate text-[11px] text-muted-foreground">
            {yesterday === null ? t("overview.pool.yesterdayUnknown") : t("overview.pool.yesterdaySameTime", { value: usdLabel(yesterday) })}
          </p>
        </Cell>
        <Cell label={t("overview.pool.requestsToday")}>
          <p className="text-lg font-semibold tabular-nums">{formatNumber(pool.requestsToday)}</p>
        </Cell>
        <Cell label={t("overview.pool.errorRateLastHour")}>
          <p className="text-lg font-semibold tabular-nums">{rateLabel(pool.errorRateLastHour)}</p>
        </Cell>
        <Cell label={t("overview.pool.spend7d")}>
          {trendsKnown && pool.trend7d ? (
            <>
              <p className="text-lg font-semibold tabular-nums">{usdLabel(pool.spend7dUsd)}</p>
              <div className="h-6">
                <SparklineChart data={trendPoints(pool.trend7d)} color="#6366f1" index={0} height={24} />
              </div>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">—</p>
          )}
        </Cell>
      </div>
    </section>
  );
}
