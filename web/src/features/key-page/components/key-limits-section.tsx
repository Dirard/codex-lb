import { useTranslation } from "react-i18next";

import { LimitUsageBar } from "@/features/api-keys/components/api-key-limit-usage";
import { KeyReportGroupKeys } from "@/features/key-reports/key-report-limits";
import { clockOrDateLabel, dateTimeLabel, limitValue, sharePercent } from "@/features/key-page/key-page-format";
import { CurrentBadge, SectionCard } from "@/features/key-page/components/section-card";
import type { KeyLimitState, KeyPageGroup, KeyPagePool } from "@/features/key-page/schemas";
import { WindowCell } from "@/features/overview/components/pool-summary-strip";
import { formatResetRelative, parseDate } from "@/utils/formatters";

function ResetLabel({ resetAt, now }: { resetAt: string; now: number }) {
  const { t } = useTranslation();
  const date = parseDate(resetAt);
  if (!date) return <>{t("keyReports.resetUnknown")}</>;
  const remaining = date.getTime() - now;
  return (
    <time dateTime={date.toISOString()}>
      {t("keyPage.limits.resets", {
        time: dateTimeLabel(resetAt),
        relative: remaining > 0 ? formatResetRelative(remaining) : t("keyReports.resetPending"),
      })}
    </time>
  );
}

function Projection({ limit }: { limit: KeyLimitState }) {
  const { t } = useTranslation();
  if (limit.projectedExhaustionAt) {
    return (
      <p className="text-[11px] font-medium text-red-600 dark:text-red-400">
        {t("keyPage.limits.runsOutAt", { time: clockOrDateLabel(limit.projectedExhaustionAt) })}
      </p>
    );
  }
  if (limit.max > 0 && limit.current >= limit.max) {
    return <p className="text-[11px] font-medium text-red-600 dark:text-red-400">{t("keyPage.limits.reached")}</p>;
  }
  return <p className="text-[11px] text-muted-foreground">{t("keyPage.limits.lastsUntilReset")}</p>;
}

function LimitRow({ limit, now }: { limit: KeyLimitState; now: number }) {
  const { t } = useTranslation();
  const label = `${t(`apis.keyInfo.limitTypes.${limit.type}`)} · ${t(`keyReports.limitWindows.${limit.window}`)}`;
  return (
    <li className="min-w-0 space-y-1.5 rounded-lg border p-3" data-testid="key-limit">
      <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-2 text-xs">
        <span className="font-medium">{label}</span>
        <span className="max-w-full truncate text-muted-foreground" title={limit.modelFilter ?? undefined}>
          {limit.modelFilter ?? t("keyPage.limits.allModels")}
        </span>
      </div>
      <div className="flex flex-wrap items-baseline justify-between gap-x-2">
        <span className="text-lg font-semibold tabular-nums">{sharePercent(limit.percent)}</span>
        <span className="text-xs tabular-nums text-muted-foreground">
          {limitValue(limit.current, limit.type)} / {limitValue(limit.max, limit.type)}
        </span>
      </div>
      <LimitUsageBar percent={limit.percent} label={label} />
      <p className="text-[11px] tabular-nums text-muted-foreground">
        <ResetLabel resetAt={limit.resetAt} now={now} />
        {limit.expired ? ` · ${t("keyPage.limits.expiredWindow")}` : ""}
        {limit.pacePerHour !== null && limit.pacePerHour > 0
          ? ` · ${t("keyPage.limits.pace", { value: limitValue(Math.round(limit.pacePerHour), limit.type) })}`
          : ""}
      </p>
      <Projection limit={limit} />
    </li>
  );
}

function PoolRemaining({ pool }: { pool: KeyPagePool }) {
  const { t, i18n } = useTranslation();
  const credits = pool.purchasedCredits;
  const partial = credits !== null && !credits.unlimited && credits.remaining !== null && credits.knownAccountCount < pool.accountCount;
  return (
    <div className="rounded-lg border bg-muted/20 p-3" data-testid="key-pool-remaining">
      <h3 className="mb-2 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        {t(pool.scope === "group" ? "keyPage.limits.groupRemaining" : "keyPage.limits.poolRemaining", { accounts: pool.accountCount })}
      </h3>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3">
        <WindowCell label={t("overview.pool.primary")} health={pool.primary} />
        <WindowCell label={t("overview.pool.secondary")} health={pool.secondary} />
        <div className="min-w-0 space-y-0.5">
          <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{t("keyReports.purchasedCreditsRemaining")}</p>
          <p className="text-lg font-semibold tabular-nums">
            {credits === null
              ? "—"
              : credits.unlimited
                ? t("common.states.unlimited")
                : credits.remaining === null
                  ? "—"
                  : (partial ? "≥ " : "") + credits.remaining.toLocaleString(i18n.language, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
          </p>
          {credits && partial ? (
            <p className="text-[11px] text-amber-600 dark:text-amber-400">
              {t("keyReports.creditCoverage", { known: credits.knownAccountCount, total: pool.accountCount })}
            </p>
          ) : (
            <p className="text-[11px] text-muted-foreground">{t("keyReports.extraCreditsNote")}</p>
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * The key's current limits (independent of the period), each with used and
 * maximum, reset and the projection at the last 3 hours' pace (R21), then
 * the pool or group remaining and, for a grouped key, the same-group list.
 */
export function KeyLimitsSection({
  limits,
  pool,
  group,
  now,
}: {
  limits: KeyLimitState[];
  pool: KeyPagePool | null;
  group: KeyPageGroup | null;
  now: number;
}) {
  const { t } = useTranslation();
  return (
    <SectionCard id="key-page-limits" title={t("keyPage.limits.title")} badge={<CurrentBadge />}>
      <div className="space-y-3 p-4">
        {limits.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("keyPage.limits.none")}</p>
        ) : (
          <ul className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {limits.map((limit, index) => (
              <LimitRow key={`${limit.type}:${limit.window}:${limit.modelFilter ?? ""}:${index}`} limit={limit} now={now} />
            ))}
          </ul>
        )}
        {pool ? <PoolRemaining pool={pool} /> : null}
        {group && group.keys.length > 0 ? (
          <KeyReportGroupKeys name={group.name} keys={group.keys} />
        ) : null}
      </div>
    </SectionCard>
  );
}
