import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { createElement, type PropsWithChildren } from "react";
import { describe, expect, it, vi } from "vitest";

import {
  useAccounts,
  useCheckResetCredits,
} from "@/features/accounts/hooks/use-accounts";
import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { createAccountSummary } from "@/test/mocks/factories";
import { createShellStatus } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";

function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        gcTime: 0,
      },
    },
  });
}

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: PropsWithChildren) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

describe("useAccounts", () => {
  it("loads accounts and invalidates related queries after mutations", async () => {
    const queryClient = createTestQueryClient();
    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
    let usageResetBody: unknown;
    server.use(
      http.post("/api/accounts/:accountId/usage-reset-credits/consume", async ({ params, request }) => {
        const accountId = String(params.accountId);
        usageResetBody = await request.json();
        return HttpResponse.json({
          status: "reset",
          accountId,
          code: "reset",
          windowsReset: 2,
          usageWritten: true,
          primaryUsedPercentBefore: 99,
          primaryUsedPercentAfter: 1,
          secondaryUsedPercentBefore: 80,
          secondaryUsedPercentAfter: 1,
          accountStatusBefore: "rate_limited",
          accountStatusAfter: "active",
        });
      }),
    );
    const { result } = renderHook(() => useAccounts(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.accountsQuery.isSuccess).toBe(true));
    const firstAccountId = result.current.accountsQuery.data?.[0]?.accountId;
    expect(firstAccountId).toBeTruthy();

    await result.current.pauseMutation.mutateAsync(firstAccountId as string);
    await result.current.resumeMutation.mutateAsync(firstAccountId as string);
    await result.current.probeMutation.mutateAsync({
      accountId: firstAccountId as string,
    });
    await result.current.usageResetMutation.mutateAsync({
      accountId: firstAccountId as string,
    });
    expect(usageResetBody).toEqual({
      redeemRequestId: expect.any(String),
    });
    const routingPolicyResult = await result.current.routingPolicyMutation.mutateAsync({
      accountId: firstAccountId as string,
      routingPolicy: "preserve",
    });
    expect(routingPolicyResult.routingPolicy).toBe("preserve");

    const imported = await result.current.importMutation.mutateAsync(
      new File(["{}"], "auth.json", { type: "application/json" }),
    );
    await result.current.deleteMutation.mutateAsync({ accountId: imported.accountId, deleteHistory: false });

    await waitFor(() => {
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["accounts", "list"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["accounts", "trends"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["accounts", "reset-credits"] });
      expect(invalidateSpy).toHaveBeenCalledWith({
        queryKey: ["accounts", "trends", firstAccountId],
      });
      expect(invalidateSpy).toHaveBeenCalledWith({
        queryKey: ["accounts", "reset-credits", firstAccountId],
      });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["dashboard", "overview"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["dashboard", "projections"] });
    });
  });

  it("exports auth for an account without invalidating account queries", async () => {
    const queryClient = createTestQueryClient();
    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");

    const { result } = renderHook(() => useAccounts(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.accountsQuery.isSuccess).toBe(true));
    const firstAccountId = result.current.accountsQuery.data?.[0]?.accountId;
    expect(firstAccountId).toBeTruthy();

    await result.current.exportAuthMutation.mutateAsync({ accountId: firstAccountId as string, currentPassword: "synthetic-password" });

    expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["accounts", "list"] });
    expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["accounts", "trends"] });
    expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["accounts", "reset-credits"] });
    expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["dashboard", "overview"] });
    expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["dashboard", "projections"] });
  });

  it("reuses the dashboard usage reset redemption id after a failed attempt", async () => {
    const queryClient = createTestQueryClient();
    const usageResetBodies: unknown[] = [];
    server.use(
      http.post("/api/accounts/:accountId/usage-reset-credits/consume", async ({ params, request }) => {
        const accountId = String(params.accountId);
        usageResetBodies.push(await request.json());
        if (usageResetBodies.length === 1) {
          return HttpResponse.json(
            {
              error: {
                code: "upstream_timeout",
                message: "Upstream response was lost",
              },
            },
            { status: 504 },
          );
        }
        return HttpResponse.json({
          status: "already_redeemed",
          accountId,
          code: "already_redeemed",
          windowsReset: 1,
          usageWritten: true,
          primaryUsedPercentBefore: 99,
          primaryUsedPercentAfter: 1,
          secondaryUsedPercentBefore: 80,
          secondaryUsedPercentAfter: 1,
          accountStatusBefore: "rate_limited",
          accountStatusAfter: "active",
        });
      }),
    );
    const { result } = renderHook(() => useAccounts(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.accountsQuery.isSuccess).toBe(true));

    await expect(
      result.current.usageResetMutation.mutateAsync({ accountId: "acc_primary" }),
    ).rejects.toThrow("Upstream response was lost");
    await result.current.usageResetMutation.mutateAsync({ accountId: "acc_primary" });

    expect(usageResetBodies).toHaveLength(2);
    expect(usageResetBodies[0]).toEqual({
      redeemRequestId: expect.any(String),
    });
    expect(usageResetBodies[1]).toEqual(usageResetBodies[0]);
  });

  it("does not reuse a failed dashboard usage reset redemption id for another account", async () => {
    const queryClient = createTestQueryClient();
    const usageResetBodies: unknown[] = [];
    server.use(
      http.post("/api/accounts/:accountId/usage-reset-credits/consume", async ({ request }) => {
        usageResetBodies.push(await request.json());
        return HttpResponse.json(
          {
            error: {
              code: "upstream_timeout",
              message: "Upstream response was lost",
            },
          },
          { status: 504 },
        );
      }),
    );
    const { result } = renderHook(() => useAccounts(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.accountsQuery.isSuccess).toBe(true));

    await expect(
      result.current.usageResetMutation.mutateAsync({ accountId: "acc_primary" }),
    ).rejects.toThrow("Upstream response was lost");
    await expect(
      result.current.usageResetMutation.mutateAsync({ accountId: "acc_secondary" }),
    ).rejects.toThrow("Upstream response was lost");

    expect(usageResetBodies).toHaveLength(2);
    expect(usageResetBodies[0]).toEqual({
      redeemRequestId: expect.any(String),
    });
    expect(usageResetBodies[1]).toEqual({
      redeemRequestId: expect.any(String),
    });
    expect(usageResetBodies[1]).not.toEqual(usageResetBodies[0]);
  });

  it("checks reset credits only on demand, once, without a polling query", async () => {
    const queryClient = createTestQueryClient();
    const requests: string[] = [];
    server.use(
      http.get("/api/accounts/:accountId/usage-reset-credits", ({ params }) => {
        requests.push(String(params.accountId));
        return HttpResponse.json({ accountId: String(params.accountId), rateLimitResetCredits: { availableCount: 4 } });
      }),
    );

    const { result } = renderHook(() => useCheckResetCredits(), { wrapper: createWrapper(queryClient) });
    expect(requests).toEqual([]);

    const checked = await result.current.mutateAsync("acc_primary");

    expect(requests).toEqual(["acc_primary"]);
    expect(checked).toMatchObject({ accountId: "acc_primary", availableCount: 4 });
    expect(Number.isNaN(Date.parse(checked.checkedAt))).toBe(false);
    expect(queryClient.getQueryCache().findAll({ queryKey: ["accounts", "usage-reset-credits"] })).toEqual([]);
  });

  it("writes the shell status of the list and patches the row after a mutation", async () => {
    const queryClient = createTestQueryClient();
    server.use(
      http.get("/api/accounts", () =>
        HttpResponse.json({
          accounts: [createAccountSummary({ accountId: "acc_row", status: "active", routingPolicy: "normal" })],
          shell: createShellStatus({ reauthRequiredAccounts: 2 }),
        }),
      ),
      // The refetch after the mutation must not race the patched row.
      http.post("/api/accounts/:accountId/pause", () => HttpResponse.json({ status: "paused" })),
    );
    const shellWrites = vi.spyOn(queryClient, "setQueryData");
    const { result } = renderHook(() => useAccounts(), { wrapper: createWrapper(queryClient) });

    await waitFor(() => expect(result.current.accountsQuery.isSuccess).toBe(true));
    expect(shellWrites).toHaveBeenCalledWith(SHELL_STATUS_QUERY_KEY, expect.objectContaining({ reauthRequiredAccounts: 2 }));
    const query = queryClient.getQueryCache().find({ queryKey: ["accounts", "list"] });
    expect((query?.options as { refetchInterval?: unknown } | undefined)?.refetchInterval).toBe(30_000);

    const fetches = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    await result.current.pauseMutation.mutateAsync("acc_row");

    await waitFor(() => expect(result.current.accountsQuery.data?.[0]?.status).toBe("paused"));
    expect(fetches).toHaveBeenCalledWith({ queryKey: ["accounts", "list"] });
  });
});
