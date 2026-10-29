import { formatCompactNumber, formatCurrency, formatNumber } from "@/utils/formatters";

import type { UsageMeasures } from "./schemas";

export const ZERO_MEASURES: UsageMeasures = {
  requests: 0,
  inputTokens: 0,
  outputTokens: 0,
  cachedInputTokens: 0,
  reasoningTokens: 0,
  costUsd: 0,
  errors: 0,
  cancelled: 0,
};

/** Input plus output tokens, the token figure of every report table. */
export function totalTokens(measures: UsageMeasures): number {
  return measures.inputTokens + measures.outputTokens;
}

/** Errors per request as a fraction, null without requests. */
export function errorRate(measures: UsageMeasures): number | null {
  return measures.requests > 0 ? measures.errors / measures.requests : null;
}

export function sumMeasures(items: UsageMeasures[]): UsageMeasures {
  const total = { ...ZERO_MEASURES };
  let reasoningKnown = true;
  for (const item of items) {
    total.requests += item.requests;
    total.inputTokens += item.inputTokens;
    total.outputTokens += item.outputTokens;
    total.cachedInputTokens += item.cachedInputTokens;
    total.costUsd += item.costUsd;
    total.errors += item.errors;
    total.cancelled += item.cancelled;
    if (item.reasoningTokens === null) {
      reasoningKnown = false;
    } else {
      total.reasoningTokens = (total.reasoningTokens ?? 0) + item.reasoningTokens;
    }
  }
  if (!reasoningKnown) {
    total.reasoningTokens = null;
  }
  return total;
}

/**
 * The change against the previous period in percent, or null when there is
 * no previous period or its value is zero (nothing to compare with).
 */
export function percentChange(current: number, previous: number | null | undefined): number | null {
  if (previous === null || previous === undefined || previous === 0) {
    return null;
  }
  return ((current - previous) / previous) * 100;
}

/** "▲ 12%", "▼ 3%" or "0%"; below 10 % one decimal is kept. */
export function percentChangeLabel(change: number): string {
  const magnitude = Math.abs(change);
  const digits = magnitude < 10 ? 1 : 0;
  const rounded = Number(magnitude.toFixed(digits));
  if (rounded === 0) {
    return "0%";
  }
  return `${change > 0 ? "▲" : "▼"} ${rounded.toFixed(digits)}%`;
}

export function countLabel(value: number): string {
  return formatNumber(value);
}

export function tokenLabel(value: number): string {
  return value < 10_000 ? formatNumber(value) : formatCompactNumber(value);
}

export function costLabel(value: number): string {
  if (value > 0 && value < 0.01) {
    return "<$0.01";
  }
  return formatCurrency(value);
}

/** A cost share given as a fraction, with one decimal. */
export function shareLabel(share: number): string {
  return `${(share * 100).toFixed(1)}%`;
}

export function rateLabel(rate: number | null): string {
  return rate === null ? "—" : `${(rate * 100).toFixed(1)}%`;
}

/** A limit fill given in percent. */
export function limitLabel(percent: number | null): string {
  return percent === null ? "—" : `${Math.round(percent)}%`;
}
