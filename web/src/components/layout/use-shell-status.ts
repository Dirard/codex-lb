import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { useLocation } from "react-router-dom";

import {
  getShellStatus,
  pageCarriesShellStatus,
  SHELL_STATUS_POLL_MS,
  SHELL_STATUS_QUERY_KEY,
} from "@/components/layout/shell-status";

/**
 * Reads the shell status. On a page whose own poll carries it (the overview,
 * the keys page, and a key page whose period includes today) the query never fetches and
 * only shows what that poll wrote; elsewhere it is the page's one periodic
 * request, every 60 s while the tab is visible.
 */
export function useShellStatus() {
  const { pathname, search } = useLocation();
  const carried = pageCarriesShellStatus(pathname, search);
  return useQuery({
    queryKey: SHELL_STATUS_QUERY_KEY,
    queryFn: getShellStatus,
    enabled: !carried,
    refetchInterval: carried ? false : SHELL_STATUS_POLL_MS,
    refetchIntervalInBackground: false,
    staleTime: 30_000,
  });
}

/** Marks the shell status stale after a mutation that changes it. */
export function useInvalidateShellStatus() {
  const queryClient = useQueryClient();
  return useCallback(() => queryClient.invalidateQueries({ queryKey: SHELL_STATUS_QUERY_KEY }), [queryClient]);
}
