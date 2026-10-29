import { z } from "zod";

const DailyReportRowSchema = z.object({
  date: z.string(),
  requests: z.number(),
  conversations: z.number(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  reasoningTokens: z.number().nullable(),
  cachedInputTokens: z.number(),
  costUsd: z.number(),
  activeAccounts: z.number(),
  cancelledCount: z.number(),
  errorCount: z.number(),
  medianTtftMs: z.number().optional().default(0),
  medianTps: z.number().optional().default(0),
  medianQueueMs: z.number().optional().default(0),
});

const ModelCostEntrySchema = z.object({
  model: z.string(),
  costUsd: z.number(),
  requests: z.number(),
  percentage: z.number(),
});

const UseragentCostEntrySchema = z.object({
  useragent: z.string(),
  costUsd: z.number(),
  requests: z.number(),
  percentage: z.number(),
});

const AccountCostEntrySchema = z.object({
  accountId: z.string().nullable(),
  alias: z.string().nullable(),
  costUsd: z.number(),
  requests: z.number(),
});

const ReportSummarySchema = z.object({
  totalCostUsd: z.number(),
  totalInputTokens: z.number(),
  totalOutputTokens: z.number(),
  totalReasoningTokens: z.number(),
  reasoningUsageKnownRequests: z.number(),
  totalCachedTokens: z.number(),
  totalRequests: z.number(),
  totalCancelled: z.number(),
  totalErrors: z.number(),
  totalConversations: z.number(),
  activeAccounts: z.number(),
  avgCostPerDay: z.number(),
  avgRequestsPerDay: z.number(),
});

const ReportComparisonPreviousSchema = z.object({
  totalCostUsd: z.number(),
  totalTokens: z.number(),
  totalRequests: z.number(),
});

const ReportComparisonSchema = z.object({
  canCompare: z.boolean(),
  previous: ReportComparisonPreviousSchema,
});

export const ReportsResponseSchema = z.object({
  summary: ReportSummarySchema,
  comparison: ReportComparisonSchema,
  daily: z.array(DailyReportRowSchema),
  byModel: z.array(ModelCostEntrySchema),
  byUseragent: z.array(UseragentCostEntrySchema),
  byAccount: z.array(AccountCostEntrySchema),
});

export type DailyReportRow = z.input<typeof DailyReportRowSchema>;
export type ModelCostEntry = z.infer<typeof ModelCostEntrySchema>;
export type UseragentCostEntry = z.infer<typeof UseragentCostEntrySchema>;
export type AccountCostEntry = z.infer<typeof AccountCostEntrySchema>;
export type ReportSummary = z.infer<typeof ReportSummarySchema>;
export type ReportComparison = z.infer<typeof ReportComparisonSchema>;
export type ReportsResponse = z.infer<typeof ReportsResponseSchema>;

const ReportDateSchema = z.string().regex(/^\d{4}-\d{2}-\d{2}$/);

export const UsageMeasuresSchema = z.object({
  requests: z.number().int(),
  inputTokens: z.number().int(),
  outputTokens: z.number().int(),
  cachedInputTokens: z.number().int(),
  reasoningTokens: z.number().int().nullable(),
  costUsd: z.number(),
  errors: z.number().int(),
  cancelled: z.number().int(),
});

const KeyReportPeriodSchema = z.object({
  start: ReportDateSchema,
  end: ReportDateSchema,
  timezone: z.string(),
  previousStart: ReportDateSchema,
  previousEnd: ReportDateSchema,
  days: z.number().int(),
});

const KeyReportSelectionSchema = z.object({
  groupIds: z.array(z.string()),
  keyIds: z.array(z.string()),
  ungrouped: z.boolean(),
  allKeys: z.boolean(),
  resolvedKeyCount: z.number().int(),
});

const KeyReportSeriesSchema = z.object({
  id: z.string().nullable(),
  name: z.string(),
  kind: z.enum(["key", "group", "ungrouped", "other"]),
});

const GroupReportRowSchema = z.object({
  id: z.string().nullable(),
  name: z.string(),
  keyCount: z.number().int(),
  activeKeys: z.number().int(),
  measures: UsageMeasuresSchema,
  share: z.number(),
});

const KeyReportRowSchema = z.object({
  id: z.string(),
  name: z.string(),
  prefix: z.string(),
  groupId: z.string().nullable(),
  groupName: z.string().nullable(),
  status: z.enum(["active", "disabled", "expired"]),
  measures: UsageMeasuresSchema,
  share: z.number(),
  lastUsedAt: z.string().nullable(),
  maxLimitPercent: z.number().nullable(),
});

const KeyReportSeriesPointSchema = z.object({
  requests: z.number().int(),
  tokens: z.number().int(),
  costUsd: z.number(),
});

const KeyReportDaySchema = z.object({
  date: ReportDateSchema,
  total: UsageMeasuresSchema,
  series: z.array(KeyReportSeriesPointSchema),
});

/** GET /api/reports/keys: the report for a selection of groups and keys. */
export const KeyUsageReportSchema = z.object({
  generatedAt: z.string(),
  period: KeyReportPeriodSchema,
  selection: KeyReportSelectionSchema,
  summary: UsageMeasuresSchema,
  previous: UsageMeasuresSchema.nullable(),
  activeKeys: z.number().int(),
  seriesBy: z.enum(["key", "group"]),
  series: z.array(KeyReportSeriesSchema),
  groups: z.array(GroupReportRowSchema),
  keys: z.array(KeyReportRowSchema),
  daily: z.array(KeyReportDaySchema),
});

export type UsageMeasures = z.infer<typeof UsageMeasuresSchema>;
export type KeyReportSeries = z.infer<typeof KeyReportSeriesSchema>;
export type KeyReportSeriesPoint = z.infer<typeof KeyReportSeriesPointSchema>;
export type GroupReportRow = z.infer<typeof GroupReportRowSchema>;
export type KeyReportRow = z.infer<typeof KeyReportRowSchema>;
export type KeyReportDay = z.infer<typeof KeyReportDaySchema>;
export type KeyUsageReport = z.infer<typeof KeyUsageReportSchema>;
