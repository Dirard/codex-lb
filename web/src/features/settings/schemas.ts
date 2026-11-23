import { z } from "zod";

const RoutingStrategySchema = z.enum([
  "usage_weighted",
  "round_robin",
  "capacity_weighted",
  "sequential_drain",
  "reset_drain",
  "single_account",
  "relative_availability",
  "fill_first",
]);
const UpstreamStreamTransportSchema = z.enum([
  "default",
  "auto",
  "http",
  "websocket",
]);
const HttpDownstreamTransportPolicySchema = z.enum([
  "smart",
  "always_http",
  "always_websocket",
  "pinned",
]);
const LimitWarmupWindowsSchema = z.enum([
  "primary",
  "secondary",
  "both",
]);
const AdditionalQuotaRoutingPolicySchema = z.enum([
  "inherit",
  "normal",
  "burn_first",
  "preserve",
]);
const AdditionalQuotaPolicySchema = z.object({
  quotaKey: z.string(),
  displayLabel: z.string(),
  routingPolicy: AdditionalQuotaRoutingPolicySchema,
  modelIds: z.array(z.string()).optional().default([]),
});
const LimitWarmupModelSchema = z.string().min(1).max(128);
const LimitWarmupPromptSchema = z.string().min(1).max(512);
export const CodexClientVersionSchema = z
  .string()
  .trim()
  .min(1)
  .max(64)
  .regex(/^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$/);
const WeeklyPaceWorkingDaysValueSchema = z.string().regex(/^[0-6](,[0-6])*$/);
const WeeklyPaceWorkingDaysSchema = WeeklyPaceWorkingDaysValueSchema.default("0,1,2,3,4,5,6");
const WeeklyPaceSmoothingMinutesSchema = z.union([
  z.literal(15),
  z.literal(30),
  z.literal(60),
  z.literal(120),
  z.literal(240),
]);

export const DashboardSettingsSchema = z
  .object({
    stickyThreadsEnabled: z.boolean(),
    upstreamStreamTransport:
      UpstreamStreamTransportSchema.optional().default("default"),
    prohibitFastMode: z.boolean().optional().default(false),
    httpDownstreamTransportPolicy:
      HttpDownstreamTransportPolicySchema.optional().default("smart"),
    preferEarlierResetAccounts: z.boolean(),
    preferEarlierResetWindow: z.enum(["primary", "secondary"]).optional().default("secondary"),
    showResetCreditBadges: z.boolean().optional().default(true),
    showResetCreditExpiryBadge: z.boolean().optional().default(true),
    routingStrategy: RoutingStrategySchema.optional().default("usage_weighted"),
    relativeAvailabilityPower: z.number().positive().optional().default(2),
    relativeAvailabilityTopK: z
      .number()
      .int()
      .min(1)
      .max(20)
      .optional()
      .default(5),
    singleAccountId: z.string().nullable().optional().default(null),
    proxyAccountResponseCreateLimit: z.number().int().min(0).optional().default(4),
    proxyAccountResponseCreateLimitEnvironmentValue: z.number().int().min(0).optional().default(4),
    proxyAccountResponseCreateLimitOverride: z.number().int().min(0).nullable().optional().default(null),
    proxyAccountStreamLimit: z.number().int().min(0).optional().default(8),
    proxyAccountStreamLimitEnvironmentValue: z.number().int().min(0).optional().default(8),
    proxyAccountStreamLimitOverride: z.number().int().min(0).nullable().optional().default(null),
    proxyAccountStreamRecoveryReserve: z.number().int().min(0).optional().default(1),
    proxyAccountStreamRecoveryReserveEnvironmentValue: z.number().int().min(0).optional().default(1),
    proxyAccountStreamRecoveryReserveOverride: z.number().int().min(0).nullable().optional().default(null),
    proxyApiKeyFairShareCongestionThresholdPct: z.number().int().min(0).max(100).optional().default(0),
    proxyApiKeyFairShareCongestionThresholdPctEnvironmentValue: z
      .number()
      .int()
      .min(0)
      .max(100)
      .optional()
      .default(0),
    proxyApiKeyFairShareCongestionThresholdPctOverride: z
      .number()
      .int()
      .min(0)
      .max(100)
      .nullable()
      .optional()
      .default(null),
    openaiCacheAffinityMaxAgeSeconds: z
      .number()
      .int()
      .positive()
      .optional()
      .default(300),
    dashboardSessionTtlSeconds: z
      .number()
      .int()
      .min(3600)
      .optional()
      .default(31536000),
    stickyReallocationBudgetThresholdPct: z.number().min(0).max(100).optional(),
    stickyReallocationPrimaryBudgetThresholdPct: z.number().min(0).max(100).optional(),
    stickyReallocationSecondaryBudgetThresholdPct: z.number().min(0).max(100).optional(),
    additionalQuotaRoutingPolicies: z
      .record(z.string(), AdditionalQuotaRoutingPolicySchema)
      .optional(),
    additionalQuotaPolicies: z.array(AdditionalQuotaPolicySchema).optional().default([]),
    warmupModel: z.string().trim().min(1).optional().default("gpt-5.4-mini"),
    codexClientVersion: CodexClientVersionSchema.optional().default("0.156.0"),
    importWithoutOverwrite: z.boolean(),
    totpRequiredOnLogin: z.boolean(),
    totpConfigured: z.boolean(),
    apiKeyAuthEnabled: z.boolean(),
    hideUpstreamQuotaFromApiKeys: z.boolean().optional().default(false),
    limitWarmupEnabled: z.boolean().optional().default(false),
    limitWarmupWindows: LimitWarmupWindowsSchema.optional().default("both"),
    limitWarmupModel: LimitWarmupModelSchema.optional().default("auto"),
    limitWarmupPrompt: LimitWarmupPromptSchema.optional().default("Say OK."),
    limitWarmupCooldownSeconds: z.number().int().min(60).optional().default(3600),
    limitWarmupExhaustedThresholdPercent: z
      .number()
      .positive()
      .max(100)
      .optional()
      .default(99),
    limitWarmupIdleThresholdPercent: z
      .number()
      .positive()
      .max(100)
      .optional()
      .default(1),
    limitWarmupMinAvailablePercent: z
      .number()
      .positive()
      .max(100)
      .optional()
      .default(100),
    weeklyPaceWorkingDays: WeeklyPaceWorkingDaysSchema,
    weeklyPaceSmoothingMinutes: WeeklyPaceSmoothingMinutesSchema.optional().default(30),
    limitWarmupStaggeredIdleEnabled: z.boolean().optional().default(false),
    requestLogRetentionDays: z.number().int().min(0).max(3650).optional().default(0),
    usageHistoryRetentionDays: z.number().int().min(0).max(3650).optional().default(0),
    requestLogRetentionOverrideDays: z.number().int().min(0).max(3650).nullable().optional().default(null),
    usageHistoryRetentionOverrideDays: z.number().int().min(0).max(3650).nullable().optional().default(null),
    version: z.number().int().min(1).optional(),
  })
  .transform((settings) => {
    const legacyProvided = settings.stickyReallocationBudgetThresholdPct !== undefined;
    const primaryProvided = settings.stickyReallocationPrimaryBudgetThresholdPct !== undefined;
    const secondaryProvided = settings.stickyReallocationSecondaryBudgetThresholdPct !== undefined;
    const primaryThreshold =
      settings.stickyReallocationPrimaryBudgetThresholdPct ??
      settings.stickyReallocationBudgetThresholdPct ??
      95;
    return {
      ...settings,
      stickyReallocationBudgetThresholdPct:
        settings.stickyReallocationBudgetThresholdPct ?? primaryThreshold,
      stickyReallocationPrimaryBudgetThresholdPct: primaryThreshold,
      stickyReallocationSecondaryBudgetThresholdPct:
        settings.stickyReallocationSecondaryBudgetThresholdPct ??
        settings.stickyReallocationBudgetThresholdPct ??
        100,
      __stickyReallocationBudgetThresholdPctProvided: legacyProvided,
      __stickyReallocationPrimaryBudgetThresholdPctProvided: primaryProvided,
      __stickyReallocationSecondaryBudgetThresholdPctProvided: secondaryProvided,
    };
  });

export const SettingsUpdateRequestSchema = z
  .object({
    expectedVersion: z.number().int().min(1).optional(),
    stickyThreadsEnabled: z.boolean().optional(),
    upstreamStreamTransport: UpstreamStreamTransportSchema.optional(),
    prohibitFastMode: z.boolean().optional(),
    httpDownstreamTransportPolicy: HttpDownstreamTransportPolicySchema.optional(),
    preferEarlierResetAccounts: z.boolean().optional(),
    preferEarlierResetWindow: z.enum(["primary", "secondary"]).optional(),
    showResetCreditBadges: z.boolean().optional(),
    showResetCreditExpiryBadge: z.boolean().optional(),
    routingStrategy: RoutingStrategySchema.optional(),
    relativeAvailabilityPower: z.number().positive().optional(),
    relativeAvailabilityTopK: z.number().int().min(1).max(20).optional(),
    singleAccountId: z.string().nullable().optional(),
    proxyAccountResponseCreateLimit: z.number().int().min(0).nullable().optional(),
    proxyAccountStreamLimit: z.number().int().min(0).nullable().optional(),
    proxyAccountStreamRecoveryReserve: z.number().int().min(0).nullable().optional(),
    proxyApiKeyFairShareCongestionThresholdPct: z.number().int().min(0).max(100).nullable().optional(),
    openaiCacheAffinityMaxAgeSeconds: z.number().int().positive().optional(),
    dashboardSessionTtlSeconds: z.number().int().min(3600).optional(),
    stickyReallocationBudgetThresholdPct: z.number().min(0).max(100).optional(),
    stickyReallocationPrimaryBudgetThresholdPct: z.number().min(0).max(100).optional(),
    stickyReallocationSecondaryBudgetThresholdPct: z.number().min(0).max(100).optional(),
    additionalQuotaRoutingPolicies: z
      .record(z.string(), AdditionalQuotaRoutingPolicySchema)
      .optional(),
    warmupModel: z.string().trim().min(1).optional(),
    codexClientVersion: CodexClientVersionSchema.optional(),
    importWithoutOverwrite: z.boolean().optional(),
    totpRequiredOnLogin: z.boolean().optional(),
    apiKeyAuthEnabled: z.boolean().optional(),
    hideUpstreamQuotaFromApiKeys: z.boolean().optional(),
    limitWarmupEnabled: z.boolean().optional(),
    limitWarmupWindows: LimitWarmupWindowsSchema.optional(),
    limitWarmupModel: LimitWarmupModelSchema.optional(),
    limitWarmupPrompt: LimitWarmupPromptSchema.optional(),
    limitWarmupCooldownSeconds: z.number().int().min(60).optional(),
    limitWarmupExhaustedThresholdPercent: z.number().positive().max(100).optional(),
    limitWarmupIdleThresholdPercent: z.number().positive().max(100).optional(),
    limitWarmupMinAvailablePercent: z.number().positive().max(100).optional(),
    weeklyPaceWorkingDays: WeeklyPaceWorkingDaysValueSchema.optional(),
    weeklyPaceSmoothingMinutes: WeeklyPaceSmoothingMinutesSchema.optional(),
    limitWarmupStaggeredIdleEnabled: z.boolean().optional(),
    // Tri-state overrides: absent = unchanged, null = clear (inherit env
    // alias), value = store the override.
    requestLogRetentionOverrideDays: z.number().int().min(0).max(3650).nullable().optional(),
    usageHistoryRetentionOverrideDays: z.number().int().min(0).max(3650).nullable().optional(),
  })
  .superRefine((settings, ctx) => {
    if (
      settings.proxyAccountStreamLimit !== undefined &&
      settings.proxyAccountStreamLimit !== null &&
      settings.proxyAccountStreamLimit > 0 &&
      settings.proxyAccountStreamRecoveryReserve !== undefined &&
      settings.proxyAccountStreamRecoveryReserve !== null &&
      settings.proxyAccountStreamRecoveryReserve > settings.proxyAccountStreamLimit
    ) {
      ctx.addIssue({
        code: "custom",
        path: ["proxyAccountStreamRecoveryReserve"],
        message: "proxyAccountStreamRecoveryReserve must not exceed proxyAccountStreamLimit",
      });
    }
    if (
      settings.requestLogRetentionOverrideDays !== undefined &&
      settings.requestLogRetentionOverrideDays !== null &&
      settings.requestLogRetentionOverrideDays !== 0 &&
      settings.requestLogRetentionOverrideDays < 30
    ) {
      ctx.addIssue({
        code: "custom",
        path: ["requestLogRetentionOverrideDays"],
        message: "request_log_retention_override_days must be 0 (disabled) or >= 30",
      });
    }
    if (
      settings.usageHistoryRetentionOverrideDays !== undefined &&
      settings.usageHistoryRetentionOverrideDays !== null &&
      settings.usageHistoryRetentionOverrideDays !== 0 &&
      settings.usageHistoryRetentionOverrideDays < 45
    ) {
      ctx.addIssue({
        code: "custom",
        path: ["usageHistoryRetentionOverrideDays"],
        message: "usage_history_retention_override_days must be 0 (disabled) or >= 45",
      });
    }
  });

type ParsedDashboardSettings = z.infer<typeof DashboardSettingsSchema>;
type StickyThresholdPresenceFlags = Pick<
  ParsedDashboardSettings,
  | "__stickyReallocationBudgetThresholdPctProvided"
  | "__stickyReallocationPrimaryBudgetThresholdPctProvided"
  | "__stickyReallocationSecondaryBudgetThresholdPctProvided"
>;
type StickyThresholdValues = Pick<
  ParsedDashboardSettings,
  | "stickyReallocationBudgetThresholdPct"
  | "stickyReallocationPrimaryBudgetThresholdPct"
  | "stickyReallocationSecondaryBudgetThresholdPct"
>;

export type DashboardSettings = Omit<
  ParsedDashboardSettings,
  keyof StickyThresholdPresenceFlags | keyof StickyThresholdValues
> &
  Partial<StickyThresholdPresenceFlags> &
  Partial<StickyThresholdValues>;
export type SettingsUpdateRequest = z.infer<typeof SettingsUpdateRequestSchema>;
export type AdditionalQuotaRoutingPolicy = z.infer<typeof AdditionalQuotaRoutingPolicySchema>;
