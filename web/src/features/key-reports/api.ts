import { get } from "@/lib/api-client";
import { ReportsResponseSchema } from "@/features/reports/schemas";
import { ApiKeySchema } from "@/features/api-keys/schemas";
import { z } from "zod";

export const KeyReportsSchema = ReportsResponseSchema.omit({ byAccount: true }).extend({
  limits: ApiKeySchema.shape.limits,
  group: z.object({
    name: z.string(),
    accountQuota: z.object({
      accountCount: z.number().int().nonnegative(),
      purchasedCredits: z.number().nonnegative().nullable().default(null),
      creditsUnlimited: z.boolean().default(false),
      creditsKnownAccountCount: z.number().int().nonnegative().default(0),
      windows: z.array(z.object({
        window: z.enum(["primary", "secondary", "monthly"]),
        usedPercent: z.number().min(0).max(100),
        accountCount: z.number().int().positive(),
      })),
    }).nullable().default(null),
    keys: z.array(ApiKeySchema.pick({ id: true, name: true, isActive: true, expiresAt: true, limits: true })
      .extend({ isCurrent: z.boolean() })),
  }).nullable().default(null),
});
export type KeyReport = z.infer<typeof KeyReportsSchema>;
export type KeyReportFilters = {
  startDate: string;
  endDate: string;
  timezone?: string;
  model: string;
};
export type KeyReportSession = { apiKey: string; report: KeyReport; filters: KeyReportFilters };

export function getKeyReport(apiKey: string, filters: KeyReportFilters, signal?: AbortSignal) {
  const params = new URLSearchParams({ start_date: filters.startDate, end_date: filters.endDate });
  if (filters.timezone) params.set("timezone", filters.timezone);
  if (filters.model.trim()) params.set("model", filters.model.trim());
  return get(`/v1/usage/reports?${params}`, KeyReportsSchema, {
    headers: { Authorization: `Bearer ${apiKey}` }, signal,
    credentials: "omit", cache: "no-store", suppressUnauthorizedHandler: true,
  });
}
