import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";

import { browserTimeZone, fetchOverview, OVERVIEW_QUERY_KEY } from "@/features/overview/api";

export const OVERVIEW_POLL_MS = 30_000;

/** The overview page's only periodic request: every 30 s while visible and on
 * focus, keeping the previous data while a refetch runs or fails. */
export function useOverview() {
  const queryClient = useQueryClient();
  const timezone = useMemo(() => browserTimeZone(), []);
  const query = useQuery({
    queryKey: [...OVERVIEW_QUERY_KEY, timezone],
    queryFn: () => fetchOverview(queryClient, timezone),
    refetchInterval: OVERVIEW_POLL_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  });
  return { ...query, timezone };
}
