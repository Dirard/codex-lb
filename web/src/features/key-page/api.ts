import type { QueryClient } from "@tanstack/react-query";

import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { get } from "@/lib/api-client";
import { invalidateEntityViews } from "@/lib/invalidate-entity-views";
import type { KeyPagePeriod } from "@/features/key-page/period";
import {
  KeyPageResponseSchema,
  KeyRequestsResponseSchema,
  type KeyPageMode,
  type KeyPageResponse,
} from "@/features/key-page/schemas";

export const KEY_PAGE_QUERY_KEY = ["key-page"] as const;

/** One page of recent requests; the server allows at most 100. */
export const KEY_REQUESTS_PAGE_SIZE = 50;

/** The owner routes use only the report-session cookie and are never cached. */
const ownerOptions = { credentials: "same-origin", cache: "no-store", suppressUnauthorizedHandler: true } as const;

export type KeyRequestsQuery = {
  cursor: string | null;
  errorsOnly: boolean;
  limit?: number;
};

function pageParams(period: KeyPagePeriod, timezone: string): URLSearchParams {
  return new URLSearchParams({ start_date: period.from, end_date: period.to, timezone });
}

function requestParams(query: KeyRequestsQuery): URLSearchParams {
  const params = new URLSearchParams();
  if (query.cursor) params.set("cursor", query.cursor);
  params.set("limit", String(query.limit ?? KEY_REQUESTS_PAGE_SIZE));
  params.set("errors_only", query.errorsOnly ? "true" : "false");
  return params;
}

function keyPath(keyId: string): string {
  return `/api/api-keys/${encodeURIComponent(keyId)}`;
}

/**
 * The administrator key page. Its shell status goes into the shared shell
 * query, which the header and the status bar read, so the page needs no
 * other periodic request while it polls.
 */
export async function fetchAdminKeyPage(
  queryClient: QueryClient,
  keyId: string,
  period: KeyPagePeriod,
  timezone: string,
  signal?: AbortSignal,
): Promise<KeyPageResponse> {
  const response = await get(`${keyPath(keyId)}/page?${pageParams(period, timezone)}`, KeyPageResponseSchema, { signal });
  if (response.shell) queryClient.setQueryData(SHELL_STATUS_QUERY_KEY, response.shell);
  return response;
}

/** The key owner's page, bound by the server to the session's key. */
export function fetchOwnerKeyPage(period: KeyPagePeriod, timezone: string, signal?: AbortSignal): Promise<KeyPageResponse> {
  return get(`/api/key-reports/page?${pageParams(period, timezone)}`, KeyPageResponseSchema, { ...ownerOptions, signal });
}

export function fetchAdminKeyRequests(keyId: string, query: KeyRequestsQuery, signal?: AbortSignal) {
  return get(`${keyPath(keyId)}/requests?${requestParams(query)}`, KeyRequestsResponseSchema, { signal });
}

export function fetchOwnerKeyRequests(query: KeyRequestsQuery, signal?: AbortSignal) {
  return get(`/api/key-reports/requests?${requestParams(query)}`, KeyRequestsResponseSchema, { ...ownerOptions, signal });
}

/** The query key of one scope's page; the owner's has no key id. */
export function keyPageQueryKey(mode: KeyPageMode, keyId: string | undefined, period: KeyPagePeriod, timezone: string) {
  return [...KEY_PAGE_QUERY_KEY, mode, mode === "admin" ? keyId ?? "" : "session", period.from, period.to, timezone] as const;
}

export function keyRequestsQueryKey(mode: KeyPageMode, keyId: string | undefined) {
  return [...KEY_PAGE_QUERY_KEY, mode, mode === "admin" ? keyId ?? "" : "session", "requests"] as const;
}

/**
 * After a key mutation: the key page, the key lists, and what the overview,
 * the directory and the shell status show.
 */
export function invalidateKeyViews(queryClient: QueryClient): Promise<unknown> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: KEY_PAGE_QUERY_KEY }),
    queryClient.invalidateQueries({ queryKey: ["api-keys"] }),
    invalidateEntityViews(queryClient),
  ]);
}
