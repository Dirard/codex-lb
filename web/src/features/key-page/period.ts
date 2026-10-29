/**
 * The key page period: inclusive local days `from`..`to` (YYYY-MM-DD) in the
 * browser's time zone, reflected as `?from=&to=` in the URL (R24). Without
 * them the page shows the 7 days ending today.
 */
export type KeyPagePeriod = {
  /** First local day, YYYY-MM-DD. */
  from: string;
  /** Last local day, YYYY-MM-DD. */
  to: string;
};

export const KEY_PAGE_PRESETS = ["today", "7d", "30d", "90d"] as const;
export type KeyPagePreset = (typeof KEY_PAGE_PRESETS)[number];

/** The longest period the server accepts, in days. */
export const KEY_PAGE_MAX_DAYS = 730;

const PRESET_DAYS: Record<KeyPagePreset, number> = { today: 1, "7d": 7, "30d": 30, "90d": 90 };
const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})$/;
const DAY_MS = 24 * 60 * 60 * 1000;

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

/** The calendar day of an instant in the browser's zone. */
export function localDateISO(date: Date = new Date()): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** UTC midnight of a calendar day, or NaN for an invalid one. */
function dayNumber(value: string): number {
  const match = ISO_DATE.exec(value);
  if (!match) return Number.NaN;
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const time = Date.UTC(year, month - 1, day);
  const parsed = new Date(time);
  if (parsed.getUTCFullYear() !== year || parsed.getUTCMonth() !== month - 1 || parsed.getUTCDate() !== day) {
    return Number.NaN;
  }
  return time;
}

/** Whether a value is an existing calendar day written YYYY-MM-DD. */
export function isISODate(value: string | null | undefined): value is string {
  return typeof value === "string" && Number.isFinite(dayNumber(value));
}

/** The calendar day `days` after (or before, when negative) a day. */
export function shiftISODate(value: string, days: number): string {
  const shifted = new Date(dayNumber(value) + days * DAY_MS);
  return `${shifted.getUTCFullYear()}-${pad(shifted.getUTCMonth() + 1)}-${pad(shifted.getUTCDate())}`;
}

/** The number of days of a period, both ends included. */
export function periodDays(period: KeyPagePeriod): number {
  return Math.round((dayNumber(period.to) - dayNumber(period.from)) / DAY_MS) + 1;
}

/** A period the server accepts: valid days, from ≤ to, at most 730 days. */
export function isValidPeriod(period: KeyPagePeriod): boolean {
  if (!isISODate(period.from) || !isISODate(period.to) || period.from > period.to) return false;
  return periodDays(period) <= KEY_PAGE_MAX_DAYS;
}

/** The preset's days ending today. */
export function presetPeriod(preset: KeyPagePreset, now: Date = new Date()): KeyPagePeriod {
  const to = localDateISO(now);
  return { from: shiftISODate(to, -(PRESET_DAYS[preset] - 1)), to };
}

export function defaultPeriod(now: Date = new Date()): KeyPagePeriod {
  return presetPeriod("7d", now);
}

/** The preset a period equals today, or "custom". */
export function presetOf(period: KeyPagePeriod, now: Date = new Date()): KeyPagePreset | "custom" {
  for (const preset of KEY_PAGE_PRESETS) {
    const candidate = presetPeriod(preset, now);
    if (candidate.from === period.from && candidate.to === period.to) return preset;
  }
  return "custom";
}

/** Whether today (in the browser's zone) is one of the period's days. */
export function includesToday(period: KeyPagePeriod, now: Date = new Date()): boolean {
  const today = localDateISO(now);
  return period.from <= today && today <= period.to;
}

/**
 * The period of a URL query. A lone `from` ends today (or on that day when
 * it lies ahead), a lone `to` covers the 7 days ending on it; anything
 * invalid falls back to the default 7 days ending today.
 */
export function periodFromSearch(search: URLSearchParams | string, now: Date = new Date()): KeyPagePeriod {
  const params = typeof search === "string" ? new URLSearchParams(search) : search;
  const rawFrom = params.get("from");
  const rawTo = params.get("to");
  const fromValid = isISODate(rawFrom);
  const toValid = isISODate(rawTo);
  if (!fromValid && !toValid) return defaultPeriod(now);
  let period: KeyPagePeriod;
  if (fromValid && toValid) {
    period = { from: rawFrom, to: rawTo };
  } else if (fromValid) {
    const today = localDateISO(now);
    period = { from: rawFrom, to: rawFrom > today ? rawFrom : today };
  } else {
    period = { from: shiftISODate(rawTo as string, -6), to: rawTo as string };
  }
  return isValidPeriod(period) ? period : defaultPeriod(now);
}

/** The query with the period's `from` and `to`, other parameters kept. */
export function searchWithPeriod(search: URLSearchParams, period: KeyPagePeriod): URLSearchParams {
  const next = new URLSearchParams(search);
  next.set("from", period.from);
  next.set("to", period.to);
  return next;
}

/** Whether the URL names a period, which preselects the key sign-in (R29). */
export function searchNamesPeriod(search: string): boolean {
  const params = new URLSearchParams(search);
  return params.has("from") || params.has("to");
}

/** The browser's IANA time zone, or UTC when it cannot be determined. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/**
 * The narrowest Requests page timeframe covering the period up to now, at
 * most 30 days (the Requests page's widest timeframe).
 */
export function requestsTimeframeFor(period: KeyPagePeriod, now: Date = new Date()): "24h" | "7d" | "30d" {
  const days = Math.round((dayNumber(localDateISO(now)) - dayNumber(period.from)) / DAY_MS) + 1;
  if (days <= 1) return "24h";
  if (days <= 7) return "7d";
  return "30d";
}

/** The Requests page filtered to the key's errors over the narrowest timeframe covering the period. */
export function keyErrorRequestsRoute(keyId: string, period: KeyPagePeriod, now: Date = new Date()): string {
  const params = new URLSearchParams({ api_key_id: keyId, status: "error", timeframe: requestsTimeframeFor(period, now) });
  return `/requests?${params}`;
}
