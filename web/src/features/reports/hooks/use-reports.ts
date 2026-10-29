import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { getReports } from "../api";
import { localDateISO } from "../date";

/** The traffic-details filter of GET /api/reports; no key ids means every key. */
export type TrafficReportParams = {
  startDate: string;
  endDate: string;
  timezone?: string;
  apiKeyIds: string[];
};

/**
 * Model, user-agent and latency details from GET /api/reports, loaded only
 * while enabled (the traffic details are expanded). Like the key report it
 * never polls and refetches on focus only for a period including today.
 */
export function useReports(params: TrafficReportParams, enabled: boolean) {
  const includesToday = params.endDate >= localDateISO();
  return useQuery({
    enabled,
    queryKey: ["reports", "traffic", params],
    queryFn: ({ signal }) =>
      getReports(
        {
          startDate: params.startDate,
          endDate: params.endDate,
          apiKeyId: params.apiKeyIds.length > 0 ? params.apiKeyIds : undefined,
          timezone: params.timezone,
        },
        signal,
      ),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
    refetchOnWindowFocus: includesToday,
    refetchInterval: false,
  });
}
