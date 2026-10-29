import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { getKeyUsageReport, type KeyUsageReportParams } from "../api";
import { localDateISO } from "../date";

export const KEY_USAGE_REPORT_STALE_MS = 60_000;

export function keyUsageReportQueryKey(params: KeyUsageReportParams) {
  return ["reports", "keys", params] as const;
}

/**
 * The Reports page's report (R32): no interval polling; it refetches on
 * window focus only when the period includes today and the data are older
 * than 60 s, and keeps the previous report while a new one loads or fails.
 */
export function useKeyUsageReport(params: KeyUsageReportParams, enabled = true) {
  const includesToday = params.endDate >= localDateISO();
  return useQuery({
    queryKey: keyUsageReportQueryKey(params),
    queryFn: ({ signal }) => getKeyUsageReport(params, signal),
    enabled,
    placeholderData: keepPreviousData,
    staleTime: KEY_USAGE_REPORT_STALE_MS,
    refetchOnWindowFocus: includesToday,
    refetchInterval: false,
  });
}
