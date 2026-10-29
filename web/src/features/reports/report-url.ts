import { addDaysISO, inclusiveDayCount, isISODate, localDateISO, MAX_REPORT_PERIOD_DAYS } from "./date";

export const QUICK_PERIODS = ["today", "yesterday", "7d", "30d", "90d", "this_month", "last_month"] as const;
export type QuickPeriod = (typeof QUICK_PERIODS)[number];
export const DEFAULT_QUICK_PERIOD: QuickPeriod = "7d";

export type ReportPeriodSelection =
  | { kind: "quick"; period: QuickPeriod }
  | { kind: "custom"; from: string; to: string };

/** The selected groups, keys and the ungrouped keys; empty means every key. */
export type ReportScope = {
  groupIds: string[];
  keyIds: string[];
  ungrouped: boolean;
};

export type ReportUrlState = {
  scope: ReportScope;
  period: ReportPeriodSelection;
};

export type ReportRange = { start: string; end: string };

export type ReportRangeError = "inverted" | "future" | "too_long";

/** The page's own URL parameters; anything else in the query is kept. */
export const REPORT_URL_PARAMS = {
  group: "group",
  key: "key",
  ungrouped: "ungrouped",
  period: "period",
  from: "from",
  to: "to",
} as const;

export const EMPTY_SCOPE: ReportScope = { groupIds: [], keyIds: [], ungrouped: false };

function uniqueIds(values: string[]): string[] {
  const seen = new Set<string>();
  const ids: string[] = [];
  for (const raw of values) {
    const value = raw.trim();
    if (value && !seen.has(value)) {
      seen.add(value);
      ids.push(value);
    }
  }
  return ids;
}

function isQuickPeriod(value: string | null): value is QuickPeriod {
  return value !== null && (QUICK_PERIODS as readonly string[]).includes(value);
}

/**
 * Reads the selection and the period from the page URL: repeatable `group`
 * and `key` ids, `ungrouped=1`, and either `period=<quick>` or custom
 * `from`/`to` dates. Missing or malformed values fall back to every key and
 * the last 7 days.
 */
export function parseReportUrl(params: URLSearchParams): ReportUrlState {
  const scope: ReportScope = {
    groupIds: uniqueIds(params.getAll(REPORT_URL_PARAMS.group)),
    keyIds: uniqueIds(params.getAll(REPORT_URL_PARAMS.key)),
    ungrouped: params.get(REPORT_URL_PARAMS.ungrouped) === "1",
  };
  const from = params.get(REPORT_URL_PARAMS.from);
  const to = params.get(REPORT_URL_PARAMS.to);
  if (isISODate(from) && isISODate(to)) {
    return { scope, period: { kind: "custom", from, to } };
  }
  const period = params.get(REPORT_URL_PARAMS.period);
  return { scope, period: { kind: "quick", period: isQuickPeriod(period) ? period : DEFAULT_QUICK_PERIOD } };
}

/**
 * Writes the selection and the period into a copy of `current`, replacing
 * the page's own parameters only; the default period (7 days) is omitted.
 */
export function writeReportUrl(state: ReportUrlState, current?: URLSearchParams): URLSearchParams {
  const params = new URLSearchParams(current);
  for (const name of Object.values(REPORT_URL_PARAMS)) {
    params.delete(name);
  }
  for (const id of state.scope.groupIds) {
    params.append(REPORT_URL_PARAMS.group, id);
  }
  for (const id of state.scope.keyIds) {
    params.append(REPORT_URL_PARAMS.key, id);
  }
  if (state.scope.ungrouped) {
    params.set(REPORT_URL_PARAMS.ungrouped, "1");
  }
  if (state.period.kind === "custom") {
    params.set(REPORT_URL_PARAMS.from, state.period.from);
    params.set(REPORT_URL_PARAMS.to, state.period.to);
  } else if (state.period.period !== DEFAULT_QUICK_PERIOD) {
    params.set(REPORT_URL_PARAMS.period, state.period.period);
  }
  return params;
}

export function isScopeEmpty(scope: ReportScope): boolean {
  return scope.groupIds.length === 0 && scope.keyIds.length === 0 && !scope.ungrouped;
}

function monthStart(year: number, month: number): string {
  return localDateISO(new Date(year, month, 1));
}

/** The local calendar days of a quick period, relative to `now`. */
export function quickPeriodRange(period: QuickPeriod, now: Date = new Date()): ReportRange {
  const today = localDateISO(now);
  switch (period) {
    case "today":
      return { start: today, end: today };
    case "yesterday": {
      const yesterday = addDaysISO(today, -1);
      return { start: yesterday, end: yesterday };
    }
    case "7d":
      return { start: addDaysISO(today, -6), end: today };
    case "30d":
      return { start: addDaysISO(today, -29), end: today };
    case "90d":
      return { start: addDaysISO(today, -89), end: today };
    case "this_month":
      return { start: monthStart(now.getFullYear(), now.getMonth()), end: today };
    case "last_month": {
      const start = monthStart(now.getFullYear(), now.getMonth() - 1);
      const end = addDaysISO(monthStart(now.getFullYear(), now.getMonth()), -1);
      return { start, end };
    }
  }
}

export function resolveReportRange(period: ReportPeriodSelection, now: Date = new Date()): ReportRange {
  return period.kind === "custom" ? { start: period.from, end: period.to } : quickPeriodRange(period.period, now);
}

/**
 * Why a custom period cannot be requested: an inverted range, a date later
 * than the browser-local today, or more than 730 days; null when valid.
 */
export function reportRangeError(range: ReportRange, today: string = localDateISO()): ReportRangeError | null {
  if (range.start > range.end) {
    return "inverted";
  }
  if (range.end > today) {
    return "future";
  }
  if (inclusiveDayCount(range.start, range.end) > MAX_REPORT_PERIOD_DAYS) {
    return "too_long";
  }
  return null;
}
