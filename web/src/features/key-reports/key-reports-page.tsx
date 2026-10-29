import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { KeyPage } from "@/features/key-page/components/key-page";

/**
 * The key owner's page after key sign-in: the shared key page in owner mode
 * (R19), without the administrator navigation. Each login owns a separate
 * query cache, cancelled and cleared when the session ends, so neither the
 * credential nor its data ever enters administrator queries.
 */
export function KeyReportsPage({ onExit }: { onExit: (message?: string) => void }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 30_000 } } }));
  useEffect(() => () => {
    void client.cancelQueries();
    client.clear();
  }, [client]);
  return (
    <QueryClientProvider client={client}>
      <main className="mx-auto min-h-screen w-full max-w-[1500px] px-4 py-8 sm:px-6">
        <KeyPage mode="owner" onExit={onExit} />
      </main>
    </QueryClientProvider>
  );
}
