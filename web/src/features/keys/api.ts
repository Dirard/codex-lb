import type { QueryClient } from "@tanstack/react-query";

import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import type { LimitRuleCreate } from "@/features/api-keys/schemas";
import {
  KEY_SORTS,
  KEY_STATUSES,
  KeyBulkResponseSchema,
  KeyIdsSchema,
  KeysPageSchema,
  type KeyBulkAction,
  type KeyBulkResponse,
  type KeyIds,
  type KeySort,
  type KeyStatus,
  type KeysPage,
} from "@/features/keys/schemas";
import { get, post } from "@/lib/api-client";

export const KEYS_QUERY_KEY = ["keys"] as const;

/** Rows per page of the keys table (R35). */
export const KEYS_PAGE_SIZE = 50;

/** The inactivity choices of the toolbar; the server accepts 1 to 365 days. */
export const INACTIVE_DAY_CHOICES = [7, 30, 90] as const;

/** "Expiring soon" of the toolbar and of the overview's link. */
export const EXPIRING_DAYS = 7;

export type KeyOrder = "asc" | "desc";

/** The search and filters of the keys page, under the names of GET /api/keys. */
export type KeysFilters = {
  q: string;
  /** A group id, "ungrouped", or null for any group. */
  group: string | null;
  status: KeyStatus | null;
  nearLimit: boolean;
  expiringDays: number | null;
  inactiveDays: number | null;
};

export type KeysListParams = KeysFilters & {
  sort: KeySort;
  /** Null is the server's default: ascending by name, descending otherwise. */
  order: KeyOrder | null;
  offset: number;
};

export const DEFAULT_KEYS_PARAMS: KeysListParams = {
  q: "",
  group: null,
  status: null,
  nearLimit: false,
  expiringDays: null,
  inactiveDays: null,
  sort: "name",
  order: null,
  offset: 0,
};

function dayCount(value: string | null): number | null {
  if (!value || !/^\d{1,3}$/.test(value)) return null;
  const days = Number(value);
  return days >= 1 && days <= 365 ? days : null;
}

/** The keys page state of a URL; unknown or invalid values fall back to their defaults. */
export function keysParamsFromSearch(search: URLSearchParams): KeysListParams {
  const sort = search.get("sort");
  const order = search.get("order");
  const status = search.get("status");
  const offset = search.get("offset");
  const nearLimit = search.get("near_limit");
  return {
    q: search.get("q") ?? "",
    group: search.get("group") || null,
    status: KEY_STATUSES.includes(status as KeyStatus) ? (status as KeyStatus) : null,
    nearLimit: nearLimit === "1" || nearLimit === "true",
    expiringDays: dayCount(search.get("expiring_days")),
    inactiveDays: dayCount(search.get("inactive_days")),
    sort: KEY_SORTS.includes(sort as KeySort) ? (sort as KeySort) : "name",
    order: order === "asc" || order === "desc" ? order : null,
    offset: offset && /^\d{1,7}$/.test(offset) ? Number(offset) : 0,
  };
}

/** The filter and sort parameters shared by the page, its ids and its URL; defaults are left out. */
function filterSearch(params: KeysListParams): URLSearchParams {
  const search = new URLSearchParams();
  if (params.q.trim()) search.set("q", params.q.trim());
  if (params.group) search.set("group", params.group);
  if (params.status) search.set("status", params.status);
  if (params.nearLimit) search.set("near_limit", "1");
  if (params.expiringDays) search.set("expiring_days", String(params.expiringDays));
  if (params.inactiveDays) search.set("inactive_days", String(params.inactiveDays));
  if (params.sort !== "name") search.set("sort", params.sort);
  if (params.order) search.set("order", params.order);
  return search;
}

/** The URL query of the keys page state. */
export function keysSearch(params: KeysListParams): URLSearchParams {
  const search = filterSearch(params);
  if (params.offset > 0) search.set("offset", String(params.offset));
  return search;
}

export function hasKeyFilters(params: KeysFilters): boolean {
  return Boolean(params.q.trim() || params.group || params.status || params.nearLimit || params.expiringDays || params.inactiveDays);
}

export function keysQueryKey(params: KeysListParams, timezone: string) {
  return [...KEYS_QUERY_KEY, "page", keysSearch(params).toString(), timezone] as const;
}

/**
 * One page of the keys list. Its shell status goes into the shared shell
 * query, which the header and the status bar read, so the page needs no
 * other periodic request.
 */
export async function fetchKeysPage(
  queryClient: QueryClient,
  params: KeysListParams,
  timezone: string,
  signal?: AbortSignal,
): Promise<KeysPage> {
  const search = keysSearch(params);
  search.set("limit", String(KEYS_PAGE_SIZE));
  search.set("timezone", timezone);
  const page = await get(`/api/keys?${search}`, KeysPageSchema, { signal });
  queryClient.setQueryData(SHELL_STATUS_QUERY_KEY, page.shell);
  return page;
}

/** The ids of every matching key in list order, at most 1 000, for "Select all matching". */
export function fetchKeyIds(params: KeysListParams, timezone: string): Promise<KeyIds> {
  const search = filterSearch(params);
  search.set("timezone", timezone);
  return get(`/api/keys/ids?${search}`, KeyIdsSchema);
}

export type KeyBulkRequest =
  | { ids: string[]; action: Extract<KeyBulkAction, "disable" | "enable"> }
  | { ids: string[]; action: "move_group"; groupId: string | null }
  | { ids: string[]; action: "set_limits"; limits: LimitRuleCreate[] };

export function applyKeysBulk(body: KeyBulkRequest): Promise<KeyBulkResponse> {
  return post("/api/keys/bulk", KeyBulkResponseSchema, { body });
}
