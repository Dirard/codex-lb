import { describe, expect, it } from "vitest";

import { KeyUsageReportSchema } from "./schemas";

const MEASURES = { requests: 3, inputTokens: 30, outputTokens: 6, cachedInputTokens: 10, reasoningTokens: null, costUsd: 0.25, errors: 1, cancelled: 0 };

function payload() {
  return {
    generatedAt: "2026-10-29T10:00:00Z",
    period: { start: "2026-10-23", end: "2026-10-29", timezone: "Europe/Prague", previousStart: "2026-10-16", previousEnd: "2026-10-22", days: 7 },
    selection: { groupIds: ["grp_1"], keyIds: [], ungrouped: true, allKeys: false, resolvedKeyCount: 2 },
    summary: MEASURES,
    previous: null,
    activeKeys: 1,
    seriesBy: "group",
    series: [
      { id: "grp_1", name: "Team", kind: "group" },
      { id: null, name: "", kind: "ungrouped" },
    ],
    groups: [
      { id: "grp_1", name: "Team", keyCount: 1, activeKeys: 1, measures: MEASURES, share: 1 },
      { id: null, name: "", keyCount: 1, activeKeys: 0, measures: { ...MEASURES, requests: 0, costUsd: 0 }, share: 0 },
    ],
    keys: [
      { id: "key_1", name: "One", prefix: "sk-1", groupId: "grp_1", groupName: "Team", status: "active", measures: MEASURES, share: 1, lastUsedAt: "2026-10-29T09:00:00Z", maxLimitPercent: 92.5 },
      { id: "key_2", name: "Two", prefix: "sk-2", groupId: null, groupName: null, status: "expired", measures: { ...MEASURES, requests: 0 }, share: 0, lastUsedAt: null, maxLimitPercent: null },
    ],
    daily: [{ date: "2026-10-29", total: MEASURES, series: [{ requests: 3, tokens: 36, costUsd: 0.25 }, { requests: 0, tokens: 0, costUsd: 0 }] }],
  };
}

describe("KeyUsageReportSchema", () => {
  it("parses the report of a selection with nullable ungrouped ids, previous period and reasoning tokens", () => {
    const report = KeyUsageReportSchema.parse(payload());
    expect(report.groups[1].id).toBeNull();
    expect(report.keys[0].maxLimitPercent).toBe(92.5);
    expect(report.summary.reasoningTokens).toBeNull();
    expect(KeyUsageReportSchema.parse({ ...payload(), previous: MEASURES }).previous?.requests).toBe(3);
  });

  it.each([
    ["an unknown series kind", (p: ReturnType<typeof payload>) => ({ ...p, series: [{ id: "x", name: "x", kind: "account" }] })],
    ["a malformed date", (p: ReturnType<typeof payload>) => ({ ...p, period: { ...p.period, start: "2026-10-2" } })],
    ["a missing selection", (p: ReturnType<typeof payload>) => ({ ...p, selection: undefined })],
    ["an unknown key status", (p: ReturnType<typeof payload>) => ({ ...p, keys: [{ ...p.keys[0], status: "deleted" }] })],
  ])("rejects %s", (_, mutate) => {
    expect(KeyUsageReportSchema.safeParse(mutate(payload())).success).toBe(false);
  });
});
