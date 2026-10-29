import { errorRate, sumMeasures } from "./report-format";
import type { KeyReportRow, KeyUsageReport, UsageMeasures } from "./schemas";

export type PerKeySortColumn =
  | "name"
  | "requests"
  | "inputTokens"
  | "outputTokens"
  | "cachedInputTokens"
  | "cost"
  | "share"
  | "errors"
  | "errorRate"
  | "lastUsed"
  | "maxLimit";

export type PerKeySort = { column: PerKeySortColumn; direction: "asc" | "desc" };

export const DEFAULT_PER_KEY_SORT: PerKeySort = { column: "cost", direction: "desc" };

export type Sortable = {
  name: string;
  measures: UsageMeasures;
  share: number;
  lastUsedAt: string | null;
  maxLimitPercent: number | null;
};

export type KeySection = {
  /** Stable section id; "ungrouped" for the keys without a group. */
  id: string;
  groupId: string | null;
  name: string;
  keyCount: number;
  activeKeys: number;
  subtotal: Sortable;
  rows: KeyReportRow[];
};

function sortValue(item: Sortable, column: PerKeySortColumn): number | string | null {
  switch (column) {
    case "name":
      return item.name.toLowerCase();
    case "requests":
      return item.measures.requests;
    case "inputTokens":
      return item.measures.inputTokens;
    case "outputTokens":
      return item.measures.outputTokens;
    case "cachedInputTokens":
      return item.measures.cachedInputTokens;
    case "cost":
      return item.measures.costUsd;
    case "share":
      return item.share;
    case "errors":
      return item.measures.errors;
    case "errorRate":
      return errorRate(item.measures);
    case "lastUsed":
      return item.lastUsedAt ? Date.parse(item.lastUsedAt) : null;
    case "maxLimit":
      return item.maxLimitPercent;
  }
}

/** Orders by the sort column with unknown values last in either direction, then by name. */
export function compareSortable(a: Sortable, b: Sortable, sort: PerKeySort): number {
  const left = sortValue(a, sort.column);
  const right = sortValue(b, sort.column);
  if (left === null || right === null) {
    if (left !== right) {
      return left === null ? 1 : -1;
    }
  } else if (left !== right) {
    const order = typeof left === "string" && typeof right === "string" ? left.localeCompare(right) : left < right ? -1 : 1;
    return sort.direction === "asc" ? order : -order;
  }
  return a.name.localeCompare(b.name);
}

function maxOf(values: (number | null)[]): number | null {
  let best: number | null = null;
  for (const value of values) {
    if (value !== null && (best === null || value > best)) {
      best = value;
    }
  }
  return best;
}

/**
 * Groups the report's keys by their group with one subtotal per group,
 * keeps the keys matching `filter` (key name, prefix or group name), hides
 * idle keys unless `showIdle`, and sorts keys and groups by `sort`.
 */
export function buildKeySections(
  report: KeyUsageReport,
  options: { filter: string; showIdle: boolean; sort: PerKeySort; ungroupedLabel: string },
): { sections: KeySection[]; idleCount: number; matchCount: number } {
  const needle = options.filter.trim().toLowerCase();
  const groupRows = new Map(report.groups.map((group) => [group.id ?? "ungrouped", group]));
  const buckets = new Map<string, { groupId: string | null; name: string; all: KeyReportRow[]; rows: KeyReportRow[] }>();
  let idleCount = 0;
  let matchCount = 0;
  for (const key of report.keys) {
    const id = key.groupId ?? "ungrouped";
    const groupName = key.groupId === null ? options.ungroupedLabel : (key.groupName ?? groupRows.get(id)?.name ?? key.groupId);
    let bucket = buckets.get(id);
    if (!bucket) {
      bucket = { groupId: key.groupId, name: groupName, all: [], rows: [] };
      buckets.set(id, bucket);
    }
    bucket.all.push(key);
    const matches =
      !needle ||
      key.name.toLowerCase().includes(needle) ||
      key.prefix.toLowerCase().includes(needle) ||
      groupName.toLowerCase().includes(needle);
    if (!matches) {
      continue;
    }
    matchCount += 1;
    if (key.measures.requests === 0) {
      idleCount += 1;
      if (!options.showIdle) {
        continue;
      }
    }
    bucket.rows.push(key);
  }
  const sections: KeySection[] = [];
  for (const [id, bucket] of buckets) {
    if (bucket.rows.length === 0) {
      continue;
    }
    const group = groupRows.get(id);
    const measures = group?.measures ?? sumMeasures(bucket.all.map((key) => key.measures));
    sections.push({
      id,
      groupId: bucket.groupId,
      name: bucket.name,
      keyCount: group?.keyCount ?? bucket.all.length,
      activeKeys: group?.activeKeys ?? bucket.all.filter((key) => key.measures.requests > 0).length,
      subtotal: {
        name: bucket.name,
        measures,
        share: group?.share ?? bucket.all.reduce((sum, key) => sum + key.share, 0),
        lastUsedAt:
          bucket.all.reduce<string | null>(
            (latest, key) => (key.lastUsedAt && (!latest || Date.parse(key.lastUsedAt) > Date.parse(latest)) ? key.lastUsedAt : latest),
            null,
          ),
        maxLimitPercent: maxOf(bucket.all.map((key) => key.maxLimitPercent)),
      },
      rows: [...bucket.rows].sort((a, b) => compareSortable(a, b, options.sort)),
    });
  }
  sections.sort((a, b) => {
    if (options.sort.column === "name" && (a.groupId === null) !== (b.groupId === null)) {
      return a.groupId === null ? 1 : -1;
    }
    return compareSortable(a.subtotal, b.subtotal, options.sort);
  });
  return { sections, idleCount, matchCount };
}
