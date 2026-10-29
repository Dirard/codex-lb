import { describe, expect, it } from "vitest";

import {
  defaultPeriod,
  includesToday,
  isISODate,
  isValidPeriod,
  keyErrorRequestsRoute,
  periodDays,
  periodFromSearch,
  presetOf,
  presetPeriod,
  requestsTimeframeFor,
  searchNamesPeriod,
  searchWithPeriod,
  shiftISODate,
} from "./period";

// Local noon keeps the calendar day independent of the test machine's zone.
const NOW = new Date(2026, 9, 29, 12, 0, 0);

describe("key page period", () => {
  it("defaults to the 7 days ending today", () => {
    expect(defaultPeriod(NOW)).toEqual({ from: "2026-10-23", to: "2026-10-29" });
    expect(periodFromSearch("", NOW)).toEqual({ from: "2026-10-23", to: "2026-10-29" });
    expect(periodFromSearch("?other=1", NOW)).toEqual({ from: "2026-10-23", to: "2026-10-29" });
  });

  it("names the presets ending today and anything else custom", () => {
    expect(presetPeriod("today", NOW)).toEqual({ from: "2026-10-29", to: "2026-10-29" });
    expect(presetPeriod("30d", NOW)).toEqual({ from: "2026-09-30", to: "2026-10-29" });
    expect(presetPeriod("90d", NOW)).toEqual({ from: "2026-08-01", to: "2026-10-29" });
    expect(presetOf({ from: "2026-10-23", to: "2026-10-29" }, NOW)).toBe("7d");
    expect(presetOf({ from: "2026-10-01", to: "2026-10-28" }, NOW)).toBe("custom");
  });

  it("reads from and to of the URL and falls back on invalid ones", () => {
    expect(periodFromSearch("?from=2026-10-01&to=2026-10-28", NOW)).toEqual({ from: "2026-10-01", to: "2026-10-28" });
    expect(periodFromSearch("?from=2026-10-20", NOW)).toEqual({ from: "2026-10-20", to: "2026-10-29" });
    expect(periodFromSearch("?to=2026-10-10", NOW)).toEqual({ from: "2026-10-04", to: "2026-10-10" });
    expect(periodFromSearch("?from=2026-10-28&to=2026-10-01", NOW)).toEqual(defaultPeriod(NOW));
    // An invalid day counts as absent.
    expect(periodFromSearch("?from=2026-02-30&to=2026-03-01", NOW)).toEqual({ from: "2026-02-23", to: "2026-03-01" });
    expect(periodFromSearch("?from=yesterday&to=2026-13-01", NOW)).toEqual(defaultPeriod(NOW));
    expect(periodFromSearch("?from=2020-01-01&to=2026-10-29", NOW)).toEqual(defaultPeriod(NOW));
  });

  it("validates calendar days and the 730-day bound", () => {
    expect(isISODate("2024-02-29")).toBe(true);
    expect(isISODate("2026-02-29")).toBe(false);
    expect(isISODate("2026-1-01")).toBe(false);
    expect(isValidPeriod({ from: "2024-10-30", to: "2026-10-29" })).toBe(true);
    expect(periodDays({ from: "2024-10-30", to: "2026-10-29" })).toBe(730);
    expect(isValidPeriod({ from: "2024-10-29", to: "2026-10-29" })).toBe(false);
    expect(isValidPeriod({ from: "2026-10-29", to: "2026-10-28" })).toBe(false);
  });

  it("shifts days across month ends and daylight-saving changes", () => {
    expect(shiftISODate("2026-03-01", -1)).toBe("2026-02-28");
    expect(shiftISODate("2026-10-24", 2)).toBe("2026-10-26");
    expect(periodDays({ from: "2026-03-28", to: "2026-03-30" })).toBe(3);
  });

  it("includes today only when today is one of its days", () => {
    expect(includesToday({ from: "2026-10-23", to: "2026-10-29" }, NOW)).toBe(true);
    expect(includesToday({ from: "2026-10-01", to: "2026-10-28" }, NOW)).toBe(false);
  });

  it("writes the period into the query and keeps other parameters", () => {
    const next = searchWithPeriod(new URLSearchParams("lang=ko&from=2026-01-01"), { from: "2026-10-01", to: "2026-10-28" });
    expect(next.toString()).toBe("lang=ko&from=2026-10-01&to=2026-10-28");
    expect(searchNamesPeriod("?from=2026-10-01&to=2026-10-28")).toBe(true);
    expect(searchNamesPeriod("?to=2026-10-28")).toBe(true);
    expect(searchNamesPeriod("?lang=ko")).toBe(false);
  });

  it("links the errors to the narrowest Requests timeframe covering the period", () => {
    expect(requestsTimeframeFor(presetPeriod("today", NOW), NOW)).toBe("24h");
    expect(requestsTimeframeFor(presetPeriod("7d", NOW), NOW)).toBe("7d");
    expect(requestsTimeframeFor(presetPeriod("90d", NOW), NOW)).toBe("30d");
    expect(keyErrorRequestsRoute("key 1", presetPeriod("7d", NOW), NOW)).toBe("/requests?api_key_id=key+1&status=error&timeframe=7d");
  });
});
