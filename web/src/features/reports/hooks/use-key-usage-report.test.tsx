import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { get } from "@/lib/api-client";
import { createKeyUsageReport } from "@/test/mocks/key-usage-report-factories";
import type { KeyUsageReportParams } from "../api";
import { daysAgoLocalISO, localDateISO } from "../date";
import { keyUsageReportQueryKey, useKeyUsageReport } from "./use-key-usage-report";

vi.mock("@/lib/api-client", () => ({ get: vi.fn() }));

const getMock = vi.mocked(get);

function setup(initial: KeyUsageReportParams, enabled = true) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const view = renderHook(({ params, on }) => useKeyUsageReport(params, on), {
    initialProps: { params: initial, on: enabled },
    wrapper: ({ children }: PropsWithChildren) => createElement(QueryClientProvider, { client: queryClient }, children),
  });
  return { ...view, queryClient };
}

const PAST: KeyUsageReportParams = {
  groupIds: ["grp_1", "grp_2"],
  keyIds: ["key_9"],
  ungrouped: true,
  startDate: "2026-01-01",
  endDate: "2026-01-31",
  timezone: "Europe/Prague",
};

describe("useKeyUsageReport", () => {
  beforeEach(() => {
    getMock.mockReset();
    getMock.mockResolvedValue(createKeyUsageReport());
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("requests the selection and the period with repeatable ids", async () => {
    const { result } = setup(PAST);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const url = new URL(String(getMock.mock.calls[0][0]), "http://localhost");
    expect(url.pathname).toBe("/api/reports/keys");
    expect(url.searchParams.getAll("group_id")).toEqual(["grp_1", "grp_2"]);
    expect(url.searchParams.getAll("api_key_id")).toEqual(["key_9"]);
    expect(url.searchParams.get("ungrouped")).toBe("1");
    expect(url.searchParams.get("start_date")).toBe("2026-01-01");
    expect(url.searchParams.get("end_date")).toBe("2026-01-31");
    expect(url.searchParams.get("timezone")).toBe("Europe/Prague");
    expect(keyUsageReportQueryKey(PAST)).toEqual(["reports", "keys", PAST]);
  });

  it("requests nothing while disabled", async () => {
    setup(PAST, false);
    await act(async () => {
      await Promise.resolve();
    });
    expect(getMock).not.toHaveBeenCalled();
  });

  it("keeps the previous report while the next one loads", async () => {
    const { result, rerender } = setup(PAST);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const first = result.current.data;
    let resolve: (value: unknown) => void = () => {};
    getMock.mockReturnValueOnce(new Promise((next) => (resolve = next)));
    rerender({ params: { ...PAST, groupIds: [] }, on: true });
    expect(result.current.data).toBe(first);
    expect(result.current.isPlaceholderData).toBe(true);
    await act(async () => {
      resolve(createKeyUsageReport({ start: "2026-01-01", end: "2026-01-31" }));
      await Promise.resolve();
    });
    await waitFor(() => expect(result.current.isPlaceholderData).toBe(false));
  });

  it("never polls and refetches on focus only for a period including today once stale", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const today: KeyUsageReportParams = { ...PAST, startDate: daysAgoLocalISO(6), endDate: localDateISO() };
    const { result, queryClient } = setup(today);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(getMock).toHaveBeenCalledTimes(1);

    const observer = queryClient.getQueryCache().find({ queryKey: keyUsageReportQueryKey(today) })?.observers[0];
    expect(observer?.options.refetchOnWindowFocus).toBe(true);
    expect(observer?.options.refetchInterval).toBe(false);
    expect(observer?.options.staleTime).toBe(60_000);
  });

  it("does not refetch on focus for a past period", async () => {
    const { result, queryClient } = setup(PAST);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const observer = queryClient.getQueryCache().find({ queryKey: keyUsageReportQueryKey(PAST) })?.observers[0];
    expect(observer?.options.refetchOnWindowFocus).toBe(false);
  });
});
