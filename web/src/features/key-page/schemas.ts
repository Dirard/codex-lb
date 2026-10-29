import { z } from "zod";

import { ShellStatusSchema } from "@/components/layout/shell-status";
import { LIMIT_TYPES, LIMIT_WINDOWS } from "@/features/api-keys/schemas";
import { WindowHealthSchema } from "@/features/overview/schemas";

/**
 * The key page contract (internal/domain/key_page.go). Fields the owner scope
 * omits (R26) are optional; Go encodes empty slices as null, which the lists
 * read as empty.
 */
const Time = z.iso.datetime({ offset: true });
const Day = z.iso.date();
const Ms = z.number().nullable();

function list<T extends z.ZodType>(item: T) {
  return z.array(item).nullable().transform((value) => value ?? []);
}

export const KeyLimitStateSchema = z.object({
  type: z.enum(LIMIT_TYPES),
  window: z.enum(LIMIT_WINDOWS),
  modelFilter: z.string().nullable(),
  /** Limit units; microdollars for cost limits. */
  current: z.number(),
  max: z.number(),
  /** 0..100 and beyond, not clamped. */
  percent: z.number(),
  resetAt: Time,
  expired: z.boolean(),
  /** Limit units per hour over the last 3 hours. */
  pacePerHour: z.number().nullable(),
  projectedExhaustionAt: Time.nullable(),
  paceBasis: z.string(),
});

const PurchasedCreditsSchema = z.object({
  remaining: z.number().nullable(),
  unlimited: z.boolean(),
  knownAccountCount: z.number().int(),
});

export const KeyPagePoolSchema = z.object({
  scope: z.enum(["pool", "group"]),
  accountCount: z.number().int(),
  primary: WindowHealthSchema,
  secondary: WindowHealthSchema,
  purchasedCredits: PurchasedCreditsSchema.nullable(),
});

/** The safe same-group key of the key report: no prefixes or assignments. */
export const KeyPageGroupKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  isActive: z.boolean(),
  expiresAt: Time.nullable(),
  isCurrent: z.boolean(),
  limits: list(z.object({
    id: z.number().optional(),
    limitType: z.enum(LIMIT_TYPES),
    limitWindow: z.enum(LIMIT_WINDOWS),
    maxValue: z.number(),
    currentValue: z.number(),
    modelFilter: z.string().nullable(),
    resetAt: Time,
  })),
});

export const KeyPageGroupSchema = z.object({
  id: z.string(),
  name: z.string(),
  keys: list(KeyPageGroupKeySchema),
});

export const KeyPageKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  prefix: z.string(),
  status: z.enum(["active", "disabled", "expired"]),
  groupId: z.string().nullable(),
  groupName: z.string().nullable(),
  createdAt: Time,
  lastUsedAt: Time.nullable(),
  expiresAt: Time.nullable(),
  allowedModels: z.array(z.string()).nullable(),
  enforcedModel: z.string().nullable(),
  enforcedReasoningEffort: z.string().nullable(),
  allowedReasoningEfforts: z.array(z.string()).nullable(),
  trafficClass: z.string(),
  serviceTier: z.string().nullable(),
  usageSections: list(z.string()),
  accountAssignmentScopeEnabled: z.boolean(),
  sourceAssignmentScopeEnabled: z.boolean(),
  /** Administrator only. */
  assignedAccounts: list(z.object({ id: z.string(), label: z.string() })).optional(),
  assignedAccountCount: z.number().int(),
  assignedSourceCount: z.number().int(),
});

export const KeyPagePeriodSchema = z.object({
  start: Day,
  end: Day,
  timezone: z.string(),
  days: z.number().int(),
  previousStart: Day,
  previousEnd: Day,
  includesToday: z.boolean(),
});

export const KeyPeriodSummarySchema = z.object({
  requests: z.number(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  cachedInputTokens: z.number(),
  reasoningTokens: z.number().nullable(),
  /** Cached share of input tokens, 0..1. */
  cacheShare: z.number().nullable(),
  costUsd: z.number(),
  errors: z.number(),
  /** Errors per request, 0..1. */
  errorRate: z.number().nullable(),
  cancelled: z.number(),
  ttftP50Ms: Ms,
  ttftP95Ms: Ms,
  totalP50Ms: Ms,
  totalP95Ms: Ms,
  queueP95Ms: Ms,
  tpsP50: z.number().nullable(),
  previous: z.object({ requests: z.number(), tokens: z.number(), costUsd: z.number() }).nullable(),
});

export const KeyDayModelSchema = z.object({
  model: z.string(),
  other: z.boolean(),
  requests: z.number(),
  tokens: z.number(),
  costUsd: z.number(),
});

export const KeyDaySchema = z.object({
  date: Day,
  requests: z.number(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  cachedInputTokens: z.number(),
  costUsd: z.number(),
  errors: z.number(),
  cancelled: z.number(),
  byModel: list(KeyDayModelSchema),
  ttftP50Ms: Ms,
  ttftP95Ms: Ms,
  totalP50Ms: Ms,
  totalP95Ms: Ms,
  queueP95Ms: Ms,
  tpsP50: z.number().nullable(),
});

/** Share is a percent (0..100) in every row below. */
export const KeyModelRowSchema = z.object({
  model: z.string(),
  requests: z.number(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  cachedInputTokens: z.number(),
  costUsd: z.number(),
  errors: z.number(),
  share: z.number(),
  ttftP50Ms: Ms,
});

export const KeyErrorCodeRowSchema = z.object({
  code: z.string(),
  count: z.number(),
  share: z.number(),
  lastSeenAt: Time.nullable(),
});

export const KeyUserAgentRowSchema = z.object({
  group: z.string(),
  requests: z.number(),
  costUsd: z.number(),
  share: z.number(),
});

export const KeyAccountCostRowSchema = z.object({
  accountId: z.string().nullable(),
  label: z.string().nullable(),
  deleted: z.boolean(),
  requests: z.number(),
  costUsd: z.number(),
  share: z.number(),
});

export const UsageTotalsSchema = z.object({
  requestCount: z.number(),
  failedCount: z.number(),
  usage: z.object({
    inputTokens: z.number(),
    outputTokens: z.number(),
    cachedInputTokens: z.number(),
    reasoningTokens: z.number(),
    costMicrodollars: z.number(),
  }),
});

const adminOnly = z.string().nullable().optional();

export const KeyRequestRowSchema = z.object({
  requestedAt: Time,
  requestId: z.string(),
  requestKind: z.string(),
  model: z.string(),
  reasoningEffort: z.string().nullable(),
  status: z.string(),
  errorCode: z.string().nullable(),
  inputTokens: z.number().nullable(),
  outputTokens: z.number().nullable(),
  cachedInputTokens: z.number().nullable(),
  reasoningTokens: z.number().nullable(),
  costUsd: z.number().nullable(),
  latencyMs: z.number().nullable(),
  latencyFirstTokenMs: z.number().nullable(),
  latencyQueueMs: z.number().nullable(),
  transport: z.string().nullable(),
  serviceTier: z.string().nullable(),
  useragentGroup: z.string().nullable(),
  // Administrator only (R26).
  accountId: adminOnly,
  planType: adminOnly,
  clientIp: adminOnly,
  conversationId: adminOnly,
  useragent: adminOnly,
  errorMessage: adminOnly,
  source: adminOnly,
  modelSourceId: adminOnly,
  modelSourceKind: adminOnly,
});

export const KeyPageResponseSchema = z.object({
  scope: z.enum(["admin", "owner"]),
  generatedAt: Time,
  /** Administrator only: written into the shell status query. */
  shell: ShellStatusSchema.optional(),
  key: KeyPageKeySchema,
  period: KeyPagePeriodSchema,
  rollupsUsable: z.boolean(),
  detailsFrom: Time.nullable(),
  limits: list(KeyLimitStateSchema),
  pool: KeyPagePoolSchema.nullable(),
  group: KeyPageGroupSchema.nullable(),
  summary: KeyPeriodSummarySchema,
  daily: list(KeyDaySchema),
  byModel: list(KeyModelRowSchema),
  errorsByCode: list(KeyErrorCodeRowSchema),
  userAgents: list(KeyUserAgentRowSchema),
  /** Administrator only. */
  byAccount: list(KeyAccountCostRowSchema).optional(),
  /** Administrator only. */
  lifetime: UsageTotalsSchema.optional(),
  recentRequests: list(KeyRequestRowSchema),
  nextCursor: z.string().nullable(),
});

export const KeyRequestsResponseSchema = z.object({
  items: list(KeyRequestRowSchema),
  nextCursor: z.string().nullable(),
});

export type KeyLimitState = z.infer<typeof KeyLimitStateSchema>;
export type KeyPagePool = z.infer<typeof KeyPagePoolSchema>;
export type KeyPageGroup = z.infer<typeof KeyPageGroupSchema>;
export type KeyPageKey = z.infer<typeof KeyPageKeySchema>;
export type KeyPeriodSummary = z.infer<typeof KeyPeriodSummarySchema>;
export type KeyDay = z.infer<typeof KeyDaySchema>;
export type KeyModelRow = z.infer<typeof KeyModelRowSchema>;
export type KeyErrorCodeRow = z.infer<typeof KeyErrorCodeRowSchema>;
export type KeyUserAgentRow = z.infer<typeof KeyUserAgentRowSchema>;
export type KeyAccountCostRow = z.infer<typeof KeyAccountCostRowSchema>;
export type UsageTotals = z.infer<typeof UsageTotalsSchema>;
export type KeyRequestRow = z.infer<typeof KeyRequestRowSchema>;
export type KeyPageResponse = z.infer<typeof KeyPageResponseSchema>;
/** The JSON the server sends, before null lists are read as empty. */
export type KeyPageResponseJson = z.input<typeof KeyPageResponseSchema>;
export type KeyRequestsResponse = z.infer<typeof KeyRequestsResponseSchema>;
export type KeyRequestsResponseJson = z.input<typeof KeyRequestsResponseSchema>;
export type KeyPageMode = "admin" | "owner";
