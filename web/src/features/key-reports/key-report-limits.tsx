import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatKeyLimitValue } from "@/features/api-keys/components/limit-rules-utils";
import { ApiKeyLimitSummary, ApiKeyStatusBadge, LimitUsageBar } from "@/features/apis/components/api-key-limit-usage";
import { cn } from "@/lib/utils";
import { formatPercentNullable, formatResetRelative, parseDate } from "@/utils/formatters";
import type { KeyReport } from "./api";

const cardClass = "min-w-0 rounded-xl border bg-card p-4";
const headingClass = "text-[11px] font-medium uppercase tracking-wider text-muted-foreground";
const valueClass = "text-[1.625rem] font-semibold tracking-[-0.02em] tabular-nums";

function ResetTime({ resetAt, now, nearest = false }: { resetAt?: string | null; now: number; nearest?: boolean }) {
  const { t } = useTranslation();
  const date = parseDate(resetAt);
  return <p className="mt-2 text-xs tabular-nums text-muted-foreground">
    {!date ? t("keyReports.resetUnknown") : <time dateTime={date.toISOString()} title={date.toLocaleString()}>
      {date.getTime() <= now ? t("keyReports.resetPending")
        : t(nearest ? "keyReports.nextReset" : "keyReports.reset", { time: formatResetRelative(date.getTime() - now) })}
    </time>}
  </p>;
}

export function KeyReportLimits({ limits, group }: Pick<KeyReport, "limits" | "group">) {
  const { t, i18n } = useTranslation();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, []);
  const quota = group?.accountQuota;
  const showSharedQuota = limits.length === 0;
  const partialCredits = quota && quota.purchasedCredits !== null && !quota.creditsUnlimited && quota.creditsKnownAccountCount < quota.accountCount;
  return (
    <div className="space-y-3">
      <div className={cn("grid gap-3", quota && (showSharedQuota ? "lg:grid-cols-3" : "md:grid-cols-2"))}>
        <section aria-labelledby="personal-key-limits" className={cardClass}>
          <h2 id="personal-key-limits" className={headingClass} title={t("keyReports.currentLimitsNote")}>{t("keyReports.personalLimits")}</h2>
          {limits.length === 0 ? <p className={cn(valueClass, "mt-2")}>{t("common.states.unlimited")}</p>
            : <div className="mt-3 space-y-4">{limits.map((limit) => {
              const label = t("apis.keyInfo.limitTypes." + limit.limitType) + " · " + t("keyReports.limitWindows." + limit.limitWindow);
              const percent = limit.maxValue > 0 ? limit.currentValue / limit.maxValue * 100 : 0;
              return <div key={limit.id}>
                <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-2 text-xs text-muted-foreground">
                  <span>{label}</span>
                  {limit.modelFilter ? <span className="max-w-full truncate" title={limit.modelFilter}>{limit.modelFilter}</span> : null}
                </div>
                <div className="mb-2 flex flex-wrap items-baseline justify-between gap-x-2">
                  <span className={valueClass}>{formatPercentNullable(percent)}</span>
                  <span className="text-xs tabular-nums text-muted-foreground">{formatKeyLimitValue(limit.currentValue, limit.limitType)} / {formatKeyLimitValue(limit.maxValue, limit.limitType)}</span>
                </div>
                <LimitUsageBar percent={percent} label={label} />
                <ResetTime resetAt={limit.resetAt} now={now} />
              </div>;
            })}</div>}
        </section>
        {quota ? <>
          {showSharedQuota ? <section aria-labelledby="group-account-quota" className={cardClass}>
            <div className="flex flex-wrap items-baseline justify-between gap-x-2">
              <h2 id="group-account-quota" className={headingClass} title={t("keyReports.groupAccountQuotaNote")}>{t("keyReports.groupAccountQuota")}</h2>
              <p className="min-w-0 truncate text-xs text-muted-foreground" title={group.name}>{group.name}</p>
            </div>
            {quota.windows.length > 0 ? <div className="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-1 xl:grid-cols-2">
              {quota.windows.map((window) => (
                <div key={window.window} title={t("keyReports.quotaCoverage", { known: window.accountCount })}>
                  <p className="text-xs text-muted-foreground">{t("keyReports.quotaWindows." + window.window)}</p>
                  <p className={cn(valueClass, "mb-2")}>{formatPercentNullable(window.usedPercent)}</p>
                  <LimitUsageBar percent={window.usedPercent} label={t("keyReports.quotaWindows." + window.window)} />
                  <ResetTime resetAt={window.nextResetAt} now={now} nearest />
                </div>
              ))}
            </div> : <p className="mt-3 text-sm text-muted-foreground">{t("keyReports.quotaUnavailable")}</p>}
          </section> : null}
          <section aria-labelledby="purchased-credit-balance" className={cardClass}>
            <h2 id="purchased-credit-balance" className={headingClass} title={t("keyReports.purchasedCreditsHelp")}>{t("keyReports.purchasedCreditsRemaining")}</h2>
            <p className={cn(valueClass, "mt-2 break-words")}>
              {quota.creditsUnlimited ? t("common.states.unlimited") : quota.purchasedCredits === null ? "—"
                : (partialCredits ? "≥ " : "") + quota.purchasedCredits.toLocaleString(i18n.language, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">{t("keyReports.extraCreditsNote")}</p>
            {partialCredits ? <p className="mt-2 text-xs text-amber-600 dark:text-amber-400"
              title={t("keyReports.creditCoverage", { known: quota.creditsKnownAccountCount, total: quota.accountCount })}>{t("keyReports.partialCredits")}</p> : null}
          </section>
        </> : null}
      </div>
      {group ? <details className="rounded-xl border bg-card">
        <summary className="cursor-pointer rounded-xl px-4 py-3 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring">
          {t("keyReports.groupLimits", { name: group.name })}
          <span className="ml-2 text-xs tabular-nums text-muted-foreground">({group.keys.length})</span>
        </summary>
        <ul className="grid gap-3 border-t p-4 sm:grid-cols-2 lg:grid-cols-3">
          {group.keys.map((key) => (
            <li key={key.id} className="min-w-0 space-y-2 rounded-lg border p-3">
              <div className="flex items-center gap-2.5">
                <p className="min-w-0 flex-1 truncate text-sm font-medium" title={key.name}>
                  {key.name}{key.isCurrent ? " (" + t("keyReports.currentKey") + ")" : ""}
                </p>
                <ApiKeyStatusBadge apiKey={key} />
              </div>
              {key.limits.length > 0 ? <ApiKeyLimitSummary limits={key.limits} />
                : <p className="text-xs text-muted-foreground">{t("apis.keyInfo.noLimitsConfigured")}</p>}
            </li>
          ))}
        </ul>
      </details> : null}
    </div>
  );
}
