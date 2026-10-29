import { useTranslation } from "react-i18next";

import { HealthBar } from "@/features/overview/components/health-bar";
import { clockLabel, percentLabel, untilLabel, usdLabel } from "@/features/overview/overview-format";
import type { WindowHealth } from "@/features/overview/schemas";
import type { GroupSummaryRow } from "@/features/groups/schemas";

function WindowFigure({ label, health }: { label: string; health: WindowHealth }) {
  const { t } = useTranslation();
  const reset = untilLabel(health.nextResetAt);
  const exhaustion = health.runsOutBeforeReset ? clockLabel(health.projectedExhaustionAt) : null;
  return (
    <div className="min-w-[8rem]">
      <p className="text-[11px] uppercase tracking-wider text-muted-foreground">{label}</p>
      <p className="text-lg font-semibold tabular-nums">{percentLabel(health.remainingPercent)}</p>
      <p className="text-[11px] text-muted-foreground">
        {reset ? t("groups.pool.resetIn", { time: reset }) : "—"}
        {exhaustion ? <span className="ml-1 font-medium text-red-500">{t("groups.pool.runsOutAt", { time: exhaustion })}</span> : null}
      </p>
    </div>
  );
}

function SpendFigure({ label, value }: { label: string; value: number | null }) {
  return (
    <div className="min-w-[6rem]">
      <p className="text-[11px] uppercase tracking-wider text-muted-foreground">{label}</p>
      <p className="text-lg font-semibold tabular-nums">{usdLabel(value)}</p>
    </div>
  );
}

/** The whole pool: account health, both windows with projections and the spend of all traffic. */
export function PoolRow({ pool }: { pool: GroupSummaryRow }) {
  const { t } = useTranslation();
  return (
    <section aria-label={t("groups.pool.title")} className="flex flex-wrap items-start gap-x-8 gap-y-3 rounded-xl border bg-card p-4">
      <div className="min-w-[10rem]">
        <p className="text-[11px] uppercase tracking-wider text-muted-foreground">{t("groups.pool.title")}</p>
        <HealthBar counts={pool.accounts} className="mt-1" />
      </div>
      <WindowFigure label={t("overview.windows.5h")} health={pool.primary} />
      <WindowFigure label={t("overview.windows.weekly")} health={pool.secondary} />
      <SpendFigure label={t("groups.columns.spendToday")} value={pool.spendTodayUsd} />
      <SpendFigure label={t("groups.columns.spend7d")} value={pool.spend7dUsd} />
      <SpendFigure label={t("groups.columns.spend30d")} value={pool.spend30dUsd} />
      <div className="min-w-[6rem]">
        <p className="text-[11px] uppercase tracking-wider text-muted-foreground">{t("groups.columns.keys")}</p>
        <p className="text-lg font-semibold tabular-nums">
          {pool.keys.active}/{pool.keys.total}
        </p>
      </div>
    </section>
  );
}
