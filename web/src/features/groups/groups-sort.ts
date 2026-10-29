import { accountCount, exhaustionOf } from "@/features/groups/groups-format";
import type { GroupSummaryRow } from "@/features/groups/schemas";

export type GroupsSortKey =
  | "name"
  | "accounts"
  | "limits"
  | "keys"
  | "limitFill"
  | "spendToday"
  | "spend7d"
  | "spend30d"
  | "primary"
  | "secondary"
  | "exhaustion";

export type GroupsSort = { key: GroupsSortKey; direction: "asc" | "desc" } | null;

/** The value a column sorts by; unknown values sort last in either direction. */
function sortValue(row: GroupSummaryRow, key: GroupsSortKey): number | string | null {
  switch (key) {
    case "name":
      return row.name.toLowerCase();
    case "accounts":
      return row.id === null ? accountCount(row) : row.accountIds.length;
    case "limits":
      return row.limits.length;
    case "keys":
      return row.keys.active;
    case "limitFill":
      return row.limitFill.maxPercent;
    case "spendToday":
      return row.spendTodayUsd;
    case "spend7d":
      return row.spend7dUsd;
    case "spend30d":
      return row.spend30dUsd;
    case "primary":
      return row.primary.remainingPercent;
    case "secondary":
      return row.secondary.remainingPercent;
    case "exhaustion": {
      const at = exhaustionOf(row);
      return at ? Date.parse(at) : null;
    }
  }
}

/** Sorts a copy of the rows; without a sort the server's attention order stays. */
export function sortGroupRows(rows: GroupSummaryRow[], sort: GroupsSort): GroupSummaryRow[] {
  if (!sort) {
    return rows;
  }
  const sign = sort.direction === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    const va = sortValue(a, sort.key);
    const vb = sortValue(b, sort.key);
    if (va === null || vb === null) {
      return va === vb ? 0 : va === null ? 1 : -1;
    }
    if (va < vb) return -sign;
    if (va > vb) return sign;
    return 0;
  });
}

/** Text columns start ascending, figures descending; a third click restores the default order. */
export function nextSort(current: GroupsSort, key: GroupsSortKey): GroupsSort {
  const first = key === "name" || key === "exhaustion" ? "asc" : "desc";
  if (!current || current.key !== key) {
    return { key, direction: first };
  }
  if (current.direction === first) {
    return { key, direction: first === "asc" ? "desc" : "asc" };
  }
  return null;
}
