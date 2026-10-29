import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";

import { getDashboardProjections } from "@/features/dashboard/api";
import { fetchGroupsSummary, GROUPS_SUMMARY_QUERY_KEY } from "@/features/groups/api";
import { browserTimeZone } from "@/features/overview/api";

export const GROUPS_SUMMARY_POLL_MS = 60_000;

/** The Groups page's only periodic request: every 60 s while the tab is
 * visible, keeping the previous data while a refetch runs or fails. */
export function useGroupsSummary() {
  const queryClient = useQueryClient();
  const timezone = useMemo(() => browserTimeZone(), []);
  const query = useQuery({
    queryKey: [...GROUPS_SUMMARY_QUERY_KEY, timezone],
    queryFn: () => fetchGroupsSummary(queryClient, timezone),
    refetchInterval: GROUPS_SUMMARY_POLL_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  });
  return { ...query, timezone };
}

/** The weekly credits pace behind the pool runway card, requested once per
 * visit of the page: it is not a periodic request. */
export function usePoolRunway() {
  return useQuery({
    queryKey: ["groups", "pool-runway"],
    queryFn: getDashboardProjections,
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  });
}
