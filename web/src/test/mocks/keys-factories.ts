import type { AccountGroup } from "@/features/account-groups/schemas";
import type { ApiKey, LimitRuleCreate } from "@/features/api-keys/schemas";
import {
  KEY_SORTS,
  KEY_STATUSES,
  KeyIdsSchema,
  KeysPageSchema,
  type KeyBulkResult,
  type KeyIds,
  type KeyRow,
  type KeySort,
  type KeysPage,
} from "@/features/keys/schemas";
import { createShellStatus } from "@/test/mocks/overview-factories";

const DAY_MS = 24 * 60 * 60 * 1000;
const PAGE_PARAMS = ["offset", "limit"];
const FILTER_PARAMS = ["q", "group", "status", "near_limit", "expiring_days", "inactive_days", "sort", "order", "timezone"];

function round6(value: number): number {
  return Math.round(value * 1e6) / 1e6;
}

function keyStatus(key: ApiKey, now: number): KeyRow["status"] {
  if (!key.isActive) return "disabled";
  if (key.expiresAt && new Date(key.expiresAt).getTime() <= now) return "expired";
  return "active";
}

/** A key row of GET /api/keys; spend is derived from the mock's lifetime cost. */
export function keyRowFromApiKey(key: ApiKey, groups: AccountGroup[], now = Date.now()): KeyRow {
  const lifetime = key.usageSummary?.totalCostUsd ?? 0;
  let maxLimit: KeyRow["maxLimit"] = null;
  for (const limit of key.limits) {
    if (limit.maxValue <= 0) continue;
    const percent = (limit.currentValue / limit.maxValue) * 100;
    if (!maxLimit || percent > maxLimit.percent) {
      maxLimit = { type: limit.limitType, window: limit.limitWindow, modelFilter: limit.modelFilter, percent: round6(percent), resetAt: limit.resetAt };
    }
  }
  return {
    id: key.id,
    name: key.name,
    prefix: key.keyPrefix,
    groupId: key.groupId ?? null,
    groupName: groups.find((group) => group.id === key.groupId)?.name ?? null,
    status: keyStatus(key, now),
    expiresAt: key.expiresAt,
    lastUsedAt: key.lastUsedAt,
    createdAt: key.createdAt,
    spendTodayUsd: round6(lifetime / 10),
    spend7dUsd: round6(lifetime / 2),
    spend30dUsd: round6(lifetime),
    maxLimit,
    limitsSummary: key.limits.map((limit) => ({
      type: limit.limitType,
      window: limit.limitWindow,
      modelFilter: limit.modelFilter,
      maxValue: limit.maxValue,
    })),
  };
}

type KeysQuery = { rows: KeyRow[]; offset: number; limit: number } | { error: string };

function days(value: string | null): number | null | undefined {
  if (value === null || value === "") return null;
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed >= 1 && parsed <= 365 ? parsed : undefined;
}

function sortValue(row: KeyRow, sort: KeySort): number | string | null {
  switch (sort) {
    case "name":
      return row.name.toLowerCase();
    case "created":
      return new Date(row.createdAt).getTime();
    case "spend_today":
      return row.spendTodayUsd;
    case "spend_7d":
      return row.spend7dUsd;
    case "spend_30d":
      return row.spend30dUsd;
    case "limit_fill":
      return row.maxLimit?.percent ?? null;
    case "last_used":
      return row.lastUsedAt ? new Date(row.lastUsedAt).getTime() : null;
  }
}

/**
 * The keys list of the Go server over the mock's keys: unknown or repeated
 * parameters and invalid values are refused, filters combine, rows are sorted
 * with keys lacking a value last and ties by id.
 */
function queryKeys(url: URL, keys: ApiKey[], groups: AccountGroup[], paged: boolean): KeysQuery {
  const names = [...url.searchParams.keys()];
  const allowed = paged ? [...FILTER_PARAMS, ...PAGE_PARAMS] : FILTER_PARAMS;
  if (names.some((name) => !allowed.includes(name)) || new Set(names).size !== names.length) return { error: "invalid_key_filters" };
  const params = url.searchParams;
  const sort = (params.get("sort") || "name") as KeySort;
  const order = params.get("order") || (sort === "name" ? "asc" : "desc");
  const status = params.get("status") || null;
  const nearLimit = params.get("near_limit") || "0";
  const expiring = days(params.get("expiring_days"));
  const inactive = days(params.get("inactive_days"));
  const limit = Number(params.get("limit") || "50");
  const offset = Number(params.get("offset") || "0");
  const q = (params.get("q") ?? "").trim().toLowerCase();
  if (
    !KEY_SORTS.includes(sort) || (order !== "asc" && order !== "desc") || (status && !KEY_STATUSES.includes(status as KeyRow["status"])) ||
    !["0", "1", "true", "false"].includes(nearLimit) || expiring === undefined || inactive === undefined ||
    !Number.isInteger(limit) || limit < 1 || limit > 100 || !Number.isInteger(offset) || offset < 0 || q.length > 256
  ) {
    return { error: "invalid_key_filters" };
  }
  const group = params.get("group") || null;
  const now = Date.now();
  const rows = keys
    .map((key) => keyRowFromApiKey(key, groups, now))
    .filter((row) => {
      if (q && !row.name.toLowerCase().includes(q) && !row.prefix.toLowerCase().includes(q)) return false;
      if (group === "ungrouped" ? row.groupId !== null : group !== null && row.groupId !== group) return false;
      if (status && row.status !== status) return false;
      if ((nearLimit === "1" || nearLimit === "true") && (row.status !== "active" || (row.maxLimit?.percent ?? 0) < 90)) return false;
      if (expiring) {
        const at = row.expiresAt ? new Date(row.expiresAt).getTime() : null;
        if (at === null || at <= now || at > now + expiring * DAY_MS) return false;
      }
      if (inactive && row.lastUsedAt && new Date(row.lastUsedAt).getTime() >= now - inactive * DAY_MS) return false;
      return true;
    })
    .sort((a, b) => {
      const av = sortValue(a, sort);
      const bv = sortValue(b, sort);
      if (av === null || bv === null) {
        if (av !== bv) return av === null ? 1 : -1;
      } else if (av !== bv) {
        return (av < bv ? -1 : 1) * (order === "asc" ? 1 : -1);
      }
      return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
    });
  return { rows, offset, limit };
}

export function createKeysPage(url: URL, keys: ApiKey[], groups: AccountGroup[]): KeysPage | { error: string } {
  const result = queryKeys(url, keys, groups, true);
  if ("error" in result) return result;
  return KeysPageSchema.parse({
    shell: createShellStatus(),
    generatedAt: new Date().toISOString(),
    timezone: url.searchParams.get("timezone") || "UTC",
    total: result.rows.length,
    offset: result.offset,
    limit: result.limit,
    rows: result.rows.slice(result.offset, result.offset + result.limit),
  });
}

export function createKeyIds(url: URL, keys: ApiKey[], groups: AccountGroup[]): KeyIds | { error: string } {
  const result = queryKeys(url, keys, groups, false);
  if ("error" in result) return result;
  return KeyIdsSchema.parse({ ids: result.rows.slice(0, 1000).map((row) => row.id), total: result.rows.length });
}

export type KeyBulkBody = {
  ids: string[];
  action: "disable" | "enable" | "move_group" | "set_limits";
  groupId?: string | null;
  limits?: LimitRuleCreate[];
};

/** Applies a bulk action to the mock keys like the Go server: per-key results in request order. */
export function applyKeyBulk(
  body: KeyBulkBody,
  keys: ApiKey[],
  groups: AccountGroup[],
  update: (key: ApiKey) => ApiKey,
): { keys: ApiKey[]; results: KeyBulkResult[] } | { error: string } {
  const move = body.action === "move_group";
  const limits = body.action === "set_limits";
  if (
    body.ids.length === 0 || body.ids.length > 1000 || new Set(body.ids).size !== body.ids.length ||
    !["disable", "enable", "move_group", "set_limits"].includes(body.action) ||
    move !== (body.groupId !== undefined) || limits !== (body.limits !== undefined)
  ) {
    return { error: "invalid_bulk_request" };
  }
  let next = keys;
  const results = body.ids.map((id): KeyBulkResult => {
    const key = next.find((candidate) => candidate.id === id);
    if (!key) return { id, ok: false, code: "not_found" };
    let changed: ApiKey;
    switch (body.action) {
      case "disable":
      case "enable":
        changed = { ...key, isActive: body.action === "enable" };
        break;
      case "move_group": {
        const group = body.groupId ? groups.find((candidate) => candidate.id === body.groupId) : null;
        if (body.groupId && !group) return { id, ok: false, code: "invalid_group" };
        changed = group
          ? {
              ...key,
              groupId: group.id,
              accountAssignmentScopeEnabled: true,
              limits: group.limits.map((rule, index) => ({
                id: 200 + index,
                limitType: rule.limitType,
                limitWindow: rule.limitWindow,
                maxValue: rule.maxValue,
                currentValue: 0,
                modelFilter: rule.modelFilter ?? null,
                resetAt: new Date(Date.now() + 7 * DAY_MS).toISOString(),
              })),
            }
          : { ...key, groupId: null };
        break;
      }
      case "set_limits":
        if (key.groupId) return { id, ok: false, code: "key_in_group" };
        changed = {
          ...key,
          limits: (body.limits ?? []).map((rule, index) => ({
            id: 300 + index,
            limitType: rule.limitType,
            limitWindow: rule.limitWindow,
            maxValue: rule.maxValue,
            currentValue: 0,
            modelFilter: rule.modelFilter ?? null,
            resetAt: new Date(Date.now() + 7 * DAY_MS).toISOString(),
          })),
        };
        break;
    }
    const saved = update(changed);
    next = next.map((candidate) => (candidate.id === id ? saved : candidate));
    return { id, ok: true };
  });
  return { keys: next, results };
}
