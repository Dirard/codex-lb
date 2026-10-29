import type { DateDisplayFormat } from "@/hooks/use-date-format";
import { formatTimeLong } from "@/utils/formatters";

const REPORTS_TIMEZONE_STORAGE_KEY = "codex-lb-reports-timezone";

function isValidTimeZone(timeZone: string | undefined): timeZone is string {
  if (!timeZone) {
    return false;
  }

  if (typeof Intl.supportedValuesOf === "function") {
    return timeZone === "UTC" || Intl.supportedValuesOf("timeZone").includes(timeZone);
  }

  try {
    new Intl.DateTimeFormat(undefined, { timeZone });
    return true;
  } catch {
    return false;
  }
}

export function getBrowserReportsTimeZone(): string | undefined {
  const detectedTimeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (isValidTimeZone(detectedTimeZone)) {
    try {
      window.localStorage.setItem(REPORTS_TIMEZONE_STORAGE_KEY, detectedTimeZone);
    } catch {
      // Some browser contexts block storage access; detection still succeeded.
    }
    return detectedTimeZone;
  }

  try {
    const cachedTimeZone = window.localStorage.getItem(REPORTS_TIMEZONE_STORAGE_KEY) ?? undefined;
    if (isValidTimeZone(cachedTimeZone)) {
      return cachedTimeZone;
    }
  } catch {
    // Some browser contexts block storage access; fall through and omit timezone.
  }

  return undefined;
}

export function localDateISO(date: Date = new Date()): string {
  const localTime = date.getTime() - date.getTimezoneOffset() * 60_000;
  return new Date(localTime).toISOString().slice(0, 10);
}

export function daysAgoLocalISO(days: number, date: Date = new Date()): string {
  const shifted = new Date(date);
  shifted.setDate(shifted.getDate() - days);
  return localDateISO(shifted);
}

export function isReportDateRangeValid(
  startDate: string | undefined,
  endDate: string | undefined,
): boolean {
  return !startDate || !endDate || startDate <= endDate;
}

export function formatReportBucketDate(
  date: string,
  displayFormat: DateDisplayFormat,
): string {
  const [year, month, day] = date.split("-");
  if (!year || !month || !day) {
    return date;
  }
  return formatTimeLong(`${year}-${month}-${day}T00:00:00`, displayFormat).date;
}

/** The longest custom period of the Reports page, in local days. */
export const MAX_REPORT_PERIOD_DAYS = 730;

/** True for a real calendar date written as YYYY-MM-DD. */
export function isISODate(value: string | null | undefined): value is string {
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    return false;
  }
  const parsed = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(parsed.getTime()) && parsed.toISOString().slice(0, 10) === value;
}

/** The calendar date `days` after (or before, when negative) an ISO date. */
export function addDaysISO(date: string, days: number): string {
  const shifted = new Date(`${date}T00:00:00Z`);
  shifted.setUTCDate(shifted.getUTCDate() + days);
  return shifted.toISOString().slice(0, 10);
}

/** The number of calendar days from start to end, both included. */
export function inclusiveDayCount(startDate: string, endDate: string): number {
  const start = Date.parse(`${startDate}T00:00:00Z`);
  const end = Date.parse(`${endDate}T00:00:00Z`);
  return Math.round((end - start) / 86_400_000) + 1;
}

/** Every calendar date from start to end, both included; empty when inverted. */
export function eachISODate(startDate: string, endDate: string): string[] {
  if (!isISODate(startDate) || !isISODate(endDate) || startDate > endDate) {
    return [];
  }
  const dates: string[] = [];
  for (let current = startDate; current <= endDate; current = addDaysISO(current, 1)) {
    dates.push(current);
  }
  return dates;
}
