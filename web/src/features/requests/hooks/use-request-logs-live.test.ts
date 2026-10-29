import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { createElement, type PropsWithChildren } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { useRequestLogs } from "@/features/requests/hooks/use-request-logs";
import { server } from "@/test/mocks/server";

function wrapper(entry: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return function Wrapper({ children }: PropsWithChildren) {
    return createElement(QueryClientProvider, { client: queryClient }, createElement(MemoryRouter, { initialEntries: [entry] }, children));
  };
}

describe("useRequestLogs on the Requests page", () => {
  it("defaults to 24 hours, fetches no options until asked and does not poll without Live (R18)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const listCalls: URLSearchParams[] = [];
    let optionCalls = 0;
    server.use(
      http.get("/api/request-logs", ({ request }) => {
        listCalls.push(new URL(request.url).searchParams);
        return HttpResponse.json({ requests: [], total: 0, hasMore: false });
      }),
      http.get("/api/request-logs/options", () => {
        optionCalls += 1;
        return HttpResponse.json({ accountIds: [], apiKeys: [], modelOptions: [], statuses: [] });
      }),
    );
    try {
      const { result, rerender } = renderHook(
        ({ optionsEnabled }: { optionsEnabled: boolean }) => useRequestLogs({ optionsEnabled }),
        { wrapper: wrapper("/requests"), initialProps: { optionsEnabled: false } },
      );
      await waitFor(() => expect(result.current.logsQuery.isSuccess).toBe(true));
      expect(result.current.filters.timeframe).toBe("24h");
      expect(listCalls[0]?.get("timeframe")).toBe("24h");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(65_000);
      });
      expect(listCalls).toHaveLength(1);
      expect(optionCalls).toBe(0);

      rerender({ optionsEnabled: true });
      await waitFor(() => expect(optionCalls).toBe(1));
    } finally {
      vi.useRealTimers();
    }
  });

  it("polls the first page every 30 s while Live is on", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let listCalls = 0;
    server.use(
      http.get("/api/request-logs", () => {
        listCalls += 1;
        return HttpResponse.json({ requests: [], total: 0, hasMore: false });
      }),
    );
    try {
      const { result } = renderHook(() => useRequestLogs({ live: true }), { wrapper: wrapper("/requests") });
      await waitFor(() => expect(result.current.logsQuery.isSuccess).toBe(true));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(31_000);
      });
      await waitFor(() => expect(listCalls).toBeGreaterThanOrEqual(2));
    } finally {
      vi.useRealTimers();
    }
  });

  it("prefills filters from link parameters of alerts, search and the key page", async () => {
    const listCalls: URLSearchParams[] = [];
    server.use(
      http.get("/api/request-logs", ({ request }) => {
        listCalls.push(new URL(request.url).searchParams);
        return HttpResponse.json({ requests: [], total: 0, hasMore: false });
      }),
    );
    const { result } = renderHook(() => useRequestLogs(), {
      wrapper: wrapper("/requests?api_key_id=key_1&status=error&timeframe=1h&account_id=acc_1&conversation_id=conv_1"),
    });
    await waitFor(() => expect(result.current.logsQuery.isSuccess).toBe(true));
    expect(result.current.filters).toMatchObject({
      apiKeyIds: ["key_1"],
      accountIds: ["acc_1"],
      statuses: ["error"],
      timeframe: "1h",
      conversationId: "conv_1",
    });
    expect(listCalls[0]?.getAll("apiKeyId")).toEqual(["key_1"]);
    expect(listCalls[0]?.get("timeframe")).toBe("1h");

    const byRequest = renderHook(() => useRequestLogs(), { wrapper: wrapper("/requests?request_id=req_ABCDEFGH1234") });
    await waitFor(() => expect(byRequest.result.current.filters.search).toBe("req_ABCDEFGH1234"));
  });
});
