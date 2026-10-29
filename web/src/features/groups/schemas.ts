import { z } from "zod";

import { ShellStatusSchema } from "@/components/layout/shell-status";
import { LIMIT_TYPES, LIMIT_WINDOWS } from "@/features/api-keys/schemas";
import { GroupRowSchema } from "@/features/overview/schemas";

const Time = z.iso.datetime({ offset: true });

/** One `group_limits` template: a rule copied into every member key. */
export const LimitTemplateSchema = z.object({
  limitType: z.enum(LIMIT_TYPES),
  limitWindow: z.enum(LIMIT_WINDOWS),
  modelFilter: z.string().nullable(),
  maxValue: z.number().int(),
});

export const GroupKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  prefix: z.string(),
  status: z.string(),
  spendTodayUsd: z.number(),
  spend7dUsd: z.number().nullable(),
  spend30dUsd: z.number().nullable(),
  maxLimitPercent: z.number().nullable(),
  lastUsedAt: Time.nullable(),
});

/** An overview group row with the Groups page additions (use case 7). */
export const GroupSummaryRowSchema = GroupRowSchema.extend({
  spend30dUsd: z.number().nullable(),
  limits: z.array(LimitTemplateSchema),
  accountIds: z.array(z.string()),
  topKeys7d: z.array(GroupKeySchema),
});

export const GroupAccountSchema = z.object({
  id: z.string(),
  label: z.string(),
  email: z.string(),
  kind: z.string(),
  status: z.string(),
  health: z.enum(["ok", "low", "limited", "reauth", "paused", "off"]),
  primaryRemainingPercent: z.number().nullable(),
  secondaryRemainingPercent: z.number().nullable(),
});

export const GroupsSummarySchema = z.object({
  generatedAt: Time,
  timezone: z.string(),
  shell: ShellStatusSchema,
  pool: GroupSummaryRowSchema,
  groups: z.array(GroupSummaryRowSchema),
  accounts: z.array(GroupAccountSchema),
});

export type LimitTemplate = z.infer<typeof LimitTemplateSchema>;
export type GroupKey = z.infer<typeof GroupKeySchema>;
export type GroupSummaryRow = z.infer<typeof GroupSummaryRowSchema>;
export type GroupAccount = z.infer<typeof GroupAccountSchema>;
export type GroupsSummary = z.infer<typeof GroupsSummarySchema>;
