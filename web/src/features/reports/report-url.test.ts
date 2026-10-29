import { describe, expect, it } from "vitest";

import { addDaysISO, eachISODate, inclusiveDayCount, isISODate } from "./date";
import {
  isScopeEmpty,
  parseReportUrl,
  quickPeriodRange,
  reportRangeError,
  resolveReportRange,
  writeReportUrl,
} from "./report-url";

// 2026-03-15 10:00 local time; quick periods count local calendar days.
const NOW = new Date(2026, 2, 15, 10, 0, 0);

describe("report URL state", () => {
  it("defaults to every key and the last 7 days", () => {
    const state = parseReportUrl(new URLSearchParams());
    expect(state).toEqual({ scope: { groupIds: [], keyIds: [], ungrouped: false }, period: { kind: "quick", period: "7d" } });
    expect(isScopeEmpty(state.scope)).toBe(true);
    expect(writeReportUrl(state).toString()).toBe("");
  });

  it("round-trips groups, keys, the ungrouped keys and a quick period", () => {
    const params = new URLSearchParams("group=grp_1&group=grp_2&key=key_9&ungrouped=1&period=30d&view=x");
    const state = parseReportUrl(params);
    expect(state.scope).toEqual({ groupIds: ["grp_1", "grp_2"], keyIds: ["key_9"], ungrouped: true });
    expect(state.period).toEqual({ kind: "quick", period: "30d" });
    const written = writeReportUrl(state, params);
    expect(written.getAll("group")).toEqual(["grp_1", "grp_2"]);
    expect(written.getAll("key")).toEqual(["key_9"]);
    expect(written.get("ungrouped")).toBe("1");
    expect(written.get("period")).toBe("30d");
    expect(written.get("view")).toBe("x");
    expect(parseReportUrl(written)).toEqual(state);
  });

  it("prefers custom dates over a quick period and drops the period when writing them", () => {
    const state = parseReportUrl(new URLSearchParams("period=90d&from=2026-01-01&to=2026-01-31"));
    expect(state.period).toEqual({ kind: "custom", from: "2026-01-01", to: "2026-01-31" });
    const written = writeReportUrl(state);
    expect(written.toString()).toBe("from=2026-01-01&to=2026-01-31");
  });

  it("ignores malformed values and duplicate ids", () => {
    const state = parseReportUrl(new URLSearchParams("group=a&group=a&group=&key=%20&ungrouped=yes&period=1y&from=2026-02-30&to=2026-03-01"));
    expect(state.scope).toEqual({ groupIds: ["a"], keyIds: [], ungrouped: false });
    expect(state.period).toEqual({ kind: "quick", period: "7d" });
  });

  it("keeps an inverted custom range so the page can explain it", () => {
    const state = parseReportUrl(new URLSearchParams("from=2026-03-10&to=2026-03-01"));
    expect(state.period).toEqual({ kind: "custom", from: "2026-03-10", to: "2026-03-01" });
    expect(reportRangeError(resolveReportRange(state.period, NOW), "2026-03-15")).toBe("inverted");
  });
});

describe("quick periods", () => {
  it.each([
    ["today", "2026-03-15", "2026-03-15"],
    ["yesterday", "2026-03-14", "2026-03-14"],
    ["7d", "2026-03-09", "2026-03-15"],
    ["30d", "2026-02-14", "2026-03-15"],
    ["90d", "2025-12-16", "2026-03-15"],
    ["this_month", "2026-03-01", "2026-03-15"],
    ["last_month", "2026-02-01", "2026-02-28"],
  ] as const)("%s covers %s to %s", (period, start, end) => {
    expect(quickPeriodRange(period, NOW)).toEqual({ start, end });
  });

  it("crosses the year for last month in January", () => {
    expect(quickPeriodRange("last_month", new Date(2026, 0, 5))).toEqual({ start: "2025-12-01", end: "2025-12-31" });
  });
});

describe("custom period validation", () => {
  it("accepts up to 730 days ending today", () => {
    expect(reportRangeError({ start: addDaysISO("2026-03-15", -729), end: "2026-03-15" }, "2026-03-15")).toBeNull();
    expect(reportRangeError({ start: addDaysISO("2026-03-15", -730), end: "2026-03-15" }, "2026-03-15")).toBe("too_long");
  });

  it("rejects a date later than today", () => {
    expect(reportRangeError({ start: "2026-03-10", end: "2026-03-16" }, "2026-03-15")).toBe("future");
  });
});

describe("date helpers", () => {
  it("validates, shifts and counts calendar days", () => {
    expect(isISODate("2024-02-29")).toBe(true);
    expect(isISODate("2026-02-29")).toBe(false);
    expect(isISODate("2026-3-1")).toBe(false);
    expect(addDaysISO("2026-03-01", -1)).toBe("2026-02-28");
    expect(inclusiveDayCount("2026-03-01", "2026-03-01")).toBe(1);
    expect(inclusiveDayCount("2026-03-28", "2026-04-02")).toBe(6);
    expect(eachISODate("2026-02-27", "2026-03-02")).toEqual(["2026-02-27", "2026-02-28", "2026-03-01", "2026-03-02"]);
    expect(eachISODate("2026-03-02", "2026-03-01")).toEqual([]);
  });
});
