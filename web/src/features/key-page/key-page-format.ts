import { formatKeyLimitValue } from "@/features/api-keys/components/limit-rules-utils";
import type { LimitType } from "@/features/api-keys/schemas";
import { formatCompactNumber, formatCurrency, formatTimeLong, parseDate } from "@/utils/formatters";

export const DASH = "—";

/** A latency in milliseconds: "850 ms", "1.2 s", or a dash when unknown. */
export function msLabel(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return DASH;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

export function countLabel(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return DASH;
  return formatCompactNumber(value);
}

export function usd(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return DASH;
  return formatCurrency(value);
}

/** A percent value (0..100) with one decimal below 10. */
export function sharePercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return DASH;
  return value > 0 && value < 10 ? `${value.toFixed(1)}%` : `${Math.round(value)}%`;
}

/** A fraction (0..1) as a percent with one decimal. */
export function fractionPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return DASH;
  return `${(value * 100).toFixed(1)}%`;
}

/** The change against the previous period, in percent; null without a base. */
export function deltaPercent(current: number, previous: number | null | undefined): number | null {
  if (previous === null || previous === undefined || previous <= 0) return null;
  return ((current - previous) / previous) * 100;
}

/** A limit amount in its unit: dollars for cost (stored in microdollars). */
export function limitValue(value: number, type: LimitType): string {
  return formatKeyLimitValue(value, type);
}

/** Tokens per second with one decimal. */
export function tpsLabel(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return DASH;
  return value.toFixed(1);
}

/** The local date and time of an instant, in the dashboard's date format. */
export function dateTimeLabel(iso: string | null | undefined): string {
  const date = parseDate(iso);
  if (!date) return DASH;
  const formatted = formatTimeLong(iso);
  return `${formatted.date} ${formatted.time}`;
}

/** HH:MM when the instant is today, otherwise the date and HH:MM. */
export function clockOrDateLabel(iso: string | null | undefined, now = new Date()): string {
  const date = parseDate(iso);
  if (!date) return DASH;
  const clock = date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (date.toDateString() === now.toDateString()) return clock;
  return `${date.toLocaleDateString([], { month: "short", day: "numeric" })} ${clock}`;
}

/** The MM-DD tick of a YYYY-MM-DD day. */
export function dayTick(day: string): string {
  return day.slice(5);
}
