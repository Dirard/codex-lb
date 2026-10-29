import type { QueryClient } from "@tanstack/react-query";

/**
 * Key, group, account and settings mutations change what the overview, the
 * search directory, the shell status and the keys list show; mark them stale.
 */
export function invalidateEntityViews(queryClient: QueryClient): Promise<unknown> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ["directory"] }),
    queryClient.invalidateQueries({ queryKey: ["overview"] }),
    queryClient.invalidateQueries({ queryKey: ["shell", "status"] }),
    // The keys list shows each key's group and limits.
    queryClient.invalidateQueries({ queryKey: ["keys"] }),
  ]);
}
