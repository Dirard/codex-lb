import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { getDashboardOverview, getDashboardProjections } from "@/features/dashboard/api";
import {
  DEFAULT_OVERVIEW_TIMEFRAME,
  type OverviewTimeframe,
} from "@/features/dashboard/schemas";
import { useDashboardPreferencesStore } from "@/hooks/use-dashboard-preferences";

export function useDashboard(timeframe: OverviewTimeframe = DEFAULT_OVERVIEW_TIMEFRAME) {
  const refreshSeconds = useDashboardPreferencesStore((state) => state.refreshSeconds);
  return useQuery({
    queryKey: ["dashboard", "overview", timeframe],
    queryFn: () => getDashboardOverview({ timeframe }),
    refetchInterval: refreshSeconds * 1_000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });
}

/** Upper bound for projection freshness between quota synchronizations. */
export const PROJECTIONS_MAX_AGE_MS = 5 * 60_000;

/**
 * Projections are computed from 30 days of persisted quota observations, so
 * they only change when the overview reports a newer `lastSyncAt`. Keying the
 * query on it refetches exactly once per new observation; the 5-minute bound
 * keeps time-relative depletion estimates current between synchronizations.
 */
export function useDashboardProjections(lastSyncAt: string | null | undefined, enabled = true) {
  return useQuery({
    queryKey: ["dashboard", "projections", lastSyncAt ?? null],
    queryFn: getDashboardProjections,
    enabled,
    placeholderData: keepPreviousData,
    staleTime: PROJECTIONS_MAX_AGE_MS,
    refetchInterval: PROJECTIONS_MAX_AGE_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });
}
