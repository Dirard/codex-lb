import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { ServerWeeklyCreditPace } from "@/features/dashboard/schemas";
import { useDateDisplayFormatStore } from "@/hooks/use-date-format";
import { cn } from "@/lib/utils";
import { formatCompactNumber, formatDateTimeInline } from "@/utils/formatters";

type RunwayStatus = NonNullable<ServerWeeklyCreditPace["runwayStatus"]>;

const VERDICT_KEYS: Record<RunwayStatus, string> = {
  safe: "dashboard.weeklyPace.verdict.safe",
  tight: "dashboard.weeklyPace.verdict.tight",
  runs_dry: "dashboard.weeklyPace.verdict.runsDry",
};

const VERDICT_BADGE: Record<RunwayStatus, string> = {
  safe: "border-border bg-muted/40 text-muted-foreground",
  tight: "border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-300",
  runs_dry: "border-red-500/25 bg-red-500/10 text-red-700 dark:text-red-300",
};

const VERDICT_FILL: Record<RunwayStatus, string> = {
  safe: "bg-primary/50",
  tight: "bg-amber-500/60",
  runs_dry: "bg-red-500/60",
};

/** The horizon is at least two days and reaches the latest reset event. */
const MIN_HORIZON_HOURS = 48;

function durationLabel(hours: number, t: TFunction): string {
  const totalMinutes = Math.max(1, Math.ceil(hours * 60));
  const days = Math.floor(totalMinutes / 1440);
  const hoursPart = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;
  if (days > 0) {
    return hoursPart > 0 ? t("formatters.duration.daysHours", { days, hours: hoursPart }) : t("formatters.duration.days", { count: days });
  }
  if (hoursPart > 0) {
    return minutes > 0 ? t("formatters.duration.hoursMinutes", { hours: hoursPart, minutes }) : t("formatters.duration.hours", { count: hoursPart });
  }
  return t("formatters.duration.minutes", { count: minutes });
}

function headroomLabel(value: number): string {
  const clamped = Math.max(0, value);
  return clamped > 0 && clamped < 10 ? `${clamped.toFixed(1)}%` : `${Math.round(clamped)}%`;
}

function finite(value: number | null | undefined): number | null {
  return value != null && Number.isFinite(value) ? value : null;
}

/**
 * The pool runway card (Q2): the weekly credit headroom of the whole pool,
 * when it runs out at the recent burn, the next relief from resets and, when
 * the pool runs dry first, the throttle-to and add-Pro recommendations. It
 * reads `GET /api/dashboard/projections` once per visit.
 */
export function PoolRunwayCard({ pace, loading }: { pace: ServerWeeklyCreditPace | null | undefined; loading: boolean }) {
  const { t } = useTranslation();
  const dateDisplayFormat = useDateDisplayFormatStore((state) => state.dateDisplayFormat);
  // The card is read once per visit, so its timeline is anchored at mount.
  const [now] = useState(() => Date.now());
  if (!pace || pace.runwayStatus == null || pace.headroomPercent == null || pace.headroomCredits == null) {
    return (
      <section aria-label={t("groups.runway.title")} className="rounded-xl border bg-card p-4 text-sm text-muted-foreground">
        <h2 className="mb-1 text-[13px] font-medium uppercase tracking-wider">{t("groups.runway.title")}</h2>
        {loading ? t("common.loading") : t("groups.runway.unavailable")}
      </section>
    );
  }
  const status = pace.runwayStatus;
  const runsDry = status === "runs_dry";
  const eta = finite(pace.depletionEtaHours);
  const relief = finite(pace.nextReliefInHours);
  const burn = pace.burnRateRecentCreditsPerHour ?? null;
  const resets = (pace.resetEvents ?? []).flatMap((event) => {
    const at = Date.parse(event.at);
    const hours = (at - now) / 3_600_000;
    return Number.isFinite(at) && hours >= 0 ? [{ at, hours, credits: event.creditsReturned }] : [];
  });
  const horizon = Math.max(eta ?? 0, relief ?? 0, ...resets.map((event) => event.hours), MIN_HORIZON_HOURS);
  const etaPercent = eta != null ? Math.min(100, Math.max(0, (eta / horizon) * 100)) : null;
  const throttleTo = runsDry && pace.throttleToPercent != null ? pace.throttleToPercent : null;
  const addPro = pace.addProAccounts != null && pace.addProAccounts > 0 ? pace.addProAccounts : null;

  return (
    <section aria-label={t("groups.runway.title")} className="space-y-3 rounded-xl border bg-card p-4" data-testid="pool-runway-card">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">{t("groups.runway.title")}</h2>
        <span className={cn("rounded-full border px-2 py-0.5 text-[11px] font-medium", VERDICT_BADGE[status])}>{t(VERDICT_KEYS[status])}</span>
      </div>
      <div className="flex flex-wrap items-baseline gap-x-2">
        <p className="text-2xl font-semibold tabular-nums">{headroomLabel(pace.headroomPercent)}</p>
        <p className="text-xs tabular-nums text-muted-foreground">
          {t("dashboard.weeklyPace.heroCreditsLeft", { credits: formatCompactNumber(Math.max(0, pace.headroomCredits)) })}
        </p>
      </div>
      <p className="text-xs tabular-nums text-muted-foreground">
        {eta != null ? (
          <span className={cn(runsDry && "font-medium text-red-600 dark:text-red-400")}>
            {t("dashboard.weeklyPace.runsOutIn", { duration: durationLabel(eta, t) })}
          </span>
        ) : burn == null ? (
          t("dashboard.weeklyPace.burnNotMeasured")
        ) : (
          t("dashboard.weeklyPace.steadyState")
        )}
        {burn != null && burn > 0 ? <span className="ml-1.5">{t("dashboard.weeklyPace.atBurnRate", { credits: formatCompactNumber(burn) })}</span> : null}
      </p>
      {relief != null ? (
        <p className={cn("text-xs tabular-nums", runsDry && eta != null ? "font-medium text-red-600 dark:text-red-400" : "text-muted-foreground")}>
          {runsDry && eta != null
            ? t("dashboard.weeklyPace.runsDryBeforeRelief", { eta: durationLabel(eta, t), relief: durationLabel(relief, t) })
            : t("dashboard.weeklyPace.reliefLine", {
                duration: durationLabel(relief, t),
                credits: formatCompactNumber(Math.max(0, pace.nextReliefCredits ?? 0)),
              })}
        </p>
      ) : null}
      <div>
        <div className="relative h-1.5 rounded-full bg-muted">
          {etaPercent != null ? <div className={cn("h-full rounded-full", VERDICT_FILL[status])} style={{ width: `${etaPercent}%` }} /> : null}
          {resets.map((event, index) => (
            <div
              key={index}
              title={t("dashboard.weeklyPace.resetTickTooltip", {
                credits: formatCompactNumber(event.credits),
                time: formatDateTimeInline(new Date(event.at).toISOString(), dateDisplayFormat),
              })}
              className="absolute top-1/2 h-3 w-0.5 -translate-y-1/2 rounded-full bg-foreground/50"
              style={{ left: `${Math.min(100, (event.hours / horizon) * 100)}%` }}
            />
          ))}
        </div>
        <div className="mt-1 flex justify-between text-[10px] text-muted-foreground">
          <span>{t("dashboard.weeklyPace.timelineNow")}</span>
          <span className="tabular-nums">{durationLabel(horizon, t)}</span>
        </div>
      </div>
      {throttleTo != null || addPro != null ? (
        <div className="grid gap-1 rounded-lg border bg-background/60 px-3 py-2 text-xs" data-testid="pool-runway-recommendations">
          <p className="font-medium">{t("dashboard.weeklyPace.recommendations.title")}</p>
          {throttleTo != null ? (
            <div className="flex justify-between gap-3">
              <span className="text-muted-foreground">{t("dashboard.weeklyPace.recommendations.throttleLabel")}</span>
              <span className="tabular-nums">{t("dashboard.weeklyPace.recommendations.throttleTo", { percent: `${Math.round(throttleTo)}%` })}</span>
            </div>
          ) : null}
          {addPro != null ? (
            <div className="flex justify-between gap-3">
              <span className="text-muted-foreground">{t("dashboard.weeklyPace.recommendations.addCapacity")}</span>
              <span className="tabular-nums">{t("dashboard.weeklyPace.recommendations.addProAccounts", { count: addPro })}</span>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
