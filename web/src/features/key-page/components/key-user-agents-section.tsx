import { useTranslation } from "react-i18next";

import { sharePercent, usd } from "@/features/key-page/key-page-format";
import { SectionCard, TableHeadRow, Th } from "@/features/key-page/components/section-card";
import type { KeyUserAgentRow } from "@/features/key-page/schemas";
import { formatNumber } from "@/utils/formatters";

/** Requests and cost by user-agent group; the share is of the period's requests. */
export function KeyUserAgentsSection({ rows }: { rows: KeyUserAgentRow[] }) {
  const { t } = useTranslation();
  return (
    <SectionCard id="key-page-user-agents" title={t("keyPage.userAgents.title")}>
      {rows.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">{t("keyPage.userAgents.none")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <TableHeadRow>
                <Th>{t("keyPage.columns.userAgent")}</Th>
                <Th align="right">{t("keyPage.columns.requests")}</Th>
                <Th align="right">{t("keyPage.columns.cost")}</Th>
                <Th align="right">{t("keyPage.columns.share")}</Th>
              </TableHeadRow>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.group} className="border-b last:border-0" data-testid="key-user-agent-row">
                  <td className="max-w-64 truncate px-3 py-1.5 text-xs" title={row.group}>{row.group || t("keyPage.userAgents.unknown")}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{formatNumber(row.requests)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{usd(row.costUsd)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{sharePercent(row.share)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </SectionCard>
  );
}
