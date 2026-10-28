import { formatAgo, formatCurrency, formatRelative, parseDate } from "@/utils/formatters";

/** A percent with no decimals, or an em dash when unknown. */
export function percentLabel(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return "—";
  }
  return `${Math.round(value)}%`;
}

/** An error rate given as a fraction, with one decimal. */
export function rateLabel(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return "—";
  }
  return `${(value * 100).toFixed(1)}%`;
}

export function usdLabel(value: number | null | undefined): string {
  if (value === null || value === undefined) {
    return "—";
  }
  return formatCurrency(value);
}

/** "in 2h" for a future instant, null for none or a past one. */
export function untilLabel(iso: string | null | undefined, now = Date.now()): string | null {
  const date = parseDate(iso);
  if (!date) {
    return null;
  }
  const ms = date.getTime() - now;
  if (ms <= 0) {
    return null;
  }
  return formatRelative(ms);
}

/** "5m ago" for a past instant, null for none. */
export function agoLabel(iso: string | null | undefined, now = Date.now()): string | null {
  const date = parseDate(iso);
  if (!date) {
    return null;
  }
  return formatAgo(now - date.getTime());
}

/** Local clock time of an instant, HH:MM. */
export function clockLabel(iso: string | null | undefined): string | null {
  const date = parseDate(iso);
  if (!date) {
    return null;
  }
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** The trend for a sparkline; unknown days count as zero. */
export function trendPoints(trend: (number | null)[] | null | undefined): { value: number }[] {
  return (trend ?? []).map((value) => ({ value: value ?? 0 }));
}
