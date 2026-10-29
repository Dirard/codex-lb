import type { AccountGroup } from "@/features/account-groups/schemas";
import type { ApiKey } from "@/features/api-keys/schemas";
import {
  KeyUsageReportSchema,
  ReportsResponseSchema,
  type GroupReportRow,
  type KeyReportRow,
  type KeyReportSeries,
  type KeyUsageReport,
  type ReportsResponse,
  type UsageMeasures,
} from "@/features/reports/schemas";

export const KEY_USAGE_CSV_HEADER =
  "date,group_id,group_name,key_id,key_name,requests,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_usd,errors,cancelled";

const SERIES_LIMIT = 8;

export function createUsageMeasures(overrides: Partial<UsageMeasures> = {}): UsageMeasures {
  return {
    requests: 0,
    inputTokens: 0,
    outputTokens: 0,
    cachedInputTokens: 0,
    reasoningTokens: 0,
    costUsd: 0,
    errors: 0,
    cancelled: 0,
    ...overrides,
  };
}

export function createKeyReportRow(overrides: Partial<KeyReportRow> = {}): KeyReportRow {
  return {
    id: "key_1",
    name: "Default key",
    prefix: "sk-test",
    groupId: null,
    groupName: null,
    status: "active",
    measures: createUsageMeasures({ requests: 150, inputTokens: 40_000, outputTokens: 10_000, cachedInputTokens: 10_000, costUsd: 1.23, errors: 3 }),
    share: 1,
    lastUsedAt: "2026-10-28T10:00:00Z",
    maxLimitPercent: 12.5,
    ...overrides,
  };
}

function sum(rows: UsageMeasures[]): UsageMeasures {
  const total = createUsageMeasures();
  for (const row of rows) {
    total.requests += row.requests;
    total.inputTokens += row.inputTokens;
    total.outputTokens += row.outputTokens;
    total.cachedInputTokens += row.cachedInputTokens;
    total.reasoningTokens = (total.reasoningTokens ?? 0) + (row.reasoningTokens ?? 0);
    total.costUsd += row.costUsd;
    total.errors += row.errors;
    total.cancelled += row.cancelled;
  }
  return total;
}

function eachDate(start: string, end: string): string[] {
  const dates: string[] = [];
  for (let current = start; current <= end && dates.length < 800; ) {
    dates.push(current);
    const next = new Date(`${current}T00:00:00Z`);
    next.setUTCDate(next.getUTCDate() + 1);
    current = next.toISOString().slice(0, 10);
  }
  return dates;
}

function shiftDate(date: string, days: number): string {
  const shifted = new Date(`${date}T00:00:00Z`);
  shifted.setUTCDate(shifted.getUTCDate() + days);
  return shifted.toISOString().slice(0, 10);
}

export type KeyUsageReportInput = {
  start: string;
  end: string;
  timezone?: string;
  selection?: Partial<KeyUsageReport["selection"]>;
  keys: KeyReportRow[];
  groupNames?: Record<string, string>;
  previous?: UsageMeasures | null;
};

/**
 * A consistent report for the given keys: totals, per-group rows, shares,
 * the top-8 series (groups when the keys span several groups, else keys)
 * and one day per date of the period with all usage on the last day.
 */
export function buildKeyUsageReport(input: KeyUsageReportInput): KeyUsageReport {
  const keys = input.keys;
  const summary = sum(keys.map((key) => key.measures));
  const share = (cost: number) => (summary.costUsd > 0 ? cost / summary.costUsd : 0);
  const byGroup = new Map<string, KeyReportRow[]>();
  for (const key of keys) {
    const id = key.groupId ?? "";
    byGroup.set(id, [...(byGroup.get(id) ?? []), key]);
  }
  const groups: GroupReportRow[] = [...byGroup.entries()]
    .map(([id, rows]) => {
      const measures = sum(rows.map((row) => row.measures));
      return {
        id: id || null,
        name: id ? (input.groupNames?.[id] ?? rows[0].groupName ?? id) : "",
        keyCount: rows.length,
        activeKeys: rows.filter((row) => row.measures.requests > 0).length,
        measures,
        share: share(measures.costUsd),
      };
    })
    .sort((a, b) => b.measures.costUsd - a.measures.costUsd);
  const seriesBy: KeyUsageReport["seriesBy"] = groups.length > 1 ? "group" : "key";
  const ranked =
    seriesBy === "group"
      ? groups.filter((group) => group.measures.requests > 0).map((group) => ({ id: group.id, name: group.name, kind: group.id === null ? ("ungrouped" as const) : ("group" as const), measures: group.measures }))
      : keys
          .filter((key) => key.measures.requests > 0)
          .sort((a, b) => b.measures.costUsd - a.measures.costUsd)
          .map((key) => ({ id: key.id as string | null, name: key.name, kind: "key" as const, measures: key.measures }));
  const top = ranked.slice(0, SERIES_LIMIT);
  const rest = ranked.slice(SERIES_LIMIT);
  const series: KeyReportSeries[] = top.map(({ id, name, kind }) => ({ id, name, kind }));
  const seriesMeasures = top.map((entry) => entry.measures);
  if (rest.length > 0) {
    series.push({ id: null, name: "Other", kind: "other" });
    seriesMeasures.push(sum(rest.map((entry) => entry.measures)));
  }
  const dates = eachDate(input.start, input.end);
  const daily = dates.map((date, index) => {
    const last = index === dates.length - 1;
    return {
      date,
      total: last ? summary : createUsageMeasures(),
      series: seriesMeasures.map((measures) => ({
        requests: last ? measures.requests : 0,
        tokens: last ? measures.inputTokens + measures.outputTokens : 0,
        costUsd: last ? measures.costUsd : 0,
      })),
    };
  });
  const days = dates.length;
  return KeyUsageReportSchema.parse({
    generatedAt: new Date().toISOString(),
    period: {
      start: input.start,
      end: input.end,
      timezone: input.timezone ?? "UTC",
      previousStart: shiftDate(input.start, -days),
      previousEnd: shiftDate(input.start, -1),
      days,
    },
    selection: {
      groupIds: [],
      keyIds: [],
      ungrouped: false,
      allKeys: true,
      resolvedKeyCount: keys.length,
      ...input.selection,
    },
    summary,
    previous: input.previous === undefined ? null : input.previous,
    activeKeys: keys.filter((key) => key.measures.requests > 0).length,
    seriesBy,
    series,
    groups,
    keys: [...keys]
      .map((key) => ({ ...key, share: share(key.measures.costUsd) }))
      .sort((a, b) => b.measures.costUsd - a.measures.costUsd),
    daily,
  });
}

export function createKeyUsageReport(overrides: Partial<KeyUsageReportInput> = {}): KeyUsageReport {
  return buildKeyUsageReport({
    start: "2026-10-23",
    end: "2026-10-29",
    keys: [createKeyReportRow()],
    ...overrides,
  });
}

/** The key rows of the mock state's keys, measured from their usage summaries. */
export function keyReportRowsFromState(apiKeys: ApiKey[], groups: AccountGroup[]): KeyReportRow[] {
  const names = new Map(groups.map((group) => [group.id, group.name]));
  const now = Date.now();
  return apiKeys.map((key) => {
    const usage = key.usageSummary;
    const requests = usage?.requestCount ?? 0;
    const tokens = usage?.totalTokens ?? 0;
    const groupId = key.groupId ?? null;
    const fill = key.limits.reduce<number | null>((best, limit) => {
      const percent = limit.maxValue > 0 ? (limit.currentValue / limit.maxValue) * 100 : 0;
      return best === null || percent > best ? percent : best;
    }, null);
    return {
      id: key.id,
      name: key.name,
      prefix: key.keyPrefix,
      groupId,
      groupName: groupId ? (names.get(groupId) ?? null) : null,
      status: !key.isActive ? "disabled" : key.expiresAt && new Date(key.expiresAt).getTime() <= now ? "expired" : "active",
      measures: createUsageMeasures({
        requests,
        inputTokens: Math.round(tokens * 0.8),
        outputTokens: tokens - Math.round(tokens * 0.8),
        cachedInputTokens: usage?.cachedInputTokens ?? 0,
        costUsd: usage?.totalCostUsd ?? 0,
      }),
      share: 0,
      lastUsedAt: key.lastUsedAt ?? null,
      maxLimitPercent: fill,
    };
  });
}

/** One CSV row per key with requests, on the report's last day. */
export function keyUsageReportCsv(report: KeyUsageReport): string {
  const lines = [KEY_USAGE_CSV_HEADER];
  for (const key of report.keys) {
    if (key.measures.requests === 0) {
      continue;
    }
    const m = key.measures;
    lines.push(
      [
        report.period.end,
        key.groupId ?? "",
        key.groupName ?? "",
        key.id,
        key.name,
        m.requests,
        m.inputTokens,
        m.outputTokens,
        m.cachedInputTokens,
        m.reasoningTokens ?? "",
        m.costUsd.toFixed(6),
        m.errors,
        m.cancelled,
      ].join(","),
    );
  }
  return `${lines.join("\n")}\n`;
}

/** An empty GET /api/reports payload for traffic details. */
export function createReportsResponse(overrides: Partial<ReportsResponse> = {}): ReportsResponse {
  return ReportsResponseSchema.parse({
    summary: {
      totalCostUsd: 0,
      totalInputTokens: 0,
      totalOutputTokens: 0,
      totalReasoningTokens: 0,
      reasoningUsageKnownRequests: 0,
      totalCachedTokens: 0,
      totalRequests: 0,
      totalCancelled: 0,
      totalErrors: 0,
      totalConversations: 0,
      activeAccounts: 0,
      avgCostPerDay: 0,
      avgRequestsPerDay: 0,
    },
    comparison: { canCompare: false, previous: { totalCostUsd: 0, totalTokens: 0, totalRequests: 0 } },
    daily: [],
    byModel: [{ model: "gpt-5.1", costUsd: 1.23, requests: 150, percentage: 100 }],
    byUseragent: [{ useragent: "codex", costUsd: 1.23, requests: 150, percentage: 100 }],
    byAccount: [],
    ...overrides,
  });
}
