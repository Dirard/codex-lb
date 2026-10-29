import { z } from "zod";

import { ShellStatusSchema } from "@/components/layout/shell-status";
import { LIMIT_TYPES, LIMIT_WINDOWS } from "@/features/api-keys/schemas";

const Time = z.iso.datetime({ offset: true });

export const KEY_SORTS = ["name", "created", "spend_today", "spend_7d", "spend_30d", "limit_fill", "last_used"] as const;
export type KeySort = (typeof KEY_SORTS)[number];

export const KEY_STATUSES = ["active", "disabled", "expired"] as const;
export type KeyStatus = (typeof KEY_STATUSES)[number];

export const KeyLimitStateSchema = z.object({
  type: z.enum(LIMIT_TYPES),
  window: z.enum(LIMIT_WINDOWS),
  modelFilter: z.string().nullable(),
  percent: z.number(),
  resetAt: Time,
});

export const KeyLimitSummarySchema = z.object({
  type: z.enum(LIMIT_TYPES),
  window: z.enum(LIMIT_WINDOWS),
  modelFilter: z.string().nullable(),
  maxValue: z.number(),
});

export const KeyRowSchema = z.object({
  id: z.string(),
  name: z.string(),
  prefix: z.string(),
  groupId: z.string().nullable(),
  groupName: z.string().nullable(),
  status: z.enum(KEY_STATUSES),
  expiresAt: Time.nullable(),
  lastUsedAt: Time.nullable(),
  createdAt: Time,
  spendTodayUsd: z.number(),
  spend7dUsd: z.number().nullable(),
  spend30dUsd: z.number().nullable(),
  maxLimit: KeyLimitStateSchema.nullable(),
  limitsSummary: z.array(KeyLimitSummarySchema),
});

/** GET /api/keys: one page of the matching keys and the total before paging. */
export const KeysPageSchema = z.object({
  shell: ShellStatusSchema,
  generatedAt: Time,
  timezone: z.string(),
  total: z.number().int().nonnegative(),
  offset: z.number().int().nonnegative(),
  limit: z.number().int().positive(),
  rows: z.array(KeyRowSchema),
});

/** GET /api/keys/ids: at most 1 000 matching ids in list order. */
export const KeyIdsSchema = z.object({
  ids: z.array(z.string()),
  total: z.number().int().nonnegative(),
});

export const KEY_BULK_ACTIONS = ["disable", "enable", "move_group", "set_limits"] as const;
export type KeyBulkAction = (typeof KEY_BULK_ACTIONS)[number];

export const KeyBulkResultSchema = z.object({
  id: z.string(),
  ok: z.boolean(),
  code: z.string().optional(),
});

export const KeyBulkResponseSchema = z.object({
  results: z.array(KeyBulkResultSchema),
});

export type KeyRow = z.infer<typeof KeyRowSchema>;
export type KeysPage = z.infer<typeof KeysPageSchema>;
export type KeyIds = z.infer<typeof KeyIdsSchema>;
export type KeyBulkResult = z.infer<typeof KeyBulkResultSchema>;
export type KeyBulkResponse = z.infer<typeof KeyBulkResponseSchema>;
