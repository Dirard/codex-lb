import type { ApiKey } from "@/features/api-keys/schemas";
import { localDateISO, periodDays, shiftISODate, type KeyPagePeriod } from "@/features/key-page/period";
import {
  KeyPageResponseSchema,
  KeyRequestsResponseSchema,
  type KeyPageResponseJson,
  type KeyRequestsResponseJson,
} from "@/features/key-page/schemas";
import { createShellStatus, createWindowHealth } from "@/test/mocks/overview-factories";

type Scope = "admin" | "owner";
type RequestRowJson = NonNullable<KeyPageResponseJson["recentRequests"]>[number];
type DayJson = NonNullable<KeyPageResponseJson["daily"]>[number];

const NOW = Date.now();
const iso = (offsetMinutes: number) => new Date(NOW + offsetMinutes * 60_000).toISOString();

/** Fields the owner scope never receives (R26). */
const ADMIN_REQUEST_FIELDS = [
  "accountId", "planType", "clientIp", "conversationId", "useragent", "errorMessage", "source", "modelSourceId", "modelSourceKind",
] as const;

/** One recent request; every tenth request from the fourth failed. */
export function createKeyRequestRow(index: number, keyId = "key_1", overrides: Partial<RequestRowJson> = {}): RequestRowJson {
  const failed = index % 10 === 3;
  return {
    requestedAt: iso(-index),
    requestId: `req_${keyId}_${String(index).padStart(4, "0")}`,
    requestKind: "normal",
    model: index % 2 === 0 ? "gpt-5.1" : "gpt-4o-mini",
    reasoningEffort: index % 2 === 0 ? "high" : null,
    status: failed ? "error" : "success",
    errorCode: failed ? "rate_limit_exceeded" : null,
    inputTokens: 1200,
    outputTokens: 300,
    cachedInputTokens: 400,
    reasoningTokens: 50,
    costUsd: 0.0125,
    latencyMs: 2400,
    latencyFirstTokenMs: 640,
    latencyQueueMs: 12,
    transport: "http",
    serviceTier: null,
    useragentGroup: "codex-cli",
    accountId: "acc_primary",
    planType: "plus",
    clientIp: "203.0.113.10",
    conversationId: `conv_${index}`,
    useragent: "codex-cli/1.2.3",
    errorMessage: failed ? "Rate limit exceeded" : null,
    source: null,
    modelSourceId: null,
    modelSourceKind: null,
    ...overrides,
  };
}

/** A request projected for a scope: the owner's has no account, address, conversation or message. */
export function scopeRequestRow(row: RequestRowJson, scope: Scope): RequestRowJson {
  if (scope === "admin") return row;
  const owner: Record<string, unknown> = { ...row };
  for (const field of ADMIN_REQUEST_FIELDS) delete owner[field];
  return owner as RequestRowJson;
}

/** The key's requests, newest first. */
export function createKeyRequestRows(count: number, keyId = "key_1"): RequestRowJson[] {
  return Array.from({ length: count }, (_, index) => createKeyRequestRow(index, keyId));
}

/**
 * One page of requests by offset cursor, as the mock server pages them; the
 * errors-only page keeps the failed requests.
 */
export function pageKeyRequests(
  rows: RequestRowJson[],
  { cursor, limit, errorsOnly }: { cursor: string | null; limit: number; errorsOnly: boolean },
  scope: Scope,
): KeyRequestsResponseJson {
  const source = errorsOnly ? rows.filter((row) => !["success", "completed", "cancelled"].includes(row.status)) : rows;
  const offset = cursor ? Number.parseInt(cursor, 10) || 0 : 0;
  const items = source.slice(offset, offset + limit).map((row) => scopeRequestRow(row, scope));
  const next = offset + limit < source.length ? String(offset + limit) : null;
  const page = { items, nextCursor: next };
  KeyRequestsResponseSchema.parse(page);
  return page;
}

/** The validated period of a key page response for local days. */
export function keyPagePeriod(period: KeyPagePeriod, timezone = "UTC"): KeyPageResponseJson["period"] {
  const days = periodDays(period);
  const today = localDateISO();
  return {
    start: period.from,
    end: period.to,
    timezone,
    days,
    previousStart: shiftISODate(period.from, -days),
    previousEnd: shiftISODate(period.from, -1),
    includesToday: period.from <= today && today <= period.to,
  };
}

function createDay(date: string, active: boolean): DayJson {
  if (!active) {
    return {
      date, requests: 0, inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, costUsd: 0, errors: 0, cancelled: 0,
      byModel: [], ttftP50Ms: null, ttftP95Ms: null, totalP50Ms: null, totalP95Ms: null, queueP95Ms: null, tpsP50: null,
    };
  }
  return {
    date,
    requests: 20,
    inputTokens: 24_000,
    outputTokens: 6_000,
    cachedInputTokens: 8_000,
    costUsd: 0.25,
    errors: 2,
    cancelled: 0,
    byModel: [
      { model: "gpt-5.1", other: false, requests: 12, tokens: 20_000, costUsd: 0.18 },
      { model: "gpt-4o-mini", other: false, requests: 6, tokens: 8_000, costUsd: 0.05 },
      { model: "", other: true, requests: 2, tokens: 2_000, costUsd: 0.02 },
    ],
    ttftP50Ms: 640,
    ttftP95Ms: 1_900,
    totalP50Ms: 2_400,
    totalP95Ms: 7_800,
    queueP95Ms: 15,
    tpsP50: 42.5,
  };
}

function limitsOf(apiKey: ApiKey): NonNullable<KeyPageResponseJson["limits"]> {
  return apiKey.limits.map((limit) => {
    const percent = limit.maxValue > 0 ? (limit.currentValue / limit.maxValue) * 100 : 0;
    return {
      type: limit.limitType,
      window: limit.limitWindow,
      modelFilter: limit.modelFilter,
      current: limit.currentValue,
      max: limit.maxValue,
      percent,
      resetAt: limit.resetAt,
      expired: false,
      pacePerHour: null,
      projectedExhaustionAt: null,
      paceBasis: "last 3 h",
    };
  });
}

function statusOf(apiKey: ApiKey): "active" | "disabled" | "expired" {
  if (!apiKey.isActive) return "disabled";
  if (apiKey.expiresAt && new Date(apiKey.expiresAt).getTime() <= Date.now()) return "expired";
  return "active";
}

export type KeyPageOptions = {
  scope?: Scope;
  period?: KeyPagePeriod;
  timezone?: string;
  groupName?: string | null;
  requests?: RequestRowJson[];
  overrides?: Partial<KeyPageResponseJson>;
};

/** The key page of a key for a period; the owner variant omits every administrator-only field. */
export function createKeyPage(apiKey: ApiKey, options: KeyPageOptions = {}): KeyPageResponseJson {
  const scope = options.scope ?? "admin";
  const today = localDateISO();
  const period = options.period ?? { from: shiftISODate(today, -6), to: today };
  const days: string[] = [];
  for (let day = period.from; day <= period.to; day = shiftISODate(day, 1)) days.push(day);
  // Traffic falls on the last seven days ending today.
  const activeFrom = shiftISODate(today, -6);
  const daily = days.map((day) => createDay(day, day >= activeFrom && day <= today));
  const requests = daily.reduce((sum, day) => sum + day.requests, 0);
  const rows = options.requests ?? createKeyRequestRows(60, apiKey.id);
  const recent = pageKeyRequests(rows, { cursor: null, limit: 50, errorsOnly: false }, scope);
  const response: KeyPageResponseJson = {
    scope,
    generatedAt: iso(0),
    shell: createShellStatus(),
    key: {
      id: apiKey.id,
      name: apiKey.name,
      prefix: apiKey.keyPrefix,
      status: statusOf(apiKey),
      groupId: apiKey.groupId,
      groupName: apiKey.groupId ? options.groupName ?? apiKey.groupId : null,
      createdAt: apiKey.createdAt,
      lastUsedAt: apiKey.lastUsedAt,
      expiresAt: apiKey.expiresAt,
      allowedModels: apiKey.allowedModels,
      enforcedModel: apiKey.enforcedModel,
      enforcedReasoningEffort: apiKey.enforcedReasoningEffort,
      allowedReasoningEfforts: apiKey.allowedReasoningEfforts,
      trafficClass: apiKey.trafficClass,
      serviceTier: apiKey.enforcedServiceTier,
      usageSections: apiKey.usageSections.split(",").filter(Boolean),
      accountAssignmentScopeEnabled: apiKey.accountAssignmentScopeEnabled,
      sourceAssignmentScopeEnabled: apiKey.sourceAssignmentScopeEnabled,
      assignedAccounts: apiKey.assignedAccountIds.map((id) => ({ id, label: id })),
      assignedAccountCount: apiKey.assignedAccountIds.length,
      assignedSourceCount: apiKey.assignedSourceIds.length,
    },
    period: keyPagePeriod(period, options.timezone),
    rollupsUsable: true,
    detailsFrom: null,
    limits: limitsOf(apiKey),
    pool: {
      scope: apiKey.groupId ? "group" : "pool",
      accountCount: 2,
      primary: createWindowHealth(),
      secondary: createWindowHealth({ remainingPercent: 80 }),
      purchasedCredits: { remaining: 12.5, unlimited: false, knownAccountCount: 2 },
    },
    group: null,
    summary: {
      requests,
      inputTokens: requests * 1_200,
      outputTokens: requests * 300,
      cachedInputTokens: requests * 400,
      reasoningTokens: requests * 50,
      cacheShare: requests > 0 ? 1 / 3 : null,
      costUsd: requests > 0 ? 1.75 : 0,
      errors: requests > 0 ? 14 : 0,
      errorRate: requests > 0 ? 0.1 : null,
      cancelled: 0,
      ttftP50Ms: requests > 0 ? 640 : null,
      ttftP95Ms: requests > 0 ? 1_900 : null,
      totalP50Ms: requests > 0 ? 2_400 : null,
      totalP95Ms: requests > 0 ? 7_800 : null,
      previous: requests > 0 ? { requests: 100, tokens: 150_000, costUsd: 1.25 } : null,
    },
    daily,
    byModel: requests > 0
      ? [
          { model: "gpt-5.1", requests: 84, inputTokens: 100_800, outputTokens: 25_200, cachedInputTokens: 33_600, costUsd: 1.26, errors: 7, share: 72, ttftP50Ms: 700 },
          { model: "gpt-4o-mini", requests: 56, inputTokens: 67_200, outputTokens: 16_800, cachedInputTokens: 22_400, costUsd: 0.49, errors: 7, share: 28, ttftP50Ms: 420 },
        ]
      : [],
    errorsByCode: requests > 0
      ? [
          { code: "rate_limit_exceeded", count: 12, share: 85.7, lastSeenAt: iso(-60) },
          { code: "", count: 2, share: 14.3, lastSeenAt: null },
        ]
      : [],
    userAgents: requests > 0 ? [{ group: "codex-cli", requests, costUsd: 1.75, share: 100 }] : [],
    byAccount: requests > 0
      ? [{ accountId: "acc_primary", label: "primary@example.com", deleted: false, requests, costUsd: 1.75, share: 100 }]
      : [],
    lifetime: {
      requestCount: 1_500,
      failedCount: 40,
      usage: { inputTokens: 1_800_000, outputTokens: 450_000, cachedInputTokens: 600_000, reasoningTokens: 75_000, costMicrodollars: 12_340_000 },
    },
    recentRequests: recent.items,
    nextCursor: recent.nextCursor,
    ...options.overrides,
  };
  if (scope === "owner") {
    delete response.shell;
    delete response.byAccount;
    delete response.lifetime;
    delete response.key.assignedAccounts;
  }
  KeyPageResponseSchema.parse(response);
  return response;
}

/** The owner's page: no shell, cost by account, lifetime or account identities. */
export function createOwnerKeyPage(apiKey: ApiKey, options: Omit<KeyPageOptions, "scope"> = {}): KeyPageResponseJson {
  return createKeyPage(apiKey, { ...options, scope: "owner" });
}
