import { useTranslation } from "react-i18next";

import { costLabel, countLabel, errorRate, rateLabel, shareLabel, tokenLabel, totalTokens } from "../report-format";
import type { GroupReportRow } from "../schemas";

/**
 * One row per group of the resolved keys and one for the ungrouped keys,
 * ordered by cost as the server sends them; shown when the report spans
 * more than one group.
 */
export function GroupReportTable({ groups }: { groups: GroupReportRow[] }) {
  const { t } = useTranslation();
  if (groups.length <= 1) {
    return null;
  }
  return (
    <section aria-labelledby="reports-groups-title" className="rounded-xl border bg-card">
      <div className="border-b px-4 py-2">
        <h2 id="reports-groups-title" className="text-[13px] font-medium tracking-wider text-muted-foreground uppercase">
          {t("reports.groups.title")}
        </h2>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-[11px] tracking-wider text-muted-foreground uppercase">
              <th className="px-3 py-2 font-medium">{t("reports.columns.group")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("reports.columns.activeKeys")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("reports.columns.requests")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("reports.columns.tokens")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("reports.columns.cost")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("reports.columns.share")}</th>
              <th className="px-3 py-2 text-right font-medium">{t("reports.columns.errors")}</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((group) => (
              <tr key={group.id ?? "ungrouped"} data-testid="group-report-row" className="border-b last:border-0">
                <td className="px-3 py-2 font-medium">{group.id === null ? t("reports.ungroupedKeys") : group.name}</td>
                <td className="px-3 py-2 text-right tabular-nums">
                  {t("reports.groups.activeOfSelected", { active: group.activeKeys, total: group.keyCount })}
                </td>
                <td className="px-3 py-2 text-right tabular-nums">{countLabel(group.measures.requests)}</td>
                <td className="px-3 py-2 text-right tabular-nums">{tokenLabel(totalTokens(group.measures))}</td>
                <td className="px-3 py-2 text-right tabular-nums">{costLabel(group.measures.costUsd)}</td>
                <td className="px-3 py-2 text-right tabular-nums">{shareLabel(group.share)}</td>
                <td className="px-3 py-2 text-right tabular-nums">
                  {countLabel(group.measures.errors)}
                  {group.measures.errors > 0 ? (
                    <span className="ml-1 text-xs text-muted-foreground">({rateLabel(errorRate(group.measures))})</span>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
