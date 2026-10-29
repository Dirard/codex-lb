import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { get } from "@/lib/api-client";
import { createReportsResponse } from "@/test/mocks/key-usage-report-factories";
import { useReports } from "./use-reports";

vi.mock("@/lib/api-client", () => ({ get: vi.fn() }));

const getMock = vi.mocked(get);

function wrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: PropsWithChildren) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

function client() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
}

function requestedParams(call = 0): URLSearchParams {
  const [url] = getMock.mock.calls[call] ?? [];
  return new URL(String(url), "http://localhost").searchParams;
}

describe("useReports (traffic details)", () => {
  beforeEach(() => {
    getMock.mockReset();
    getMock.mockResolvedValue(createReportsResponse());
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("requests the period, time zone and resolved key ids", async () => {
    const { result } = renderHook(
      () => useReports({ startDate: "2030-01-09", endDate: "2030-01-15", timezone: "Europe/Prague", apiKeyIds: ["key_1", "key_2"] }, true),
      { wrapper: wrapper(client()) },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(getMock).toHaveBeenCalledTimes(1);
    expect(String(getMock.mock.calls[0][0])).toMatch(/^\/api\/reports\?/);
    const params = requestedParams();
    expect(params.get("start_date")).toBe("2030-01-09");
    expect(params.get("end_date")).toBe("2030-01-15");
    expect(params.get("timezone")).toBe("Europe/Prague");
    expect(params.getAll("api_key_id")).toEqual(["key_1", "key_2"]);
    expect(params.has("account_id")).toBe(false);
    expect(params.has("model")).toBe(false);
  });

  it("sends no key filter for every key and omits an unknown time zone", async () => {
    const { result } = renderHook(
      () => useReports({ startDate: "2030-01-09", endDate: "2030-01-15", timezone: undefined, apiKeyIds: [] }, true),
      { wrapper: wrapper(client()) },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const params = requestedParams();
    expect(params.has("api_key_id")).toBe(false);
    expect(params.has("timezone")).toBe(false);
  });

  it("requests nothing while disabled", async () => {
    renderHook(() => useReports({ startDate: "2030-01-09", endDate: "2030-01-15", apiKeyIds: [] }, false), {
      wrapper: wrapper(client()),
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(getMock).not.toHaveBeenCalled();
  });

  it("does not poll on an interval", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { result } = renderHook(
      () => useReports({ startDate: "2030-01-09", endDate: "2030-01-15", apiKeyIds: [] }, true),
      { wrapper: wrapper(client()) },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(getMock).toHaveBeenCalledTimes(1);
  });
});
