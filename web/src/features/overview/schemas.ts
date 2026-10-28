import { z } from "zod";

import { ShellStatusSchema } from "@/components/layout/shell-status";

const Time = z.iso.datetime({ offset: true });

export const HealthCountsSchema = z.object({
  ok: z.number().int(),
  low: z.number().int(),
  limited: z.number().int(),
  reauth: z.number().int(),
  paused: z.number().int(),
  off: z.number().int(),
});

export const WindowHealthSchema = z.object({
  remainingPercent: z.number().nullable(),
  remainingCredits: z.number().nullable(),
  capacityCredits: z.number().nullable(),
  coveredAccounts: z.number().int(),
  totalAccounts: z.number().int(),
  nextResetAt: Time.nullable(),
  fullResetAt: Time.nullable(),
  burnCreditsPerHour: z.number().nullable(),
  projectedExhaustionAt: Time.nullable(),
  runsOutBeforeReset: z.boolean(),
});

export const PoolSummarySchema = z.object({
  accounts: HealthCountsSchema,
  externalSources: z.number().int(),
  primary: WindowHealthSchema,
  secondary: WindowHealthSchema,
  spendTodayUsd: z.number(),
  spendYesterdaySameTimeUsd: z.number().nullable(),
  requestsToday: z.number().int(),
  errorRateLastHour: z.number().nullable(),
  spend7dUsd: z.number().nullable(),
  trend7d: z.array(z.number().nullable()).nullable(),
});

export const ALERT_RULES = [
  "account_reauth",
  "quota_runway",
  "key_limit",
  "error_spike",
  "key_error_spike",
  "key_expiry",
  "quota_data_stale",
] as const;

export const ALERT_ACTION_KINDS = [
  "reauth",
  "open_group",
  "open_accounts",
  "edit_limits",
  "open_group_limits",
  "open_requests",
  "open_key_errors",
  "extend_expiry",
  "open_list",
] as const;

export const AlertSchema = z.object({
  id: z.string(),
  rule: z.enum(ALERT_RULES),
  severity: z.enum(["warning", "critical"]),
  subject: z.object({
    type: z.enum(["pool", "group", "account", "key"]),
    id: z.string().nullable(),
    name: z.string(),
  }),
  window: z.string().nullable(),
  value: z.number().nullable(),
  at: Time.nullable(),
  count: z.number().int(),
  action: z.object({
    kind: z.enum(ALERT_ACTION_KINDS),
    target: z.string(),
  }),
});

export const ProblemAccountSchema = z.object({
  id: z.string(),
  label: z.string(),
  status: z.string(),
  health: z.enum(["ok", "low", "limited", "reauth", "paused", "off"]),
  primaryRemainingPercent: z.number().nullable(),
  primaryResetAt: Time.nullable(),
  secondaryRemainingPercent: z.number().nullable(),
  secondaryResetAt: Time.nullable(),
  action: z.enum(["reauth", "resume", "open"]),
});

export const TopKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  spendTodayUsd: z.number(),
  spend7dUsd: z.number().nullable(),
  maxLimitPercent: z.number().nullable(),
  errorRateLastHour: z.number().nullable(),
  lastUsedAt: Time.nullable(),
});

export const GroupRowSchema = z.object({
  id: z.string().nullable(),
  name: z.string(),
  attention: z.enum(["none", "warning", "critical"]),
  accounts: HealthCountsSchema,
  primary: WindowHealthSchema,
  secondary: WindowHealthSchema,
  limitFill: z.object({
    maxPercent: z.number().nullable(),
    keysAtOrAbove90: z.number().int(),
    keysBlocked: z.number().int(),
  }),
  keys: z.object({ active: z.number().int(), total: z.number().int() }),
  spendTodayUsd: z.number(),
  spend7dUsd: z.number().nullable(),
  trend7d: z.array(z.number().nullable()).nullable(),
  problemAccounts: z.array(ProblemAccountSchema),
  topKeys: z.array(TopKeySchema),
});

export const LimitStateSchema = z.object({
  type: z.string(),
  window: z.string(),
  modelFilter: z.string().nullable(),
  percent: z.number(),
  resetAt: Time,
});

export const ATTENTION_PRIMARY_ACTIONS = ["edit_limits", "open_group_limits", "open_key_errors", "extend_expiry", "open_key"] as const;

export const AttentionKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  prefix: z.string(),
  groupId: z.string().nullable(),
  groupName: z.string().nullable(),
  reasons: z.array(
    z.object({
      reason: z.enum(["limit_blocked", "limit_near", "error_spike", "expiring", "spend_spike"]),
      severity: z.enum(["warning", "critical"]),
      value: z.number().nullable(),
    }),
  ),
  spendTodayUsd: z.number(),
  spend7dUsd: z.number().nullable(),
  maxLimit: LimitStateSchema.nullable(),
  expiresAt: Time.nullable(),
  lastUsedAt: Time.nullable(),
  primaryAction: z.enum(ATTENTION_PRIMARY_ACTIONS),
});

export const OverviewResponseSchema = z.object({
  generatedAt: Time,
  timezone: z.string(),
  shell: ShellStatusSchema,
  pool: PoolSummarySchema,
  alerts: z.array(AlertSchema),
  groups: z.array(GroupRowSchema),
  attentionKeys: z.array(AttentionKeySchema),
  attentionKeysTotal: z.number().int(),
});

export type HealthCounts = z.infer<typeof HealthCountsSchema>;
export type WindowHealth = z.infer<typeof WindowHealthSchema>;
export type PoolSummary = z.infer<typeof PoolSummarySchema>;
export type Alert = z.infer<typeof AlertSchema>;
export type AlertAction = Alert["action"];
export type ProblemAccount = z.infer<typeof ProblemAccountSchema>;
export type TopKey = z.infer<typeof TopKeySchema>;
export type GroupRow = z.infer<typeof GroupRowSchema>;
export type AttentionKey = z.infer<typeof AttentionKeySchema>;
export type OverviewResponse = z.infer<typeof OverviewResponseSchema>;
