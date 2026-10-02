import { del, get, post } from "@/lib/api-client";
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
        nextResetAt: z.iso.datetime({ offset: true }).nullable().optional(),
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
const SessionSchema = z.object({ authenticated: z.boolean() });
const sessionOptions = { credentials: "same-origin", cache: "no-store", suppressUnauthorizedHandler: true } as const;

export function getKeyReportSession(signal?: AbortSignal) {
  return get("/api/key-reports/session", SessionSchema, { ...sessionOptions, signal });
}

export function createKeyReportSession(apiKey: string, signal?: AbortSignal) {
  return post("/api/key-reports/session", SessionSchema.extend({ authenticated: z.literal(true) }), {
    ...sessionOptions, headers: { Authorization: `Bearer ${apiKey}` }, signal,
  });
}

export function deleteKeyReportSession() {
  return del("/api/key-reports/session", SessionSchema.extend({ authenticated: z.literal(false) }), sessionOptions);
}

export function getKeyReport(filters: KeyReportFilters, signal?: AbortSignal) {
  const params = new URLSearchParams({ start_date: filters.startDate, end_date: filters.endDate });
  if (filters.timezone) params.set("timezone", filters.timezone);
  if (filters.model.trim()) params.set("model", filters.model.trim());
  return get(`/api/key-reports/reports?${params}`, KeyReportsSchema, { ...sessionOptions, signal });
}
