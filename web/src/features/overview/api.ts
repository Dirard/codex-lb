import type { QueryClient } from "@tanstack/react-query";

import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { get } from "@/lib/api-client";
import { OverviewResponseSchema, type OverviewResponse } from "@/features/overview/schemas";

export const OVERVIEW_QUERY_KEY = ["overview"] as const;

/** The browser's IANA time zone, or UTC when it cannot be determined. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/**
 * Fetches the overview for a time zone and writes its shell status into the
 * shared shell query, which the header and the status bar read; the overview
 * page therefore needs no other periodic request.
 */
export async function fetchOverview(queryClient: QueryClient, timezone: string): Promise<OverviewResponse> {
  const response = await get(`/api/overview?timezone=${encodeURIComponent(timezone)}`, OverviewResponseSchema);
  queryClient.setQueryData(SHELL_STATUS_QUERY_KEY, response.shell);
  return response;
}
