import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import { DIRECTORY_QUERY_KEY, getDirectory } from "@/features/search/api";

/** Keys, groups and accounts for search and pickers; fetched on demand and
 * kept for 5 minutes, invalidated by key, group and account mutations. */
export function useDirectory(enabled = true) {
  return useQuery({
    queryKey: DIRECTORY_QUERY_KEY,
    queryFn: getDirectory,
    enabled,
    staleTime: 5 * 60_000,
  });
}

export function useInvalidateDirectory() {
  const queryClient = useQueryClient();
  return useCallback(() => queryClient.invalidateQueries({ queryKey: DIRECTORY_QUERY_KEY }), [queryClient]);
}
