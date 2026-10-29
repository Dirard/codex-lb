import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";

import {
  fetchKeysPage,
  keysParamsFromSearch,
  keysQueryKey,
  keysSearch,
  type KeysListParams,
} from "@/features/keys/api";
import { browserTimeZone } from "@/features/overview/api";
import { ApiError } from "@/lib/api-client";

/** The keys page polls its current page every 60 s while visible (R35). */
export const KEYS_POLL_MS = 60_000;

function retryable(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false;
  return failureCount < 1;
}

export type UpdateKeysParams = (patch: Partial<KeysListParams>, options?: { replace?: boolean }) => void;

/**
 * The keys page state in the URL under the parameter names of GET /api/keys,
 * so links such as `/keys?near_limit=1` open those filters. Changing a filter
 * or the sort returns to the first page.
 */
export function useKeysParams(): { params: KeysListParams; update: UpdateKeysParams } {
  const [search, setSearch] = useSearchParams();
  const params = useMemo(() => keysParamsFromSearch(search), [search]);
  const update = useCallback<UpdateKeysParams>(
    (patch, options) => {
      setSearch(
        (current) => {
          const next = { ...keysParamsFromSearch(current), ...patch };
          if (!("offset" in patch)) next.offset = 0;
          return keysSearch(next);
        },
        { replace: options?.replace ?? false },
      );
    },
    [setSearch],
  );
  return { params, update };
}

/**
 * The keys page's only periodic request: its current page every 60 s while
 * the tab is visible. Previous data stays visible while a refetch runs or
 * fails.
 */
export function useKeysList() {
  const queryClient = useQueryClient();
  const timezone = useMemo(() => browserTimeZone(), []);
  const { params, update } = useKeysParams();
  const query = useQuery({
    queryKey: keysQueryKey(params, timezone),
    queryFn: ({ signal }) => fetchKeysPage(queryClient, params, timezone, signal),
    refetchInterval: KEYS_POLL_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    placeholderData: keepPreviousData,
    staleTime: 10_000,
    retry: retryable,
  });
  return { params, update, query, timezone };
}

export type KeySelection = {
  ids: ReadonlySet<string>;
  set: (ids: string[], selected: boolean) => void;
  replace: (ids: string[]) => void;
  clear: () => void;
};

/** The selected key ids; they survive polls, sorting and page changes until cleared (R35). */
export function useKeySelection(): KeySelection {
  const [ids, setIds] = useState<ReadonlySet<string>>(() => new Set());
  const set = useCallback((changed: string[], selected: boolean) => {
    setIds((current) => {
      const next = new Set(current);
      for (const id of changed) {
        if (selected) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);
  const replace = useCallback((next: string[]) => setIds(new Set(next)), []);
  const clear = useCallback(() => setIds(new Set()), []);
  return { ids, set, replace, clear };
}
