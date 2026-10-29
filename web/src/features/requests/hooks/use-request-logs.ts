import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { useSearchParams } from "react-router-dom";

import {
  getRequestLogOptions,
  getRequestLogs,
  type RequestLogFacetFilters,
  type RequestLogsListFilters,
} from "@/features/dashboard/api";
import {
  DEFAULT_REQUESTS_TIMEFRAME,
  FilterStateSchema,
  parseRequestsTimeframe,
  type FilterState,
} from "@/features/dashboard/schemas";

export const LIVE_REFRESH_MS = 30_000;

const DEFAULT_FILTER_STATE: FilterState = {
  search: "",
  timeframe: DEFAULT_REQUESTS_TIMEFRAME,
  accountIds: [],
  apiKeyIds: [],
  modelOptions: [],
  statuses: [],
  conversationId: null,
  limit: 25,
  offset: 0,
};

export function requestLogFiltersApplied(filters: FilterState): boolean {
  return (
    filters.search.trim() !== "" ||
    filters.timeframe !== DEFAULT_FILTER_STATE.timeframe ||
    filters.accountIds.length > 0 ||
    filters.apiKeyIds.length > 0 ||
    filters.modelOptions.length > 0 ||
    filters.statuses.length > 0 ||
    Boolean(filters.conversationId)
  );
}

// Links from alerts, search and other pages use snake_case parameters; the
// page rewrites them to its own names on the next filter change.
const LINK_PARAM_KEYS = ["api_key_id", "account_id", "request_id", "conversation_id"] as const;

const REQUEST_LOG_PARAM_KEYS = [
  "search",
  "timeframe",
  "accountId",
  "apiKeyId",
  "modelOption",
  "status",
  "conversationId",
  "limit",
  "offset",
  ...LINK_PARAM_KEYS,
] as const;

function parseNumber(value: string | null, fallback: number): number {
  if (value === null) {
    return fallback;
  }
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

export function parseFilterState(params: URLSearchParams): FilterState {
  const candidate = {
    search: params.get("search") ?? params.get("request_id") ?? "",
    timeframe: parseRequestsTimeframe(params.get("timeframe")),
    accountIds: [...params.getAll("accountId"), ...params.getAll("account_id")],
    apiKeyIds: [...params.getAll("apiKeyId"), ...params.getAll("api_key_id")],
    modelOptions: params.getAll("modelOption"),
    statuses: params.getAll("status"),
    conversationId: params.get("conversationId") || params.get("conversation_id") || null,
    limit: parseNumber(params.get("limit"), DEFAULT_FILTER_STATE.limit),
    offset: parseNumber(params.get("offset"), DEFAULT_FILTER_STATE.offset),
  };
  const parsed = FilterStateSchema.safeParse(candidate);
  if (parsed.success) {
    return parsed.data;
  }
  return DEFAULT_FILTER_STATE;
}

function writeFilterState(state: FilterState, base?: URLSearchParams): URLSearchParams {
  const params = new URLSearchParams(base);
  for (const key of REQUEST_LOG_PARAM_KEYS) {
    params.delete(key);
  }
  if (state.search) {
    params.set("search", state.search);
  }
  if (state.timeframe !== DEFAULT_FILTER_STATE.timeframe) {
    params.set("timeframe", state.timeframe);
  }
  for (const value of state.accountIds) {
    params.append("accountId", value);
  }
  for (const value of state.apiKeyIds) {
    params.append("apiKeyId", value);
  }
  for (const value of state.modelOptions) {
    params.append("modelOption", value);
  }
  for (const value of state.statuses) {
    params.append("status", value);
  }
  if (state.conversationId) {
    params.set("conversationId", state.conversationId);
  }
  params.set("limit", String(state.limit));
  params.set("offset", String(state.offset));
  return params;
}

export type UseRequestLogsOptions = {
  enabled?: boolean;
  /** Live polls the first page every 30 s; otherwise nothing is polled. */
  live?: boolean;
  /** Filter options are fetched only once the filter controls are opened. */
  optionsEnabled?: boolean;
};

export function useRequestLogs(options: UseRequestLogsOptions = {}) {
  const enabled = options.enabled ?? true;
  const live = options.live ?? false;
  const optionsEnabled = options.optionsEnabled ?? false;
  const [searchParams, setSearchParams] = useSearchParams();

  const filters = useMemo(() => parseFilterState(searchParams), [searchParams]);
  const filtersApplied = requestLogFiltersApplied(filters);
  const listFilters = useMemo<RequestLogsListFilters>(
    () => ({
      search: filters.search || undefined,
      limit: filters.limit,
      offset: filters.offset,
      accountIds: filters.accountIds,
      apiKeyIds: filters.apiKeyIds,
      statuses: filters.statuses,
      modelOptions: filters.modelOptions,
      timeframe: filters.timeframe,
      conversationId: filters.conversationId ?? undefined,
    }),
    [filters],
  );
  const facetFilters = useMemo<RequestLogFacetFilters>(
    () => ({
      timeframe: filters.timeframe,
      accountIds: filters.accountIds,
      apiKeyIds: filters.apiKeyIds,
      modelOptions: filters.modelOptions,
    }),
    [filters.accountIds, filters.apiKeyIds, filters.modelOptions, filters.timeframe],
  );
  const polling = live && filters.offset === 0;

  const {
    data: logsResult,
    error: logsError,
    isFetching: logsIsFetching,
    isLoading: logsIsLoading,
    isPending: logsIsPending,
    isPlaceholderData: logsIsPlaceholderData,
    isSuccess: logsIsSuccess,
    refetch: refetchLogs,
  } = useQuery({
    queryKey: ["requests", "request-logs", listFilters],
    queryFn: async () => ({
      page: await getRequestLogs(listFilters),
      filtersApplied,
    }),
    enabled,
    refetchInterval: polling ? LIVE_REFRESH_MS : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: polling,
    placeholderData: keepPreviousData,
  });
  const logsData = logsResult?.page;
  const emptyStateFiltersApplied =
    filtersApplied || (logsIsPlaceholderData && Boolean(logsResult?.filtersApplied));
  const logsQuery = {
    data: logsData,
    error: logsError,
    isFetching: logsIsFetching,
    isLoading: logsIsLoading,
    isPending: logsIsPending,
    isPlaceholderData: logsIsPlaceholderData,
    isSuccess: logsIsSuccess,
    refetch: refetchLogs,
  };

  const {
    data: optionsData,
    error: optionsError,
    isFetching: optionsIsFetching,
    isLoading: optionsIsLoading,
    isPending: optionsIsPending,
    isSuccess: optionsIsSuccess,
    refetch: refetchOptions,
  } = useQuery({
    queryKey: ["requests", "request-log-options", facetFilters],
    queryFn: () => getRequestLogOptions(facetFilters),
    enabled: enabled && optionsEnabled,
    staleTime: 5 * 60_000,
    placeholderData: keepPreviousData,
  });
  const optionsQuery = {
    data: optionsData,
    error: optionsError,
    isFetching: optionsIsFetching,
    isLoading: optionsIsLoading,
    isPending: optionsIsPending,
    isSuccess: optionsIsSuccess,
    refetch: refetchOptions,
  };

  const updateFilters = (patch: Partial<FilterState>) => {
    const nextState: FilterState = {
      ...filters,
      ...patch,
    };
    setSearchParams(writeFilterState(nextState, searchParams));
  };

  return {
    filters,
    listFilters,
    facetFilters,
    emptyStateFiltersApplied,
    logsQuery,
    optionsQuery,
    updateFilters,
  };
}
