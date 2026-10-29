import { useTranslation } from "react-i18next";

import { msLabel, tpsLabel } from "@/features/key-page/key-page-format";
import { SectionCard, TableHeadRow, Th } from "@/features/key-page/components/section-card";
import type { KeyDay, KeyPeriodSummary } from "@/features/key-page/schemas";
import { formatNumber } from "@/utils/formatters";

/**
 * Latency per local day from the rollup histograms (R22): TTFT p50 and p95,
 * total latency p50 and p95, queue p95 and tokens per second p50, newest
 * day first, with the period row on top.
 */
export function KeyLatencySection({ daily, summary }: { daily: KeyDay[]; summary: KeyPeriodSummary }) {
  const { t } = useTranslation();
  const days = [...daily].reverse();
  return (
    <SectionCard id="key-page-latency" title={t("keyPage.latency.title")}>
      <div className="max-h-96 overflow-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 bg-card">
            <TableHeadRow>
              <Th>{t("keyPage.columns.day")}</Th>
              <Th align="right">{t("keyPage.columns.requests")}</Th>
              <Th align="right">{t("keyPage.columns.ttftP50")}</Th>
              <Th align="right">{t("keyPage.columns.ttftP95")}</Th>
              <Th align="right">{t("keyPage.columns.totalP50")}</Th>
              <Th align="right">{t("keyPage.columns.totalP95")}</Th>
              <Th align="right">{t("keyPage.columns.queueP95")}</Th>
              <Th align="right">{t("keyPage.columns.tpsP50")}</Th>
            </TableHeadRow>
          </thead>
          <tbody>
            <tr className="border-b bg-muted/30 font-medium" data-testid="key-latency-period">
              <td className="px-3 py-1.5 text-xs">{t("keyPage.latency.period")}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{formatNumber(summary.requests)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(summary.ttftP50Ms)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(summary.ttftP95Ms)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(summary.totalP50Ms)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(summary.totalP95Ms)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(summary.queueP95Ms)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{tpsLabel(summary.tpsP50)}</td>
            </tr>
            {days.map((day) => (
              <tr key={day.date} className="border-b last:border-0" data-testid="key-latency-row">
                <td className="px-3 py-1.5 text-xs tabular-nums">{day.date}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{formatNumber(day.requests)}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(day.ttftP50Ms)}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(day.ttftP95Ms)}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(day.totalP50Ms)}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(day.totalP95Ms)}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(day.queueP95Ms)}</td>
                <td className="px-3 py-1.5 text-right tabular-nums">{tpsLabel(day.tpsP50)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </SectionCard>
  );
}
