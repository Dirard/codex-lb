import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Skeleton } from "@/components/ui/skeleton";
import { LimitUsageBar } from "@/features/api-keys/components/api-key-limit-usage";
import { formatKeyLimitValue } from "@/features/api-keys/components/limit-rules-utils";
import { KeyRowActions } from "@/features/keys/components/key-row-actions";
import type { KeyOrder } from "@/features/keys/api";
import type { KeyRow, KeySort } from "@/features/keys/schemas";
import { agoLabel, untilLabel, usdLabel } from "@/features/overview/overview-format";
import { cn } from "@/lib/utils";
import { formatTimeLong } from "@/utils/formatters";

const LIMITS_SHOWN = 2;

function StatusBadge({ status }: { status: KeyRow["status"] }) {
  const { t } = useTranslation();
  const tone = status === "active" ? "bg-emerald-500" : status === "expired" ? "bg-amber-500" : "bg-zinc-500";
  return <Badge className={cn("text-[10px] text-white", tone)}>{t(`common.states.${status}`)}</Badge>;
}

function dateLabel(iso: string | null): string {
  return iso ? formatTimeLong(iso).date : "—";
}

/** The fullest limit: percent, bar, window and the time until its reset. */
function FullestLimit({ row }: { row: KeyRow }) {
  const { t } = useTranslation();
  const limit = row.maxLimit;
  if (!limit) return <span className="text-muted-foreground">—</span>;
  const until = untilLabel(limit.resetAt);
  return (
    <div className="min-w-[7.5rem] space-y-1">
      <div className="flex items-baseline justify-between gap-2">
        <span className={cn("font-medium tabular-nums", limit.percent >= 90 && "text-red-600 dark:text-red-400")}>
          {Math.round(limit.percent)}%
        </span>
        <span className="text-[11px] text-muted-foreground">
          {t(`keyReports.limitWindows.${limit.window}`)}
          {until ? ` · ${t("overview.window.resetsIn", { until })}` : ""}
        </span>
      </div>
      <LimitUsageBar percent={limit.percent} label={t("keys.table.fullestLimit")} />
    </div>
  );
}

function LimitsSummary({ row }: { row: KeyRow }) {
  const { t } = useTranslation();
  if (row.limitsSummary.length === 0) return <span className="text-muted-foreground">—</span>;
  const shown = row.limitsSummary.slice(0, LIMITS_SHOWN);
  const rest = row.limitsSummary.length - shown.length;
  return (
    <div className="space-y-0.5 text-[11px] leading-tight">
      {shown.map((limit) => (
        <div key={`${limit.type}:${limit.window}:${limit.modelFilter ?? ""}`} className="whitespace-nowrap">
          <span className="font-medium">{formatKeyLimitValue(limit.maxValue, limit.type)}</span>{" "}
          <span className="text-muted-foreground">
            {t(`apiKeys.limitTypes.${limit.type}`)} · {t(`keyReports.limitWindows.${limit.window}`)}
            {limit.modelFilter ? ` · ${limit.modelFilter}` : ""}
          </span>
        </div>
      ))}
      {rest > 0 ? <div className="text-muted-foreground">{t("keys.table.moreLimits", { count: rest })}</div> : null}
    </div>
  );
}

type Column = { sort?: KeySort; label: string; align?: "right" };

function SortHeader({
  column,
  sort,
  order,
  onSort,
}: {
  column: Column;
  sort: KeySort;
  order: KeyOrder;
  onSort: (sort: KeySort) => void;
}) {
  const { t } = useTranslation();
  const active = column.sort === sort;
  const Icon = !active ? ArrowUpDown : order === "asc" ? ArrowUp : ArrowDown;
  return (
    <th
      scope="col"
      aria-sort={active ? (order === "asc" ? "ascending" : "descending") : undefined}
      className={cn("sticky top-0 z-10 bg-card px-3 py-2 font-medium", column.align === "right" && "text-right")}
    >
      {column.sort ? (
        <button
          type="button"
          className={cn("inline-flex items-center gap-1 uppercase hover:text-foreground", active && "text-foreground")}
          onClick={() => onSort(column.sort as KeySort)}
          aria-label={t("keys.table.sortBy", { column: column.label })}
        >
          {column.label}
          <Icon className="h-3 w-3" aria-hidden="true" />
        </button>
      ) : (
        column.label
      )}
    </th>
  );
}

export type KeysTableProps = {
  rows: KeyRow[];
  loading: boolean;
  sort: KeySort;
  order: KeyOrder;
  selected: ReadonlySet<string>;
  onSelect: (ids: string[], selected: boolean) => void;
  onSort: (sort: KeySort) => void;
};

/**
 * One page of keys (at most 50 rows) with a sticky header, a checkbox per
 * row and for the page, sortable spend, limit, last-use and creation
 * columns, a row click that opens the key page and a row menu (R35).
 */
export function KeysTable({ rows, loading, sort, order, selected, onSelect, onSort }: KeysTableProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const pageIds = rows.map((row) => row.id);
  const selectedOnPage = pageIds.filter((id) => selected.has(id)).length;
  const pageState = selectedOnPage === 0 ? false : selectedOnPage === pageIds.length ? true : "indeterminate";
  const columns: Column[] = [
    { sort: "name", label: t("keys.table.key") },
    { label: t("keys.table.group") },
    { label: t("keys.table.status") },
    { sort: "spend_today", label: t("keys.table.spendToday"), align: "right" },
    { sort: "spend_7d", label: t("keys.table.spend7d"), align: "right" },
    { sort: "spend_30d", label: t("keys.table.spend30d"), align: "right" },
    { sort: "limit_fill", label: t("keys.table.fullestLimit") },
    { label: t("keys.table.limits") },
    { label: t("keys.table.expiry") },
    { sort: "last_used", label: t("keys.table.lastUsed") },
    { sort: "created", label: t("keys.table.created") },
  ];
  return (
    <div className="max-h-[calc(100dvh-14rem)] overflow-auto rounded-xl border bg-card" data-testid="keys-table">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b text-left text-[11px] uppercase tracking-wider text-muted-foreground">
            <th scope="col" className="sticky top-0 z-10 w-8 bg-card px-3 py-2">
              <Checkbox
                checked={pageState}
                disabled={rows.length === 0}
                onCheckedChange={(value) => onSelect(pageIds, value === true)}
                aria-label={t("keys.table.selectPage")}
              />
            </th>
            {columns.map((column) => (
              <SortHeader key={column.label} column={column} sort={sort} order={order} onSort={onSort} />
            ))}
            <th scope="col" className="sticky top-0 z-10 bg-card px-3 py-2">
              <span className="sr-only">{t("keys.table.actions")}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {loading && rows.length === 0
            ? Array.from({ length: 8 }, (_, index) => (
                <tr key={index} className="border-b last:border-0">
                  <td colSpan={columns.length + 2} className="px-3 py-2">
                    <Skeleton className="h-8 w-full" />
                  </td>
                </tr>
              ))
            : rows.map((row) => {
                const checked = selected.has(row.id);
                const open = () => navigate(`/keys/${encodeURIComponent(row.id)}`);
                return (
                  <tr
                    key={row.id}
                    data-testid="keys-row"
                    data-state={checked ? "selected" : undefined}
                    className="cursor-pointer border-b last:border-0 hover:bg-muted/50 data-[state=selected]:bg-muted/60"
                    onClick={open}
                  >
                    <td className="px-3 py-2" onClick={(event) => event.stopPropagation()}>
                      <Checkbox
                        checked={checked}
                        onCheckedChange={(value) => onSelect([row.id], value === true)}
                        aria-label={t("keys.table.selectKey", { name: row.name })}
                      />
                    </td>
                    <td className="max-w-[16rem] px-3 py-2">
                      <a
                        href={`/keys/${encodeURIComponent(row.id)}`}
                        className="block truncate font-medium hover:underline"
                        onClick={(event) => {
                          event.preventDefault();
                          event.stopPropagation();
                          open();
                        }}
                      >
                        {row.name}
                      </a>
                      <span className="block font-mono text-[11px] text-muted-foreground">{row.prefix}</span>
                    </td>
                    <td className="max-w-[10rem] truncate px-3 py-2 text-xs">
                      {row.groupName ?? (row.groupId ? row.groupId : <span className="text-muted-foreground">{t("keys.filters.ungrouped")}</span>)}
                    </td>
                    <td className="px-3 py-2">
                      <StatusBadge status={row.status} />
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{usdLabel(row.spendTodayUsd)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{usdLabel(row.spend7dUsd)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{usdLabel(row.spend30dUsd)}</td>
                    <td className="px-3 py-2 text-xs">
                      <FullestLimit row={row} />
                    </td>
                    <td className="px-3 py-2">
                      <LimitsSummary row={row} />
                    </td>
                    <td className="whitespace-nowrap px-3 py-2 text-xs">
                      {row.expiresAt ? (
                        row.status === "expired" || !untilLabel(row.expiresAt) ? (
                          <span className="text-amber-600 dark:text-amber-400">{t("common.states.expired")}</span>
                        ) : (
                          <span title={dateLabel(row.expiresAt)}>{t("keys.table.expiresIn", { until: untilLabel(row.expiresAt) })}</span>
                        )
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="whitespace-nowrap px-3 py-2 text-xs">
                      {agoLabel(row.lastUsedAt) ?? <span className="text-muted-foreground">{t("keys.table.never")}</span>}
                    </td>
                    <td className="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">{dateLabel(row.createdAt)}</td>
                    <td className="px-3 py-2">
                      <KeyRowActions row={row} />
                    </td>
                  </tr>
                );
              })}
        </tbody>
      </table>
    </div>
  );
}
