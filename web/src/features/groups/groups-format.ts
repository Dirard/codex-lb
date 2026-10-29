import type { TFunction } from "i18next";

import type { GroupSummaryRow, LimitTemplate } from "@/features/groups/schemas";
import { formatCompactNumber, formatCurrency } from "@/utils/formatters";

/** "50 USD / week" or "5M tokens / day (gpt-5)" for one limit template. */
export function limitTemplateLabel(rule: Pick<LimitTemplate, "limitType" | "limitWindow" | "maxValue" | "modelFilter">, t: TFunction): string {
  const value =
    rule.limitType === "cost_usd"
      ? formatCurrency(rule.maxValue / 1_000_000)
      : `${formatCompactNumber(rule.maxValue)} ${t(`groups.limits.units.${rule.limitType}`)}`;
  const label = t("groups.limits.rule", { value, window: t(`groups.limits.windows.${rule.limitWindow}`) });
  return rule.modelFilter ? `${label} (${rule.modelFilter})` : label;
}

/** All templates of a row joined with a middle dot, or "No limits". */
export function limitsSummary(limits: LimitTemplate[], t: TFunction): string {
  if (limits.length === 0) {
    return t("groups.limits.none");
  }
  return limits.map((rule) => limitTemplateLabel(rule, t)).join(" · ");
}

/** The earlier exhaustion of the two windows, only when it falls before the reset. */
export function exhaustionOf(row: Pick<GroupSummaryRow, "primary" | "secondary">): string | null {
  const times = [row.primary, row.secondary]
    .filter((window) => window.runsOutBeforeReset && window.projectedExhaustionAt)
    .map((window) => window.projectedExhaustionAt as string)
    .sort();
  return times[0] ?? null;
}

/** Number of accounts a row covers, from its health counts. */
export function accountCount(row: Pick<GroupSummaryRow, "accounts">): number {
  const counts = row.accounts;
  return counts.ok + counts.low + counts.limited + counts.reauth + counts.paused + counts.off;
}
