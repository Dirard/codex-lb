import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { agoLabel } from "@/features/overview/overview-format";
import { dateTimeLabel, dayTick, fractionPercent, sharePercent } from "@/features/key-page/key-page-format";
import { SectionCard, TableHeadRow, Th } from "@/features/key-page/components/section-card";
import { keyErrorRequestsRoute, type KeyPagePeriod } from "@/features/key-page/period";
import type { KeyDay, KeyErrorCodeRow, KeyPageMode } from "@/features/key-page/schemas";
import { formatNumber } from "@/utils/formatters";

function DailyErrorRate({ daily }: { daily: KeyDay[] }) {
  const { t } = useTranslation();
  const rates = daily.map((day) => ({ day, rate: day.requests > 0 ? day.errors / day.requests : 0 }));
  const max = Math.max(...rates.map((entry) => entry.rate), 0);
  return (
    <div className="min-w-0 space-y-1">
      <h3 className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{t("keyPage.errors.ratePerDay")}</h3>
      <ol className="flex h-20 items-end gap-px" aria-label={t("keyPage.errors.ratePerDay")}>
        {rates.map(({ day, rate }) => {
          const label = t("keyPage.errors.dayRate", {
            day: day.date,
            rate: fractionPercent(day.requests > 0 ? rate : null),
            errors: formatNumber(day.errors),
            requests: formatNumber(day.requests),
          });
          return (
            <li key={day.date} className="flex h-full min-w-[3px] flex-1 items-end" title={label} aria-label={label}>
              <span
                className={rate > 0 ? "w-full rounded-t-sm bg-red-500/70" : "w-full bg-muted"}
                style={{ height: rate > 0 && max > 0 ? `${Math.max(4, (rate / max) * 100)}%` : "2px" }}
              />
            </li>
          );
        })}
      </ol>
      {daily.length > 0 ? (
        <div className="flex justify-between text-[10px] tabular-nums text-muted-foreground">
          <span>{dayTick(daily[0].date)}</span>
          <span>{dayTick(daily[daily.length - 1].date)}</span>
        </div>
      ) : null}
    </div>
  );
}

/**
 * Errors by code (an empty code is "No code") with share and last seen hour,
 * the error rate per day and a way to the failed requests: the Requests page
 * for the administrator, the errors-only recent requests for the owner.
 */
export function KeyErrorsSection({
  mode,
  keyId,
  period,
  rows,
  daily,
  onShowErrors,
}: {
  mode: KeyPageMode;
  keyId: string | undefined;
  period: KeyPagePeriod;
  rows: KeyErrorCodeRow[];
  daily: KeyDay[];
  onShowErrors: () => void;
}) {
  const { t } = useTranslation();
  const hasErrors = rows.some((row) => row.count > 0);
  return (
    <SectionCard
      id="key-page-errors"
      title={t("keyPage.errors.title")}
      actions={
        hasErrors ? (
          mode === "admin" && keyId ? (
            <Button asChild size="sm" variant="ghost" className="h-6 px-2 text-[11px]">
              <Link to={keyErrorRequestsRoute(keyId, period)}>{t("keyPage.errors.showRequests")}</Link>
            </Button>
          ) : (
            <Button type="button" size="sm" variant="ghost" className="h-6 px-2 text-[11px]" onClick={onShowErrors}>
              {t("keyPage.errors.showRequests")}
            </Button>
          )
        ) : null
      }
    >
      <div className="grid gap-4 p-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        {hasErrors ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <TableHeadRow>
                  <Th>{t("keyPage.columns.code")}</Th>
                  <Th align="right">{t("keyPage.columns.count")}</Th>
                  <Th align="right">{t("keyPage.columns.share")}</Th>
                  <Th align="right">{t("keyPage.columns.lastSeen")}</Th>
                </TableHeadRow>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.code || "__none__"} className="border-b last:border-0" data-testid="key-error-row">
                    <td className="px-3 py-1.5 font-mono text-xs">
                      {row.code ? row.code : <span className="font-sans text-muted-foreground">{t("keyPage.errors.noCode")}</span>}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{formatNumber(row.count)}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{sharePercent(row.share)}</td>
                    <td className="px-3 py-1.5 text-right text-xs text-muted-foreground" title={dateTimeLabel(row.lastSeenAt)}>
                      {agoLabel(row.lastSeenAt) ?? "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t("keyPage.errors.none")}</p>
        )}
        <DailyErrorRate daily={daily} />
      </div>
    </SectionCard>
  );
}
