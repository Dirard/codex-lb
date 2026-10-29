import { useTranslation } from "react-i18next";

import { countLabel, sharePercent, usd } from "@/features/key-page/key-page-format";
import { SectionCard, TableHeadRow, Th } from "@/features/key-page/components/section-card";
import type { KeyAccountCostRow, UsageTotals } from "@/features/key-page/schemas";
import type { Directory } from "@/features/search/api";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { cn } from "@/lib/utils";
import { formatNumber } from "@/utils/formatters";

/** An account's label from the response, else the directory, else its id. */
export function AccountLabel({
  accountId,
  label,
  deleted = false,
  directory,
}: {
  accountId: string | null | undefined;
  label?: string | null;
  deleted?: boolean;
  directory: Directory | undefined;
}) {
  const { t } = useTranslation();
  const blurred = usePrivacyStore((state) => state.blurred);
  if (!accountId) return <span className="text-muted-foreground">{t("keyPage.accounts.none")}</span>;
  const entry = directory?.accounts.find((account) => account.id === accountId);
  const text = label || entry?.label || accountId;
  const sensitive = blurred && text.includes("@");
  return (
    <span className={cn("truncate", sensitive && "privacy-blur")} title={sensitive ? undefined : text}>
      {text}
      {deleted ? <span className="ml-1 text-[11px] text-muted-foreground">({t("keyPage.accounts.deleted")})</span> : null}
    </span>
  );
}

/** Administrator: the period's cost by account (R24 section 11). */
export function KeyCostByAccountSection({ rows, directory }: { rows: KeyAccountCostRow[]; directory: Directory | undefined }) {
  const { t } = useTranslation();
  return (
    <SectionCard id="key-page-accounts" title={t("keyPage.accounts.title")}>
      {rows.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">{t("keyPage.accounts.empty")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <TableHeadRow>
                <Th>{t("keyPage.columns.account")}</Th>
                <Th align="right">{t("keyPage.columns.requests")}</Th>
                <Th align="right">{t("keyPage.columns.cost")}</Th>
                <Th align="right">{t("keyPage.columns.share")}</Th>
              </TableHeadRow>
            </thead>
            <tbody>
              {rows.map((row, index) => (
                <tr key={row.accountId ?? `none-${index}`} className="border-b last:border-0" data-testid="key-account-row">
                  <td className="max-w-72 px-3 py-1.5 text-xs">
                    <AccountLabel accountId={row.accountId} label={row.label} deleted={row.deleted} directory={directory} />
                  </td>
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

/** Administrator: the key's lifetime totals, labelled "Lifetime" (`usage_totals`). */
export function KeyLifetimeSection({ lifetime }: { lifetime: UsageTotals }) {
  const { t } = useTranslation();
  const usage = lifetime.usage;
  const facts: [string, string][] = [
    [t("keyPage.kpis.requests"), formatNumber(lifetime.requestCount)],
    [t("keyPage.lifetime.failed"), formatNumber(lifetime.failedCount)],
    [t("common.units.input"), countLabel(usage.inputTokens)],
    [t("common.units.output"), countLabel(usage.outputTokens)],
    [t("common.units.cached"), countLabel(usage.cachedInputTokens)],
    [t("keyPage.kpis.reasoning"), countLabel(usage.reasoningTokens)],
    [t("keyPage.kpis.cost"), usd(usage.costMicrodollars / 1_000_000)],
  ];
  return (
    <SectionCard id="key-page-lifetime" title={t("keyPage.lifetime.title")}>
      <dl className="grid grid-cols-2 gap-4 p-4 sm:grid-cols-4 xl:grid-cols-7">
        {facts.map(([label, value]) => (
          <div key={label} className="min-w-0 space-y-0.5">
            <dt className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</dt>
            <dd className="text-base font-semibold tabular-nums">{value}</dd>
          </div>
        ))}
      </dl>
    </SectionCard>
  );
}
