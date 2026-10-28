import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import type { OverviewAction } from "@/features/overview/components/overview-actions";
import { agoLabel, percentLabel, rateLabel, untilLabel, usdLabel } from "@/features/overview/overview-format";
import type { GroupRow, ProblemAccount } from "@/features/overview/schemas";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { cn } from "@/lib/utils";

function problemAction(account: ProblemAccount): OverviewAction {
  switch (account.action) {
    case "reauth":
      return { kind: "reauth", target: account.id };
    case "resume":
      return { kind: "resume", target: account.id };
  }
  return { kind: "open_account", target: account.id };
}

/** Problem accounts (up to 10) and top keys by spend today (up to 5) of one group row. */
export function GroupRowDetail({ row, onAction }: { row: GroupRow; onAction: (action: OverviewAction) => void }) {
  const { t } = useTranslation();
  const blurred = usePrivacyStore((s) => s.blurred);
  return (
    <div className="grid gap-4 bg-muted/30 px-4 py-3 lg:grid-cols-2">
      <div className="min-w-0 space-y-2">
        <h3 className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{t("overview.groups.problemAccounts")}</h3>
        {row.problemAccounts.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("overview.groups.noProblemAccounts")}</p>
        ) : (
          <ul className="space-y-1.5">
            {row.problemAccounts.map((account) => (
              <li key={account.id} className="flex items-center gap-2 text-xs">
                <span className={cn("min-w-0 flex-1 truncate font-medium", blurred && account.label.includes("@") ? "privacy-blur" : undefined)}>
                  {account.label}
                </span>
                <span className="shrink-0 text-muted-foreground">{t(`overview.health.${account.health}`)}</span>
                <span className="shrink-0 tabular-nums text-muted-foreground" title={t("overview.windows.5h")}>
                  5h {percentLabel(account.primaryRemainingPercent)}
                  {untilLabel(account.primaryResetAt) ? ` · ${untilLabel(account.primaryResetAt)}` : ""}
                </span>
                <span className="shrink-0 tabular-nums text-muted-foreground" title={t("overview.windows.weekly")}>
                  {t("overview.windows.weeklyShort")} {percentLabel(account.secondaryRemainingPercent)}
                </span>
                <Button type="button" size="sm" variant="outline" className="h-6 px-2 text-[11px]" onClick={() => onAction(problemAction(account))}>
                  {t(`overview.groups.accountActions.${account.action}`)}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="min-w-0 space-y-2">
        <h3 className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{t("overview.groups.topKeys")}</h3>
        {row.topKeys.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("overview.groups.noKeys")}</p>
        ) : (
          <table className="w-full text-xs">
            <thead>
              <tr className="text-left text-[11px] text-muted-foreground">
                <th className="py-1 font-medium">{t("overview.columns.key")}</th>
                <th className="py-1 text-right font-medium">{t("overview.columns.spendToday")}</th>
                <th className="py-1 text-right font-medium">{t("overview.columns.spend7d")}</th>
                <th className="py-1 text-right font-medium">{t("overview.columns.limitFill")}</th>
                <th className="py-1 text-right font-medium">{t("overview.columns.errorsLastHour")}</th>
                <th className="py-1 text-right font-medium">{t("overview.columns.lastUsed")}</th>
              </tr>
            </thead>
            <tbody>
              {row.topKeys.map((key) => (
                <tr key={key.id} className="border-t border-border/50">
                  <td className="max-w-40 truncate py-1">
                    <Link className="hover:underline" to={`/keys?selected=${encodeURIComponent(key.id)}`}>
                      {key.name}
                    </Link>
                  </td>
                  <td className="py-1 text-right tabular-nums">{usdLabel(key.spendTodayUsd)}</td>
                  <td className="py-1 text-right tabular-nums">{usdLabel(key.spend7dUsd)}</td>
                  <td className="py-1 text-right tabular-nums">{percentLabel(key.maxLimitPercent)}</td>
                  <td className="py-1 text-right tabular-nums">{rateLabel(key.errorRateLastHour)}</td>
                  <td className="py-1 text-right text-muted-foreground">{agoLabel(key.lastUsedAt) ?? t("common.time.never")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
