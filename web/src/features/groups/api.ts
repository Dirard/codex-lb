import type { QueryClient } from "@tanstack/react-query";

import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { GroupsSummarySchema, type GroupsSummary } from "@/features/groups/schemas";
import { get } from "@/lib/api-client";

export const GROUPS_SUMMARY_QUERY_KEY = ["groups", "summary"] as const;

/**
 * Fetches the groups summary for a time zone and writes its shell status into
 * the shared shell query, so the Groups page needs no other periodic request.
 */
export async function fetchGroupsSummary(queryClient: QueryClient, timezone: string): Promise<GroupsSummary> {
  const response = await get(`/api/groups/summary?timezone=${encodeURIComponent(timezone)}`, GroupsSummarySchema);
  queryClient.setQueryData(SHELL_STATUS_QUERY_KEY, response.shell);
  return response;
}
