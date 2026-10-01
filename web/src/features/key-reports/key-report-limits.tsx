import { useTranslation } from "react-i18next";

import { ApiKeyLimitDetails, ApiKeyLimitSummary, ApiKeyStatusBadge } from "@/features/apis/components/api-key-limit-usage";
import type { KeyReport } from "./api";
import { formatPercentNullable } from "@/utils/formatters";
import { formatCreditValue } from "@/features/dashboard/account-credit-display";

export function KeyReportLimits({ limits, group }: Pick<KeyReport, "limits" | "group">) {
  const { t } = useTranslation();
  if (limits.length === 0 && !group) return null;
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">{t("keyReports.currentLimitsNote")}</p>
      {limits.length > 0 ? (
        <section aria-labelledby="personal-key-limits" className="space-y-3 rounded-xl border bg-card p-5">
          <h2 id="personal-key-limits" className="font-semibold">{t("keyReports.personalLimits")}</h2>
          <ApiKeyLimitDetails limits={limits} />
        </section>
      ) : null}
      {group ? (
        <section aria-labelledby="group-key-limits" className="space-y-3 rounded-xl border bg-card p-5">
          <h2 id="group-key-limits" className="font-semibold">{t("keyReports.groupLimits", { name: group.name })}</h2>
          {group.accountQuota ? (
            <section aria-labelledby="group-account-quota" className="space-y-2 rounded-lg bg-muted/40 p-3">
              <h3 id="group-account-quota" className="text-sm font-medium">{t("keyReports.groupAccountQuota")}</h3>
              <p className="text-xs text-muted-foreground">{t("keyReports.groupAccountQuotaNote")}</p>
              <div className="space-y-1">
                <p className="text-xs text-muted-foreground">{t("keyReports.purchasedCreditsRemaining")}</p>
                <p className="text-lg font-semibold tabular-nums">{group.accountQuota.creditsUnlimited
                  ? t("common.states.unlimited") : formatCreditValue(group.accountQuota.purchasedCredits)}</p>
                <p className="text-xs text-muted-foreground">{t("keyReports.creditCoverage", {
                  known: group.accountQuota.creditsKnownAccountCount, total: group.accountQuota.accountCount,
                })}</p>
              </div>
              {group.accountQuota.windows.length > 0 ? <dl className="flex flex-wrap gap-x-8 gap-y-3">
                {group.accountQuota.windows.map((window) => (
                  <div key={window.window} className="space-y-1">
                    <dt className="text-xs text-muted-foreground">{t(`keyReports.quotaWindows.${window.window}`)}</dt>
                    <dd className="text-lg font-semibold tabular-nums">{formatPercentNullable(window.usedPercent)}</dd>
                    <dd className="text-xs text-muted-foreground">{t("keyReports.quotaCoverage", {
                      known: window.accountCount, total: group.accountQuota?.accountCount,
                    })}</dd>
                  </div>
                ))}
              </dl> : <p className="text-sm text-muted-foreground">{t("keyReports.quotaUnavailable")}</p>}
            </section>
          ) : null}
          <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {group.keys.map((key) => (
              <li key={key.id} className="min-w-0 space-y-2 rounded-lg border p-3">
                <div className="flex items-center gap-2.5">
                  <p className="min-w-0 flex-1 truncate text-sm font-medium" title={key.name}>
                    {key.name}{key.isCurrent ? ` (${t("keyReports.currentKey")})` : ""}
                  </p>
                  <ApiKeyStatusBadge apiKey={key} />
                </div>
                {key.limits.length > 0 ? <ApiKeyLimitSummary limits={key.limits} />
                  : <p className="text-xs text-muted-foreground">{t("apis.keyInfo.noLimitsConfigured")}</p>}
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}
