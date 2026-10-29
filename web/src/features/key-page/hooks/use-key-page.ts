import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

import {
  fetchAdminKeyPage,
  fetchOwnerKeyPage,
  keyPageQueryKey,
} from "@/features/key-page/api";
import {
  includesToday,
  periodFromSearch,
  searchWithPeriod,
  type KeyPagePeriod,
} from "@/features/key-page/period";
import type { KeyPageMode } from "@/features/key-page/schemas";
import { ApiError } from "@/lib/api-client";

/** The key page polls every 60 s, only while its period includes today (R24). */
export const KEY_PAGE_POLL_MS = 60_000;

/** The period of the URL, the page's only period control (R24). */
export function useKeyPagePeriod(): { period: KeyPagePeriod; setPeriod: (period: KeyPagePeriod) => void } {
  const [params, setParams] = useSearchParams();
  const period = useMemo(() => periodFromSearch(params), [params]);
  const setPeriod = useCallback(
    (next: KeyPagePeriod) => setParams((current) => searchWithPeriod(current, next)),
    [setParams],
  );
  return { period, setPeriod };
}

function retryable(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false;
  return failureCount < 1;
}

/**
 * The key page's only periodic request: every 60 s while the tab is visible
 * and the period includes today, otherwise read once. Previous data stays
 * visible while a refetch runs or fails.
 */
export function useKeyPage(mode: KeyPageMode, keyId: string | undefined, period: KeyPagePeriod, timezone: string) {
  const queryClient = useQueryClient();
  const live = includesToday(period);
  return useQuery({
    queryKey: keyPageQueryKey(mode, keyId, period, timezone),
    queryFn: ({ signal }) =>
      mode === "admin"
        ? fetchAdminKeyPage(queryClient, keyId ?? "", period, timezone, signal)
        : fetchOwnerKeyPage(period, timezone, signal),
    enabled: mode === "owner" || Boolean(keyId),
    refetchInterval: live ? KEY_PAGE_POLL_MS : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: live,
    placeholderData: keepPreviousData,
    staleTime: live ? 10_000 : 5 * 60_000,
    retry: mode === "owner" ? false : retryable,
  });
}

export function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

export function isNotFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}
