import { ArrowDown, ArrowUp, ChevronsUpDown, KeyRound } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { HealthBar } from "@/features/overview/components/health-bar";
import { clockLabel, percentLabel, untilLabel, usdLabel } from "@/features/overview/overview-format";
import type { WindowHealth } from "@/features/overview/schemas";
import { accountCount, exhaustionOf, limitsSummary } from "@/features/groups/groups-format";
import { nextSort, sortGroupRows, type GroupsSort, type GroupsSortKey } from "@/features/groups/groups-sort";
import type { GroupSummaryRow } from "@/features/groups/schemas";
import { cn } from "@/lib/utils";

function WindowCell({ health }: { health: WindowHealth }) {
  const reset = untilLabel(health.nextResetAt);
  return (
    <div className="tabular-nums">
      <span className="font-medium">{percentLabel(health.remainingPercent)}</span>
      {reset ? <span className="block text-[11px] text-muted-foreground">{reset}</span> : null}
    </div>
  );
}

const COLUMNS: { key: GroupsSortKey; labelKey: string; align?: "right" }[] = [
  { key: "name", labelKey: "groups.columns.name" },
  { key: "accounts", labelKey: "groups.columns.accounts" },
  { key: "limits", labelKey: "groups.columns.limits" },
  { key: "keys", labelKey: "groups.columns.keys" },
  { key: "limitFill", labelKey: "groups.columns.limitFill" },
  { key: "spendToday", labelKey: "groups.columns.spendToday", align: "right" },
  { key: "spend7d", labelKey: "groups.columns.spend7d", align: "right" },
  { key: "spend30d", labelKey: "groups.columns.spend30d", align: "right" },
  { key: "primary", labelKey: "groups.columns.primary" },
  { key: "secondary", labelKey: "groups.columns.secondary" },
  { key: "exhaustion", labelKey: "groups.columns.exhaustion" },
];

/**
 * Every group and the ungrouped row; every column sorts, and the default is
 * the summary's attention order (use case 7, R40). A group row opens its
 * sheet; the ungrouped row links to its keys.
 */
export function GroupsTable({ rows, onOpen }: { rows: GroupSummaryRow[]; onOpen: (groupId: string) => void }) {
  const { t } = useTranslation();
  const [sort, setSort] = useState<GroupsSort>(null);
  const sorted = useMemo(() => sortGroupRows(rows, sort), [rows, sort]);

  return (
    <section aria-labelledby="groups-table-title" className="rounded-xl border bg-card">
      <div className="flex items-center justify-between border-b px-4 py-2">
        <h2 id="groups-table-title" className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
          {t("groups.table.title")}
        </h2>
        {sort ? (
          <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => setSort(null)}>
            {t("groups.table.defaultOrder")}
          </button>
        ) : null}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-[11px] uppercase tracking-wider text-muted-foreground">
              {COLUMNS.map((column) => {
                const active = sort?.key === column.key;
                const Icon = !active ? ChevronsUpDown : sort.direction === "asc" ? ArrowUp : ArrowDown;
                return (
                  <th
                    key={column.key}
                    className={cn("px-2 py-2 font-medium", column.align === "right" && "text-right")}
                    aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}
                  >
                    <button
                      type="button"
                      className={cn("inline-flex items-center gap-1 uppercase hover:text-foreground", active && "text-foreground")}
                      onClick={() => setSort((current) => nextSort(current, column.key))}
                    >
                      {t(column.labelKey)}
                      <Icon className="h-3 w-3" aria-hidden="true" />
                    </button>
                  </th>
                );
              })}
              <th className="px-2 py-2" />
            </tr>
          </thead>
          <tbody>
            {sorted.map((row) => {
              const exhaustion = exhaustionOf(row);
              const grouped = row.id !== null;
              const name = grouped ? row.name : t("groups.table.ungrouped");
              const keysLink = `/keys?group=${grouped ? encodeURIComponent(row.id as string) : "ungrouped"}`;
              return (
                <tr
                  key={row.id ?? "__ungrouped__"}
                  data-testid="groups-row"
                  className={cn("border-b align-top", grouped && "cursor-pointer hover:bg-muted/30")}
                  onClick={grouped ? () => onOpen(row.id as string) : undefined}
                >
                  <td className="px-2 py-2">
                    <div className="flex items-center gap-2">
                      {grouped ? (
                        <button
                          type="button"
                          className="font-medium hover:underline"
                          onClick={(event) => {
                            event.stopPropagation();
                            onOpen(row.id as string);
                          }}
                        >
                          {name}
                        </button>
                      ) : (
                        <span className="font-medium">{name}</span>
                      )}
                      {row.attention !== "none" ? (
                        <Badge variant={row.attention === "critical" ? "destructive" : "secondary"} className="text-[10px]">
                          {t(`overview.severity.${row.attention}`)}
                        </Badge>
                      ) : null}
                    </div>
                    {!grouped ? <span className="text-[11px] text-muted-foreground">{t("groups.table.wholePool")}</span> : null}
                  </td>
                  <td className="px-2 py-2">
                    <span className="text-xs tabular-nums">{grouped ? row.accountIds.length : accountCount(row)}</span>
                    <HealthBar counts={row.accounts} />
                  </td>
                  <td className="max-w-[16rem] px-2 py-2 text-xs text-muted-foreground">
                    {grouped ? limitsSummary(row.limits, t) : t("groups.limits.perKey")}
                  </td>
                  <td className="px-2 py-2 tabular-nums">
                    {row.keys.active}/{row.keys.total}
                  </td>
                  <td className="px-2 py-2 tabular-nums">
                    <span className="font-medium">{percentLabel(row.limitFill.maxPercent)}</span>
                    {row.limitFill.keysAtOrAbove90 > 0 ? (
                      <span className="block text-[11px] text-amber-600">
                        {t("overview.groups.keysNearLimit", { count: row.limitFill.keysAtOrAbove90 })}
                      </span>
                    ) : null}
                  </td>
                  <td className="px-2 py-2 text-right font-medium tabular-nums">{usdLabel(row.spendTodayUsd)}</td>
                  <td className="px-2 py-2 text-right tabular-nums">{usdLabel(row.spend7dUsd)}</td>
                  <td className="px-2 py-2 text-right tabular-nums">{usdLabel(row.spend30dUsd)}</td>
                  <td className="px-2 py-2">
                    <WindowCell health={row.primary} />
                  </td>
                  <td className="px-2 py-2">
                    <WindowCell health={row.secondary} />
                  </td>
                  <td className="px-2 py-2 text-xs">
                    {exhaustion ? <span className="font-medium text-red-500">{clockLabel(exhaustion)}</span> : <span className="text-muted-foreground">—</span>}
                  </td>
                  <td className="px-2 py-2">
                    <Link
                      to={keysLink}
                      className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
                      aria-label={t("groups.table.openKeys", { name })}
                      title={t("groups.table.openKeys", { name })}
                      onClick={(event) => event.stopPropagation()}
                    >
                      <KeyRound className="h-3.5 w-3.5" />
                    </Link>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
